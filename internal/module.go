package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	manifest "github.com/Muxcore-Media/downloader-native-torrent"
	"github.com/Muxcore-Media/downloader-native-torrent/internal/meshstore"
	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

type Module struct {
	downloaderv1.UnimplementedTorrentServiceServer
	mu       sync.RWMutex
	torrents map[string]*torrentHandle

	vpn    *vpnManager
	natPMP *natPMPClient
	engine torrentEngine
	mc     *client.Client
	mesh   *meshstore.Client // non-nil when DOWNLOAD_STORAGE=mesh

	id               string
	grpcAddr         string
	dlDir            string
	listenPort       int
	seedRatio        float64
	seedMinutes      int
	wgConfPath       string
	wgKillSwitch     bool
	filePriorityMode string
	enableDHT        bool
	enablePEX        bool
	grpcSrv          *grpc.Server
	lis              net.Listener
	infoTimeout      time.Duration

	persistMu sync.Mutex
	stopping  bool

	meshSessions meshSessionStore // optional test hook for torrent/sessions.json
}

// filePriorityApplier is optional on managedTorrent (anacrolix).
type filePriorityApplier interface {
	ApplyFilePriorities(mode string)
}

type torrentHandle struct {
	ID           string
	URI          string
	Name         string
	InfoHash     string
	Label        string
	SavePath     string
	AddedAt      time.Time
	Status       string
	mu           sync.RWMutex
	TotalSize    int64
	Downloaded   int64
	Peers        int32
	Seeders      int32
	UploadRate   int32
	DownloadRate int32
	Uploaded     int64
	ErrorStr     string
	CompletedAt  *time.Time
	Files        []fileInfo

	session  managedTorrent
	cancel   context.CancelFunc
	done     chan struct{}
	resumeCh chan struct{}
}

type fileInfo struct {
	Path       string
	Size       int64
	Downloaded int64
	Wanted     bool
}

type Config struct {
	ID               string
	GRPCAddr         string
	DownloadDir      string
	WGConfPath       string
	WGKillSwitch     bool
	NatPMPPort       int
	ListenPort       int
	SeedRatio        float64
	SeedMinutes      int
	FilePriorityMode string
	EnableDHT        *bool // nil → env/default true
	EnablePEX        *bool
	Engine           torrentEngine // optional; tests inject a fake
	InfoTimeout      time.Duration
	MeshSessions     meshSessionStore
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "downloader-native-torrent"
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = ":9461"
	}
	if cfg.DownloadDir == "" {
		cfg.DownloadDir = "/var/lib/downloader-native-torrent/downloads"
	}
	if cfg.ListenPort == 0 {
		cfg.ListenPort = 6881
	}
	if cfg.SeedRatio == 0 {
		cfg.SeedRatio = 1.0
	}
	if cfg.SeedMinutes == 0 {
		cfg.SeedMinutes = 60
	}
	if cfg.InfoTimeout == 0 {
		cfg.InfoTimeout = 10 * time.Minute
	}
	if v := os.Getenv("DOWNLOADER_GRPC_ADDR"); v != "" {
		cfg.GRPCAddr = v
	}
	if v := os.Getenv("DOWNLOAD_DIR"); v != "" {
		cfg.DownloadDir = v
	}
	if v := os.Getenv("WG_CONF"); v != "" {
		cfg.WGConfPath = v
	}
	if v := os.Getenv("WG_KILL_SWITCH"); v == "true" {
		cfg.WGKillSwitch = true
	}
	if v := os.Getenv("NAT_PMP_PORT"); v != "" {
		_, _ = fmt.Sscanf(v, "%d", &cfg.NatPMPPort)
	}
	if v := os.Getenv("TORRENT_LISTEN_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			cfg.ListenPort = p
		}
	} else if cfg.NatPMPPort > 0 {
		cfg.ListenPort = cfg.NatPMPPort
	}
	if v := os.Getenv("SEED_RATIO"); v != "" {
		if r, err := strconv.ParseFloat(v, 64); err == nil && r > 0 {
			cfg.SeedRatio = r
		}
	}
	if v := os.Getenv("SEED_MINUTES"); v != "" {
		if m, err := strconv.Atoi(v); err == nil && m > 0 {
			cfg.SeedMinutes = m
		}
	}
	filePri := normalizeFilePriorityMode(cfg.FilePriorityMode)
	if cfg.FilePriorityMode == "" {
		if v := os.Getenv("TORRENT_FILE_PRIORITY"); v != "" {
			filePri = normalizeFilePriorityMode(v)
		}
	}
	enableDHT := true
	if cfg.EnableDHT != nil {
		enableDHT = *cfg.EnableDHT
	} else if v := os.Getenv("TORRENT_ENABLE_DHT"); v != "" {
		enableDHT = envTruthyBool(v)
	}
	enablePEX := true
	if cfg.EnablePEX != nil {
		enablePEX = *cfg.EnablePEX
	} else if v := os.Getenv("TORRENT_ENABLE_PEX"); v != "" {
		enablePEX = envTruthyBool(v)
	}

	return &Module{
		id:               cfg.ID,
		grpcAddr:         cfg.GRPCAddr,
		dlDir:            cfg.DownloadDir,
		listenPort:       cfg.ListenPort,
		seedRatio:        cfg.SeedRatio,
		seedMinutes:      cfg.SeedMinutes,
		infoTimeout:      cfg.InfoTimeout,
		wgConfPath:       cfg.WGConfPath,
		wgKillSwitch:     cfg.WGKillSwitch,
		filePriorityMode: filePri,
		enableDHT:        enableDHT,
		enablePEX:        enablePEX,
		engine:           cfg.Engine,
		torrents:         make(map[string]*torrentHandle),
		vpn:              newVPNManager(),
		natPMP:           newNATPMPClient(),
		meshSessions:     cfg.MeshSessions,
	}
}

func envTruthyBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:           m.id,
		Name:         "Downloader Native Torrent",
		Version:      modulesdk.ManifestVersion(manifest.ManifestJSON),
		Roles:        []string{"downloader"},
		Description:  "Native torrent download engine (anacrolix) with WireGuard VPN and NAT-PMP support",
		Author:       "MuxCore",
		Capabilities: []string{"downloader", "downloader.torrent", "downloader.native.torrent", "settings"},
		Contracts: []contracts.ContractDeclaration{
			{
				Repo:      "github.com/Muxcore-Media/contracts-downloader",
				Interface: "Downloader",
				Version:   "v0.1.0",
			},
		},
		MinCoreVersion: "0.4.0",
		HTTPAddr:       m.grpcAddr,
	}
}

func (m *Module) Init(ctx context.Context) error {
	engineEnv := os.Getenv("DOWNLOADER_ENGINE")
	if m.engine == nil {
		if err := enforceLiveDownloaderVPN(m.wgConfPath, engineEnv); err != nil {
			return err
		}
	}
	// Mesh storage needs the core dial before the engine; local mode needs the dir now.
	if storageMode() == "local" {
		if err := os.MkdirAll(m.dlDir, 0755); err != nil {
			return fmt.Errorf("create download dir: %w", err)
		}
	}
	if m.engine == nil {
		mode, err := resolveEngineMode(engineEnv)
		if err != nil {
			return err
		}
		switch mode {
		case engineModeFixture:
			slog.Info("downloader engine: fixture (no network; default)")
			m.engine = m.newFixtureEngine()
		default:
			slog.Warn("downloader engine: LIVE acquisition enabled (DOWNLOADER_ENGINE=live, VPN configured)")
			if storageMode() == "local" {
				eng, err := newLiveEngine(m.dlDir, m.listenPort, anacrolixEngineOpts{
					EnableDHT: m.enableDHT,
					EnablePEX: m.enablePEX,
				})
				if err != nil {
					return err
				}
				m.engine = eng
			}
			// mesh mode: engine created in dialCore after StorageClient is available
		}
	}
	lis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", m.grpcAddr, err)
	}
	m.lis = lis
	slog.Info("downloader-native-torrent initialized",
		"addr", m.grpcAddr, "storage", storageMode(), "dir", m.dlDir, "listen_port", m.listenPort,
		"file_priority", m.filePriorityMode, "dht", m.enableDHT, "pex", m.enablePEX)

	if autoStartVPNOnInit {
		go m.autoStartVPN()
	}
	return nil
}

// autoStartVPNOnInit is false only in tests that must not bring up WireGuard.
var autoStartVPNOnInit = true

func (m *Module) autoStartVPN() {
	cfgPath := m.wgConfPath
	if cfgPath == "" {
		cfgPath = os.Getenv("WG_CONF")
	}
	if cfgPath == "" {
		return
	}

	ks := m.wgKillSwitch || os.Getenv("WG_KILL_SWITCH") == "true"
	if err := validateKillSwitch(ks); err != nil {
		slog.Warn("auto-start wireguard skipped kill switch", "error", err)
		ks = false
	}
	slog.Info("auto-starting wireguard", "config", cfgPath)

	if err := m.vpn.start(cfgPath, ks); err != nil {
		slog.Warn("auto-start wireguard failed", "error", err)
		return
	}

	if ks {
		slog.Info("kill switch enabled - all non-VPN traffic blocked")
	}

	_, iface, localIP, _, _, _, _ := m.vpn.status()
	m.bindAfterVPN(cfgPath, iface, localIP, 0, false)
}

// bindAfterVPN maps a Proton NAT-PMP port when the conf enables it (or forceNATPMP),
// then rebinds the torrent client to the WireGuard IPv4 address and assigned listen port.
// fallbackPort 0 uses m.listenPort (TORRENT_LISTEN_PORT, default 6881).
func (m *Module) bindAfterVPN(cfgPath, iface, localIP string, fallbackPort int, forceNATPMP bool) {
	if fallbackPort <= 0 {
		fallbackPort = m.listenPort
	}
	cfgData, _ := os.ReadFile(cfgPath)
	if forceNATPMP || hasNATPMPEnabled(string(cfgData)) {
		gw := natPMPGateway(parseConfig(string(cfgData)))
		m.natPMP.setOnPortChange(func(p int) {
			m.rebindTorrent(localIP, p, iface)
		})
		if err := m.natPMP.start(gw, localIP); err != nil {
			slog.Warn("auto-start nat-pmp failed", "error", err)
			m.rebindTorrent(localIP, fallbackPort, iface)
			return
		}
		return
	}
	m.rebindTorrent(localIP, fallbackPort, iface)
}

func (m *Module) rebindTorrent(host string, port int, iface string) {
	if host == "" || port <= 0 {
		return
	}
	m.mu.Lock()
	m.listenPort = port
	m.mu.Unlock()
	if err := m.rebindEngine(host, port); err != nil {
		slog.Warn("vpn: bind torrent client to wireguard ip failed", "ip", host, "port", port, "error", err)
		return
	}
	slog.Info("torrent client bound to wireguard", "interface", iface, "ip", host, "port", port)
}

func (m *Module) Start(ctx context.Context) error {
	m.grpcSrv = grpc.NewServer()
	downloaderv1.RegisterTorrentServiceServer(m.grpcSrv, m)
	cdlv1.RegisterDownloaderServiceServer(m.grpcSrv, &contractsServer{m: m})
	m.registerSettingsMesh(m.grpcSrv)
	go func() {
		slog.Info("downloader-native-torrent gRPC service started", "addr", m.grpcAddr)
		if err := m.grpcSrv.Serve(m.lis); err != nil {
			slog.Error("downloader-native-torrent gRPC serve error", "error", err)
		}
	}()
	go func() {
		m.dialCore(context.Background())
		if err := m.ensureEngine(context.Background()); err != nil {
			slog.Error("torrent engine not ready", "error", err)
			return
		}
		m.restorePersistedTorrents()
		go m.repairIncompleteAssemblies()
	}()
	return nil
}

func (m *Module) dialCore(ctx context.Context) {
	meshAddr := os.Getenv("MUXCORE_GRPC_ADDR")
	if meshAddr == "" {
		return // optional — events soft-skip without mesh
	}
	insecureMode := os.Getenv("MUXCORE_INSECURE_DISABLE_TLS") == "true" || os.Getenv("MUXCORE_GRPC_INSECURE") == "true"
	var opts []client.Option
	if insecureMode {
		opts = append(opts, client.WithInsecure())
	}
	c, err := client.Dial(meshAddr, opts...)
	if err != nil {
		slog.Warn("downloader: dial core failed", "error", err)
		return
	}
	m.mu.Lock()
	m.mc = c
	m.mu.Unlock()
	slog.Info("downloader: connected to core mesh", "addr", meshAddr)
}

func (m *Module) Stop(ctx context.Context) error {
	m.persistActiveTorrents()
	m.mu.Lock()
	m.stopping = true
	for _, th := range m.torrents {
		if th.cancel != nil {
			th.cancel()
		}
	}
	m.mu.Unlock()

	if m.engine != nil {
		_ = m.engine.Close()
	}
	m.natPMP.stop()
	_ = m.vpn.stop()
	if m.mc != nil {
		_ = m.mc.Close()
	}
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	slog.Info("downloader-native-torrent stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
	if storageMode() == "mesh" {
		m.mu.RLock()
		mc := m.mc
		m.mu.RUnlock()
		if mc == nil {
			return fmt.Errorf("mesh storage: not connected to core")
		}
		if _, err := mc.Storage.Capabilities(ctx); err != nil {
			return fmt.Errorf("mesh storage: %w", err)
		}
		return nil
	}
	_, err := os.Stat(m.dlDir)
	if err != nil {
		return fmt.Errorf("download dir %s: %w", m.dlDir, err)
	}
	return nil
}

// ── Torrent operations ────────────────────────────────────────

func (m *Module) AddTorrent(ctx context.Context, req *downloaderv1.AddTorrentRequest) (*downloaderv1.AddTorrentResponse, error) {
	uri := req.GetUri()
	if uri == "" {
		return nil, fmt.Errorf("uri is required")
	}
	if _, err := classifyURI(uri); err != nil {
		return nil, err
	}
	if err := m.ensureEngine(ctx); err != nil {
		return nil, err
	}
	hash := strings.ToLower(parseInfoHash(uri))
	var savePath string
	if storageMode() == "mesh" {
		if hash == "" {
			// magnet without hash in URI is rare; placeholder until metadata
			savePath = "storage://torrent/pending"
		} else {
			savePath = storageSavePath(hash)
		}
	} else {
		savePath = resolveSavePath(m.dlDir, req.GetSavePath())
	}
	if hash != "" {
		if existing := m.findHandleByInfoHash(hash); existing != nil {
			if _, err := m.engine.AddURI(ctx, uri, existing.SavePath); err != nil {
				slog.Debug("merge trackers for existing torrent", "hash", hash, "error", err)
			}
			existing.mu.RLock()
			name, infoHash := existing.Name, existing.InfoHash
			id := existing.ID
			existing.mu.RUnlock()
			return &downloaderv1.AddTorrentResponse{Id: id, Name: name, InfoHash: infoHash}, nil
		}
	}
	if storageMode() != "mesh" {
		if err := mkdirAllLocal(savePath, 0755); err != nil {
			return nil, fmt.Errorf("create save path: %w", err)
		}
	}

	if err := m.requireVPNReadyNow(); err != nil {
		return nil, err
	}

	th := m.spawnTorrent("", uri, savePath, req.GetLabel(), req.GetPaused())
	m.persistActiveTorrents()

	th.mu.Lock()
	name, infoHash := th.Name, th.InfoHash
	if storageMode() == "mesh" && infoHash != "" && !strings.Contains(th.SavePath, infoHash) {
		th.SavePath = storageSavePath(infoHash)
	}
	curSavePath := th.SavePath
	th.mu.Unlock()
	slog.Info("torrent added", "id", th.ID, "name", name, "save_path", curSavePath)
	return &downloaderv1.AddTorrentResponse{
		Id: th.ID, Name: name, InfoHash: infoHash,
	}, nil
}

func (m *Module) findHandleByInfoHash(hash string) *torrentHandle {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, th := range m.torrents {
		th.mu.RLock()
		h := strings.ToLower(th.InfoHash)
		st := th.Status
		th.mu.RUnlock()
		if h == hash && (st == "queued" || st == "downloading" || st == "paused") {
			return th
		}
	}
	return nil
}

func pieceLayoutOf(session managedTorrent) (pieceLayout, bool) {
	type provider interface {
		PieceLayout() (pieceLayout, bool)
	}
	p, ok := session.(provider)
	if !ok {
		return pieceLayout{}, false
	}
	return p.PieceLayout()
}

// resolveIndexerURI turns Prowlarr/indexer HTTP download links into magnets
// (and mesh save paths) before AddURI so sessions never persist ephemeral URLs
// stuck at storage://torrent/pending.
func (m *Module) resolveIndexerURI(ctx context.Context, th *torrentHandle, uri string) string {
	kind, err := classifyURI(uri)
	if err != nil || kind != "http" {
		return uri
	}
	var hc *http.Client
	if eng, ok := m.engine.(*anacrolixEngine); ok && eng.http != nil {
		hc = eng.http
	}
	_, magnet, ferr := fetchTorrentFile(ctx, hc, uri)
	if ferr != nil || magnet == "" {
		if ferr != nil {
			slog.Debug("indexer http resolve deferred to AddURI", "id", th.ID, "error", ferr)
		}
		return uri
	}
	hash := strings.ToLower(parseInfoHash(magnet))
	th.mu.Lock()
	th.URI = magnet
	if hash != "" {
		th.InfoHash = hash
		if storageMode() == "mesh" {
			cur := th.SavePath
			if dest, ok := meshPendingDest(cur, hash); ok {
				th.SavePath = dest
				slog.Info("mesh pending save path set to infohash", "from", cur, "to", dest)
			}
		}
	}
	th.mu.Unlock()
	m.persistActiveTorrents()
	slog.Info("resolved indexer http to magnet", "id", th.ID, "infohash", hash)
	return magnet
}

func (m *Module) runTorrent(ctx context.Context, th *torrentHandle, uri string, paused bool) {
	defer close(th.done)

	if err := m.requireVPNConnected(ctx); err != nil {
		m.failTorrent(th, err)
		return
	}

	uri = m.resolveIndexerURI(ctx, th, uri)

	session, err := m.engine.AddURI(ctx, uri, th.SavePath)
	if err != nil {
		if alt := magnetFallbackURI(uri, th.SavePath); alt != "" && alt != uri {
			slog.Warn("add uri failed; retrying with magnet from save path", "id", th.ID, "error", err)
			uri = alt
			th.mu.Lock()
			th.URI = alt
			th.mu.Unlock()
			session, err = m.engine.AddURI(ctx, uri, th.SavePath)
		}
		if err != nil {
			m.failTorrent(th, err)
			return
		}
	}
	th.mu.Lock()
	th.session = session
	th.mu.Unlock()

	// Relocate mesh pending as soon as the infohash is known (magnet xt= or
	// HTTP→magnet redirect), before WaitInfo — otherwise pieces land under pending.
	if hash := strings.ToLower(parseInfoHash(uri)); hash != "" {
		th.mu.Lock()
		if th.InfoHash == "" {
			th.InfoHash = hash
		}
		th.mu.Unlock()
		if session, err = m.relocatePendingAfterMetadata(ctx, th, session, uri); err != nil {
			m.failTorrent(th, err)
			return
		}
	}

	infoCtx, infoCancel := context.WithTimeout(ctx, m.infoTimeout)
	defer infoCancel()
	if err := session.WaitInfo(infoCtx); err != nil {
		if rescued, rerr := m.rescueWaitInfoWithCache(ctx, th, session, uri); rerr == nil {
			session = rescued
		} else {
			session.Drop()
			m.failTorrent(th, fmt.Errorf("wait metadata: %w", err))
			return
		}
	}
	m.syncHandleFromTorrent(th, session)
	if m.canonicalizePersistedURI(th) {
		th.mu.RLock()
		uri = th.URI
		th.mu.RUnlock()
		m.persistActiveTorrents()
	}
	m.persistMeshMetainfo(session)
	session, err = m.relocatePendingAfterMetadata(ctx, th, session, uri)
	if err != nil {
		m.failTorrent(th, err)
		return
	}
	if layout, ok := pieceLayoutOf(session); ok {
		if linked, err := tryLinkSiblingPartials(th.SavePath, layout); err != nil {
			slog.Debug("sibling partial link", "error", err)
		} else if linked {
			session.Drop()
			session, err = m.engine.AddURI(ctx, uri, th.SavePath)
			if err != nil {
				m.failTorrent(th, err)
				return
			}
			th.mu.Lock()
			th.session = session
			th.mu.Unlock()
			relinkCtx, relinkCancel := context.WithTimeout(ctx, m.infoTimeout)
			if err := session.WaitInfo(relinkCtx); err != nil {
				relinkCancel()
				session.Drop()
				m.failTorrent(th, fmt.Errorf("wait metadata: %w", err))
				return
			}
			relinkCancel()
			m.syncHandleFromTorrent(th, session)
		}
	}

	if paused {
		th.mu.Lock()
		th.Status = "paused"
		th.mu.Unlock()
		m.persistActiveTorrents()
		slog.Info("torrent paused after metadata", "id", th.ID)
		select {
		case <-ctx.Done():
			session.Drop()
			return
		case <-th.resumeCh:
		}
	}

	m.beginDownload(session)
	th.mu.Lock()
	th.Status = "downloading"
	th.mu.Unlock()
	m.publishDownloadEvent(contracts.EventDownloadStarted, th, "")

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastBytes int64
	var lastUp int64
	lastTick := time.Now()

	for {
		select {
		case <-ctx.Done():
			session.Drop()
			return
		case <-ticker.C:
			th.mu.RLock()
			st := th.Status
			th.mu.RUnlock()
			if st == "paused" {
				continue
			}
			completed := session.BytesCompleted()
			missing := session.BytesMissing()
			uploaded := session.BytesUploaded()
			now := time.Now()
			elapsed := now.Sub(lastTick).Seconds()
			var dlRate, upRate int32
			if elapsed > 0 {
				dlRate = int32(float64(completed-lastBytes) / elapsed)
				upRate = int32(float64(uploaded-lastUp) / elapsed)
				if dlRate < 0 {
					dlRate = 0
				}
				if upRate < 0 {
					upRate = 0
				}
			}
			lastBytes = completed
			lastUp = uploaded
			lastTick = now

			files := session.Files()
			for i := range files {
				if files[i].Size > 0 && completed >= session.TotalLength() {
					files[i].Downloaded = files[i].Size
				}
			}

			th.mu.Lock()
			th.Downloaded = completed
			th.TotalSize = session.TotalLength()
			th.Uploaded = uploaded
			th.Peers = int32(session.ActivePeers())
			th.Seeders = int32(session.ConnectedSeeders())
			th.DownloadRate = dlRate
			th.UploadRate = upRate
			if len(files) > 0 {
				th.Files = files
			}
			th.mu.Unlock()

			if missing == 0 && session.TotalLength() > 0 {
				m.markCompleted(th, session)
				m.seedUntilDone(ctx, th, session)
				session.Drop()
				return
			}
		}
	}
}

// rescueWaitInfoWithCache re-adds via a cached .torrent when DHT metadata stalls.
func (m *Module) rescueWaitInfoWithCache(ctx context.Context, th *torrentHandle, session managedTorrent, uri string) (managedTorrent, error) {
	th.mu.RLock()
	hash, save, name := th.InfoHash, th.SavePath, th.Name
	th.mu.RUnlock()
	if hash == "" {
		hash = parseInfoHash(uri)
	}
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) != 40 {
		return nil, fmt.Errorf("no infohash for metadata rescue")
	}
	if _, err := fetchTorrentMetainfoWith(ctx, hash, defaultMetainfoHTTPClient(), m.mesh); err != nil {
		return nil, err
	}
	session.Drop()
	magnet := magnetFromInfoHash(hash, name)
	if magnet == "" {
		magnet = uri
	}
	th.mu.Lock()
	th.URI = magnet
	th.InfoHash = hash
	th.mu.Unlock()
	newSession, err := m.engine.AddURI(ctx, magnet, save)
	if err != nil {
		return nil, err
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := newSession.WaitInfo(waitCtx); err != nil {
		newSession.Drop()
		return nil, err
	}
	th.mu.Lock()
	th.session = newSession
	th.mu.Unlock()
	slog.Info("rescued torrent metadata from cache", "id", th.ID, "infohash", hash)
	return newSession, nil
}

func (m *Module) relocatePendingAfterMetadata(ctx context.Context, th *torrentHandle, session managedTorrent, uri string) (managedTorrent, error) {
	th.mu.RLock()
	cur, hash := th.SavePath, th.InfoHash
	th.mu.RUnlock()
	if storageMode() == "mesh" {
		if dest, ok := meshPendingDest(cur, hash); ok {
			th.mu.Lock()
			th.SavePath = dest
			th.mu.Unlock()
			slog.Info("mesh pending save path set to infohash", "from", cur, "to", dest)
			m.persistActiveTorrents()
		}
		return session, nil
	}
	dest, ok := hashPartialDest(cur, hash)
	if !ok {
		return session, nil
	}
	session.Drop()
	if err := movePendingPartial(cur, dest); err != nil {
		slog.Warn("rename pending partial", "from", cur, "to", dest, "error", err)
		dest = cur
	} else {
		th.mu.Lock()
		th.SavePath = dest
		th.mu.Unlock()
		if dest != cur {
			slog.Info("renamed pending partial to infohash", "from", cur, "to", dest)
			m.persistActiveTorrents()
		}
	}
	session, err := m.engine.AddURI(ctx, uri, dest)
	if err != nil {
		return nil, err
	}
	th.mu.Lock()
	th.session = session
	th.mu.Unlock()
	infoCtx, cancel := context.WithTimeout(ctx, m.infoTimeout)
	defer cancel()
	if err := session.WaitInfo(infoCtx); err != nil {
		session.Drop()
		return nil, fmt.Errorf("wait metadata: %w", err)
	}
	m.syncHandleFromTorrent(th, session)
	return session, nil
}

func (m *Module) persistMeshMetainfo(session managedTorrent) {
	if storageMode() != "mesh" || m.mesh == nil {
		return
	}
	at, ok := session.(*anacrolixTorrent)
	if !ok || at.t == nil || at.t.Info() == nil {
		return
	}
	var buf bytes.Buffer
	mi := at.t.Metainfo()
	if err := mi.Write(&buf); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := m.mesh.PutMeta(ctx, at.t.InfoHash(), buf.Bytes()); err != nil {
		slog.Debug("persist mesh metainfo", "error", err)
	}
}

func (m *Module) syncHandleFromTorrent(th *torrentHandle, session managedTorrent) {
	th.mu.Lock()
	defer th.mu.Unlock()
	if n := session.Name(); n != "" {
		th.Name = n
	}
	if h := session.InfoHash(); h != "" {
		th.InfoHash = h
	}
	th.TotalSize = session.TotalLength()
	th.Files = session.Files()
	m.syncFileWanted(th, session)
}

func (m *Module) syncFileWanted(th *torrentHandle, session managedTorrent) {
	m.mu.RLock()
	mode := m.filePriorityMode
	m.mu.RUnlock()
	paths := make([]string, len(th.Files))
	for i, f := range th.Files {
		paths[i] = f.Path
	}
	want := selectFilesToDownload(paths, mode)
	for i := range th.Files {
		if len(want) == len(th.Files) {
			th.Files[i].Wanted = want[i]
		} else {
			th.Files[i].Wanted = true
		}
	}
	_ = session
}

func (m *Module) markCompleted(th *torrentHandle, session managedTorrent) {
	now := time.Now()
	files := session.Files()
	for i := range files {
		files[i].Downloaded = files[i].Size
	}
	if storageMode() == "mesh" && m.mesh != nil {
		if at, ok := session.(*anacrolixTorrent); ok {
			info := at.t.Info()
			ih := at.t.InfoHash()
			if info != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
				assembled, err := m.mesh.AssembleFiles(ctx, info, ih)
				cancel()
				if err != nil {
					slog.Warn("mesh assemble files", "id", th.ID, "error", err, "assembled", len(assembled))
				}
				if err != nil || len(assembled) == 0 {
					msg := "mesh assemble produced no files"
					if err != nil {
						msg = fmt.Sprintf("mesh assemble: %v", err)
					}
					m.failTorrent(th, fmt.Errorf("%s", msg))
					return
				}
				files = make([]fileInfo, 0, len(assembled))
				for _, a := range assembled {
					files = append(files, fileInfo{Path: a.URI, Size: a.Size, Downloaded: a.Size})
				}
				th.mu.Lock()
				th.SavePath = storageSavePath(ih.HexString())
				th.mu.Unlock()
				slog.Info("mesh assembled files into storage", "id", th.ID, "files", len(assembled))
			}
		}
	}
	th.mu.Lock()
	th.Status = "seeding"
	th.Downloaded = session.TotalLength()
	th.TotalSize = session.TotalLength()
	th.DownloadRate = 0
	th.Uploaded = session.BytesUploaded()
	th.CompletedAt = &now
	th.Files = files
	th.mu.Unlock()
	m.persistActiveTorrents()
	m.publishDownloadEvent(contracts.EventDownloadCompleted, th, "")
	slog.Info("download completed; seeding", "id", th.ID, "name", th.Name)
}

func (m *Module) seedUntilDone(ctx context.Context, th *torrentHandle, session managedTorrent) {
	deadline := time.Now().Add(time.Duration(m.seedMinutes) * time.Minute)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastUp int64
	lastTick := time.Now()
	for {
		if m.seedRatioReached(th, session) || time.Now().After(deadline) {
			th.mu.Lock()
			th.Status = "completed"
			th.UploadRate = 0
			th.mu.Unlock()
			m.persistActiveTorrents()
			slog.Info("seeding finished", "id", th.ID, "ratio_target", m.seedRatio)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			up := session.BytesUploaded()
			now := time.Now()
			elapsed := now.Sub(lastTick).Seconds()
			var upRate int32
			if elapsed > 0 {
				upRate = int32(float64(up-lastUp) / elapsed)
				if upRate < 0 {
					upRate = 0
				}
			}
			lastUp = up
			lastTick = now
			th.mu.Lock()
			th.Uploaded = up
			th.UploadRate = upRate
			th.Peers = int32(session.ActivePeers())
			th.Seeders = int32(session.ConnectedSeeders())
			th.mu.Unlock()
		}
	}
}

func (m *Module) seedRatioReached(th *torrentHandle, session managedTorrent) bool {
	total := session.TotalLength()
	if total <= 0 {
		return true
	}
	return float64(session.BytesUploaded())/float64(total) >= m.seedRatio
}

func (m *Module) failTorrent(th *torrentHandle, err error) {
	m.mu.RLock()
	stopping := m.stopping
	m.mu.RUnlock()
	if stopping {
		return
	}
	th.mu.Lock()
	th.Status = "error"
	th.ErrorStr = err.Error()
	th.mu.Unlock()
	m.persistActiveTorrents()
	m.publishDownloadEvent(contracts.EventDownloadFailed, th, err.Error())
	slog.Warn("torrent failed", "id", th.ID, "error", err)
}

func (m *Module) publishDownloadEvent(eventType string, th *torrentHandle, errStr string) {
	m.mu.RLock()
	mc := m.mc
	m.mu.RUnlock()
	if mc == nil {
		return
	}
	th.mu.RLock()
	savePath := th.SavePath
	if !strings.HasPrefix(savePath, "storage://") {
		savePath = resolveSavePath(m.dlDir, savePath)
	}
	payload := contracts.DownloadEventPayload{
		ID:       th.ID,
		Name:     th.Name,
		InfoHash: th.InfoHash,
		SavePath: savePath,
		Label:    th.Label,
		Error:    errStr,
	}
	for _, f := range th.Files {
		p := f.Path
		if !strings.HasPrefix(p, "storage://") {
			p = joinSaveAndRelPath(savePath, f.Path)
		}
		payload.Files = append(payload.Files, contracts.DownloadEventFile{Path: p, Size: f.Size})
	}
	th.mu.RUnlock()
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mc.Events.Publish(ctx, eventType, m.id, data); err != nil {
		slog.Warn("publish download event failed", "type", eventType, "error", err)
	}
}

func (m *Module) RemoveTorrent(ctx context.Context, req *downloaderv1.RemoveTorrentRequest) (*downloaderv1.RemoveTorrentResponse, error) {
	id := req.GetId()
	m.mu.Lock()
	th, ok := m.torrents[id]
	if ok {
		delete(m.torrents, id)
	}
	m.mu.Unlock()
	if !ok {
		return &downloaderv1.RemoveTorrentResponse{}, nil
	}
	if th.cancel != nil {
		th.cancel()
	}
	if th.done != nil {
		select {
		case <-th.done:
		case <-time.After(5 * time.Second):
		}
	}
	th.mu.Lock()
	session := th.session
	savePath := th.SavePath
	name := th.Name
	infoHash := th.InfoHash
	files := append([]fileInfo(nil), th.Files...)
	th.mu.Unlock()
	if session != nil {
		session.Drop()
	}
	if req.GetDeleteFiles() {
		m.deleteTorrentData(savePath, name, infoHash, files)
	}
	m.persistActiveTorrents()
	return &downloaderv1.RemoveTorrentResponse{}, nil
}

func (m *Module) deleteTorrentData(savePath, name, infoHash string, files []fileInfo) {
	if storageMode() == "mesh" && m.mesh != nil {
		h, ok := parseStorageInfoHash(savePath)
		if !ok {
			h, ok = decodeInfoHash(infoHash)
		}
		if ok {
			if err := m.mesh.DeleteTorrent(context.Background(), h); err != nil {
				slog.Warn("mesh delete torrent", "hash", infoHash, "error", err)
			}
		}
		return
	}
	for _, f := range files {
		_ = os.RemoveAll(joinSaveAndRelPath(savePath, f.Path))
	}
	if name != "" {
		_ = os.RemoveAll(filepath.Join(savePath, name))
	}
}

func (m *Module) GetTorrent(ctx context.Context, req *downloaderv1.GetTorrentRequest) (*downloaderv1.GetTorrentResponse, error) {
	m.mu.RLock()
	th, ok := m.torrents[req.GetId()]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("torrent not found: %s", req.GetId())
	}
	return &downloaderv1.GetTorrentResponse{Torrent: th.toProto()}, nil
}

func (m *Module) ListTorrents(ctx context.Context, req *downloaderv1.ListTorrentsRequest) (*downloaderv1.ListTorrentsResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	filter := req.GetFilter()
	list := make([]*downloaderv1.TorrentInfo, 0, len(m.torrents))
	for _, th := range m.torrents {
		if !torrentMatchesListFilter(th, filter, "", "") {
			continue
		}
		list = append(list, th.toProto())
	}
	if list == nil {
		list = []*downloaderv1.TorrentInfo{}
	}
	return &downloaderv1.ListTorrentsResponse{Torrents: list}, nil
}

func torrentMatchesListFilter(th *torrentHandle, legacyFilter, category, status string) bool {
	th.mu.RLock()
	defer th.mu.RUnlock()
	if category != "" && !strings.EqualFold(th.Label, category) {
		return false
	}
	stFilter := status
	if stFilter == "" && legacyFilter != "" && legacyFilter != "all" {
		stFilter = legacyFilter
	}
	if stFilter != "" && stFilter != "all" && th.Status != stFilter {
		return false
	}
	return true
}

// ── VPN operations ────────────────────────────────────────────

func (m *Module) PauseTorrent(_ context.Context, req *downloaderv1.PauseTorrentRequest) (*downloaderv1.PauseTorrentResponse, error) {
	ok, err := m.pauseTorrent(req.GetId())
	if err != nil {
		return nil, err
	}
	return &downloaderv1.PauseTorrentResponse{Success: ok}, nil
}

func (m *Module) ResumeTorrent(_ context.Context, req *downloaderv1.ResumeTorrentRequest) (*downloaderv1.ResumeTorrentResponse, error) {
	ok, err := m.resumeTorrent(req.GetId())
	if err != nil {
		return nil, err
	}
	return &downloaderv1.ResumeTorrentResponse{Success: ok}, nil
}

func (m *Module) pauseTorrent(id string) (bool, error) {
	m.mu.RLock()
	th, ok := m.torrents[id]
	m.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("torrent %q not found", id)
	}
	th.mu.Lock()
	if th.Status == "completed" || th.Status == "error" || th.Status == "removed" {
		th.mu.Unlock()
		return false, fmt.Errorf("cannot pause torrent in status %s", th.Status)
	}
	if th.session != nil {
		th.session.PauseDownload()
	}
	th.Status = "paused"
	th.mu.Unlock()
	m.persistActiveTorrents()
	return true, nil
}

func (m *Module) resumeTorrent(id string) (bool, error) {
	m.mu.RLock()
	th, ok := m.torrents[id]
	m.mu.RUnlock()
	if !ok {
		return false, fmt.Errorf("torrent %q not found", id)
	}
	th.mu.Lock()
	if th.Status != "paused" {
		th.mu.Unlock()
		return false, fmt.Errorf("torrent is not paused")
	}
	session := th.session
	th.Status = "downloading"
	resumeCh := th.resumeCh
	th.mu.Unlock()

	select {
	case resumeCh <- struct{}{}:
	default:
	}
	if session != nil {
		m.beginDownload(session)
	}
	m.persistActiveTorrents()
	return true, nil
}

func (m *Module) beginDownload(session managedTorrent) {
	m.mu.RLock()
	mode := m.filePriorityMode
	m.mu.RUnlock()
	if ap, ok := session.(filePriorityApplier); ok {
		ap.ApplyFilePriorities(mode)
		return
	}
	session.DownloadAll()
}

func (m *Module) applyFilePriorityToAllSessions(mode string) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, th := range m.torrents {
		th.mu.RLock()
		session := th.session
		th.mu.RUnlock()
		if session == nil {
			continue
		}
		if ap, ok := session.(filePriorityApplier); ok {
			ap.ApplyFilePriorities(mode)
		} else {
			session.DownloadAll()
		}
		m.syncFileWanted(th, session)
	}
}

func (m *Module) GetCapabilities(context.Context, *downloaderv1.GetCapabilitiesRequest) (*downloaderv1.GetCapabilitiesResponse, error) {
	return &downloaderv1.GetCapabilitiesResponse{
		SupportsCategories:    true,
		SupportsPausing:       true,
		SupportsFileSelection: true,
		SupportedProtocols:    []string{"torrent", "magnet"},
	}, nil
}

func (m *Module) VpnStatus(ctx context.Context, req *downloaderv1.VpnStatusRequest) (*downloaderv1.VpnStatusResponse, error) {
	connected, iface, ip, gw, cfg, connectedAt, ks := m.vpn.status()
	ts := int64(0)
	if !connectedAt.IsZero() {
		ts = connectedAt.Unix()
	}
	return &downloaderv1.VpnStatusResponse{
		Connected:         connected,
		InterfaceName:     iface,
		LocalIp:           ip,
		GatewayIp:         gw,
		ConfigFile:        cfg,
		ConnectedAtUnix:   ts,
		KillSwitchEnabled: ks,
	}, nil
}

func (m *Module) VpnStart(ctx context.Context, req *downloaderv1.VpnStartRequest) (*downloaderv1.VpnStartResponse, error) {
	configFile := req.GetConfigFile()
	if configFile == "" {
		configFile = os.Getenv("WG_CONF")
	}
	if configFile == "" {
		return nil, fmt.Errorf("config_file is required (or set WG_CONF env)")
	}

	if err := validateKillSwitch(req.GetEnableKillSwitch()); err != nil {
		return &downloaderv1.VpnStartResponse{Started: false, Error: err.Error()}, nil
	}

	if err := m.vpn.start(configFile, req.GetEnableKillSwitch()); err != nil {
		return &downloaderv1.VpnStartResponse{
			Started: false,
			Error:   err.Error(),
		}, nil
	}

	_, iface, ip, _, _, _, _ := m.vpn.status()
	fallback := int(req.GetTorrentListenPort())
	m.bindAfterVPN(configFile, iface, ip, fallback, req.GetEnableNatPmp())

	return &downloaderv1.VpnStartResponse{
		Started:       true,
		InterfaceName: iface,
		LocalIp:       ip,
	}, nil
}

func (m *Module) VpnStop(ctx context.Context, req *downloaderv1.VpnStopRequest) (*downloaderv1.VpnStopResponse, error) {
	m.natPMP.stop()
	_ = m.vpn.stop()
	return &downloaderv1.VpnStopResponse{Stopped: true}, nil
}

func (m *Module) NatPmpStatus(ctx context.Context, req *downloaderv1.NatPmpStatusRequest) (*downloaderv1.NatPmpStatusResponse, error) {
	enabled, internalPort, extPort, protocol, lifetime, mappedAt, gateway := m.natPMP.status()
	ts := int64(0)
	if !mappedAt.IsZero() {
		ts = mappedAt.Unix()
	}
	return &downloaderv1.NatPmpStatusResponse{
		Enabled:         enabled,
		InternalPort:    int32(internalPort),
		ExternalPort:    int32(extPort),
		Protocol:        protocol,
		LifetimeSeconds: int32(lifetime),
		MappedAtUnix:    ts,
		Gateway:         gateway,
	}, nil
}

func (th *torrentHandle) toProto() *downloaderv1.TorrentInfo {
	th.mu.RLock()
	defer th.mu.RUnlock()

	progress := 0.0
	if th.TotalSize > 0 {
		progress = float64(th.Downloaded) / float64(th.TotalSize) * 100
	}

	files := make([]*downloaderv1.TorrentFile, len(th.Files))
	for i, f := range th.Files {
		files[i] = &downloaderv1.TorrentFile{Path: f.Path, Size: f.Size, Downloaded: f.Downloaded}
	}

	info := &downloaderv1.TorrentInfo{
		Id:           th.ID,
		Name:         th.Name,
		InfoHash:     th.InfoHash,
		TotalSize:    th.TotalSize,
		Downloaded:   th.Downloaded,
		Progress:     progress,
		Peers:        th.Peers,
		Seeders:      th.Seeders,
		UploadRate:   th.UploadRate,
		DownloadRate: th.DownloadRate,
		Status:       th.Status,
		Error:        th.ErrorStr,
		SavePath:     th.SavePath,
		Label:        th.Label,
		AddedAt:      th.AddedAt.Format(time.RFC3339),
		Files:        files,
	}
	_ = th.Uploaded // contracts layer reads Uploaded from handle via mapTorrentInfoFromHandle
	if th.CompletedAt != nil {
		info.CompletedAt = th.CompletedAt.Format(time.RFC3339)
	}
	return info
}

func parseMagnetName(uri string) string {
	for _, part := range split(uri, "&") {
		if hasPrefix(part, "dn=") {
			return urlDecode(part[3:])
		}
		// magnet display name may appear after ?
		if i := indexOf(part, "dn=", 0); i >= 0 && hasPrefix(part[i:], "dn=") {
			return urlDecode(part[i+3:])
		}
	}
	// also check first segment after ?
	if i := indexOf(uri, "dn=", 0); i >= 0 {
		rest := uri[i+3:]
		end := indexOf(rest, "&", 0)
		if end >= 0 {
			return urlDecode(rest[:end])
		}
		return urlDecode(rest)
	}
	return "unknown"
}

func parseInfoHash(uri string) string {
	start := indexOf(uri, "xt=urn:btih:", 0)
	if start >= 0 {
		hash := uri[start+12:]
		end := indexOf(hash, "&", 0)
		if end >= 0 {
			return hash[:end]
		}
		return hash
	}
	if len(uri) == 40 {
		return uri
	}
	return ""
}

func split(s, sep string) []string {
	var parts []string
	for i := 0; i < len(s); {
		j := indexOf(s, sep, i)
		if j < 0 {
			parts = append(parts, s[i:])
			break
		}
		parts = append(parts, s[i:j])
		i = j + len(sep)
	}
	return parts
}

func indexOf(s, sub string, start int) int {
	if start >= len(s) {
		return -1
	}
	for i := start; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func urlDecode(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '+' {
			out = append(out, ' ')
		} else if s[i] == '%' && i+2 < len(s) {
			hi := hexVal(s[i+1])
			lo := hexVal(s[i+2])
			if hi >= 0 && lo >= 0 {
				out = append(out, byte(hi<<4|lo))
				i += 2
			} else {
				out = append(out, s[i])
			}
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

func hexVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c - 'a' + 10)
	case c >= 'A' && c <= 'F':
		return int(c - 'A' + 10)
	default:
		return -1
	}
}

var _ contracts.Module = (*Module)(nil)
