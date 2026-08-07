package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	"github.com/Muxcore-Media/core/pkg/contracts"
	"github.com/Muxcore-Media/core/sdk/go/client"
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

	id           string
	grpcAddr     string
	dlDir        string
	listenPort   int
	seedRatio    float64
	seedMinutes  int
	wgConfPath   string
	wgKillSwitch bool
	grpcSrv      *grpc.Server
	lis          net.Listener
	infoTimeout  time.Duration
}

type torrentHandle struct {
	ID           string
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
}

type Config struct {
	ID           string
	GRPCAddr     string
	DownloadDir  string
	WGConfPath   string
	WGKillSwitch bool
	NatPMPPort   int
	ListenPort   int
	SeedRatio    float64
	SeedMinutes  int
	Engine       torrentEngine // optional; tests inject a fake
	InfoTimeout  time.Duration
}

func NewModule(cfg Config) *Module {
	if cfg.ID == "" {
		cfg.ID = "downloader-native-torrent"
	}
	if cfg.GRPCAddr == "" {
		cfg.GRPCAddr = ":9460"
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
		fmt.Sscanf(v, "%d", &cfg.NatPMPPort)
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

	return &Module{
		id:           cfg.ID,
		grpcAddr:     cfg.GRPCAddr,
		dlDir:        cfg.DownloadDir,
		listenPort:   cfg.ListenPort,
		seedRatio:    cfg.SeedRatio,
		seedMinutes:  cfg.SeedMinutes,
		infoTimeout:  cfg.InfoTimeout,
		wgConfPath:   cfg.WGConfPath,
		wgKillSwitch: cfg.WGKillSwitch,
		engine:       cfg.Engine,
		torrents:     make(map[string]*torrentHandle),
		vpn:          newVPNManager(),
		natPMP:       newNATPMPClient(),
	}
}

func (m *Module) Info() contracts.ModuleInfo {
	return contracts.ModuleInfo{
		ID:           m.id,
		Name:         "Downloader Native Torrent",
		Version:      "0.2.0",
		Roles:        []string{"downloader"},
		Description:  "Native torrent download engine (anacrolix) with WireGuard VPN and NAT-PMP support",
		Author:       "MuxCore",
		Capabilities: []string{"downloader", "downloader.native.torrent", "settings"},
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
	if err := os.MkdirAll(m.dlDir, 0755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}
	if m.engine == nil {
		eng, err := newAnacrolixEngine(m.dlDir, m.listenPort, "", nil)
		if err != nil {
			return err
		}
		m.engine = eng
	}
	lis, err := net.Listen("tcp", m.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", m.grpcAddr, err)
	}
	m.lis = lis
	slog.Info("downloader-native-torrent initialized",
		"addr", m.grpcAddr, "dir", m.dlDir, "listen_port", m.listenPort)

	go m.autoStartVPN()
	return nil
}

func (m *Module) autoStartVPN() {
	cfgPath := m.wgConfPath
	if cfgPath == "" {
		cfgPath = os.Getenv("WG_CONF")
	}
	if cfgPath == "" {
		return
	}

	ks := m.wgKillSwitch || os.Getenv("WG_KILL_SWITCH") == "true"
	slog.Info("auto-starting wireguard", "config", cfgPath)

	if err := m.vpn.start(cfgPath, ks); err != nil {
		slog.Warn("auto-start wireguard failed", "error", err)
		return
	}

	if ks {
		slog.Info("kill switch enabled - all non-VPN traffic blocked")
	}

	_, iface, localIP, _, _, _, _ := m.vpn.status()
	if localIP != "" {
		if eng, ok := m.engine.(*anacrolixEngine); ok {
			if err := eng.rebind(localIP); err != nil {
				slog.Warn("vpn: bind torrent client to wireguard ip failed", "ip", localIP, "error", err)
			} else {
				slog.Info("torrent client bound to wireguard", "interface", iface, "ip", localIP)
			}
		}
	}

	portStr := os.Getenv("NAT_PMP_PORT")
	cfgData, _ := os.ReadFile(cfgPath)
	if portStr != "" || hasNATPMPEnabled(string(cfgData)) {
		var port int
		if portStr != "" {
			fmt.Sscanf(portStr, "%d", &port)
		} else {
			port = 6881
		}
		if port > 0 {
			_, _, _, gw, _, _, _ := m.vpn.status()
			if gw == "" {
				gw = "10.2.0.1"
			}
			if err := m.natPMP.start(gw, port, "tcp"); err != nil {
				slog.Warn("auto-start nat-pmp failed", "error", err)
			} else {
				slog.Info("nat-pmp port mapping active", "internal_port", port)
			}
		}
	}
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
	go m.dialCore(context.Background())
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
	m.mu.Lock()
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
	m.vpn.stop()
	if m.mc != nil {
		m.mc.Close()
	}
	if m.grpcSrv != nil {
		m.grpcSrv.GracefulStop()
	}
	slog.Info("downloader-native-torrent stopped")
	return nil
}

func (m *Module) Health(ctx context.Context) error {
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
	savePath := req.GetSavePath()
	if savePath == "" {
		savePath = m.dlDir
	}
	if err := os.MkdirAll(savePath, 0755); err != nil {
		return nil, fmt.Errorf("create save path: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	th := &torrentHandle{
		ID:       fmt.Sprintf("torrent_%d", time.Now().UnixNano()),
		Name:     parseMagnetName(uri),
		InfoHash: parseInfoHash(uri),
		Label:    req.GetLabel(),
		SavePath: savePath,
		AddedAt:  time.Now(),
		Status:   "queued",
		Files:    []fileInfo{},
		cancel:   cancel,
		done:     make(chan struct{}),
		resumeCh: make(chan struct{}, 1),
	}

	m.mu.Lock()
	m.torrents[th.ID] = th
	m.mu.Unlock()

	go m.runTorrent(runCtx, th, uri, req.GetPaused())

	th.mu.RLock()
	name, infoHash := th.Name, th.InfoHash
	th.mu.RUnlock()
	slog.Info("torrent added", "id", th.ID, "name", name)
	return &downloaderv1.AddTorrentResponse{
		Id: th.ID, Name: name, InfoHash: infoHash,
	}, nil
}

func (m *Module) runTorrent(ctx context.Context, th *torrentHandle, uri string, paused bool) {
	defer close(th.done)

	session, err := m.engine.AddURI(ctx, uri, th.SavePath)
	if err != nil {
		m.failTorrent(th, err)
		return
	}
	th.mu.Lock()
	th.session = session
	th.mu.Unlock()

	infoCtx, infoCancel := context.WithTimeout(ctx, m.infoTimeout)
	defer infoCancel()
	if err := session.WaitInfo(infoCtx); err != nil {
		session.Drop()
		m.failTorrent(th, fmt.Errorf("wait metadata: %w", err))
		return
	}
	m.syncHandleFromTorrent(th, session)

	if paused {
		th.mu.Lock()
		th.Status = "paused"
		th.mu.Unlock()
		slog.Info("torrent paused after metadata", "id", th.ID)
		select {
		case <-ctx.Done():
			session.Drop()
			return
		case <-th.resumeCh:
		}
	}

	session.DownloadAll()
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
}

func (m *Module) markCompleted(th *torrentHandle, session managedTorrent) {
	now := time.Now()
	files := session.Files()
	for i := range files {
		files[i].Downloaded = files[i].Size
	}
	th.mu.Lock()
	th.Status = "completed"
	th.Downloaded = session.TotalLength()
	th.TotalSize = session.TotalLength()
	th.DownloadRate = 0
	th.CompletedAt = &now
	th.Files = files
	th.mu.Unlock()
	m.publishDownloadEvent(contracts.EventDownloadCompleted, th, "")
	slog.Info("download completed", "id", th.ID, "name", th.Name)
}

func (m *Module) seedUntilDone(ctx context.Context, th *torrentHandle, session managedTorrent) {
	deadline := time.Now().Add(time.Duration(m.seedMinutes) * time.Minute)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if m.seedRatioReached(th, session) || time.Now().After(deadline) {
			slog.Info("seeding finished", "id", th.ID, "ratio_target", m.seedRatio)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			up := session.BytesUploaded()
			th.mu.Lock()
			th.UploadRate = 0
			th.Peers = int32(session.ActivePeers())
			th.Seeders = int32(session.ConnectedSeeders())
			_ = up
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
	th.mu.Lock()
	th.Status = "error"
	th.ErrorStr = err.Error()
	th.mu.Unlock()
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
	payload := contracts.DownloadEventPayload{
		ID:       th.ID,
		Name:     th.Name,
		InfoHash: th.InfoHash,
		SavePath: th.SavePath,
		Label:    th.Label,
		Error:    errStr,
	}
	for _, f := range th.Files {
		payload.Files = append(payload.Files, contracts.DownloadEventFile{Path: f.Path, Size: f.Size})
	}
	th.mu.RUnlock()
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mc.Events.Publish(ctx, eventType, m.id, data); err != nil {
		slog.Debug("publish download event failed", "type", eventType, "error", err)
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
	files := append([]fileInfo(nil), th.Files...)
	th.mu.Unlock()
	if session != nil {
		session.Drop()
	}
	if req.GetDeleteFiles() {
		m.deleteTorrentData(savePath, name, files)
	}
	return &downloaderv1.RemoveTorrentResponse{}, nil
}

func (m *Module) deleteTorrentData(savePath, name string, files []fileInfo) {
	for _, f := range files {
		p := f.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(savePath, p)
		}
		_ = os.RemoveAll(p)
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
		th.mu.RLock()
		s := th.Status
		th.mu.RUnlock()
		if filter != "" && filter != "all" && s != filter {
			continue
		}
		list = append(list, th.toProto())
	}
	if list == nil {
		list = []*downloaderv1.TorrentInfo{}
	}
	return &downloaderv1.ListTorrentsResponse{Torrents: list}, nil
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

func (m *Module) GetCapabilities(context.Context, *downloaderv1.GetCapabilitiesRequest) (*downloaderv1.GetCapabilitiesResponse, error) {
	return &downloaderv1.GetCapabilitiesResponse{
		SupportsCategories:    true,
		SupportsPausing:       true,
		SupportsFileSelection: false,
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

	if err := m.vpn.start(configFile, req.GetEnableKillSwitch()); err != nil {
		return &downloaderv1.VpnStartResponse{
			Started: false,
			Error:   err.Error(),
		}, nil
	}

	_, _, ip, _, _, _, _ := m.vpn.status()
	_, iface, _, _, _, _, _ := m.vpn.status()

	if req.GetEnableNatPmp() && req.GetTorrentListenPort() > 0 {
		cfgData, err := os.ReadFile(configFile)
		gw := ""
		if err == nil {
			gw = parseConfig(string(cfgData)).Interface.DNS[0]
		}
		if gw == "" {
			gw = "10.2.0.1"
		}
		if err := m.natPMP.start(gw, int(req.GetTorrentListenPort()), "tcp"); err != nil {
			slog.Warn("vpn: nat-pmp start failed", "error", err)
		}
	}

	return &downloaderv1.VpnStartResponse{
		Started:       true,
		InterfaceName: iface,
		LocalIp:       ip,
	}, nil
}

func (m *Module) VpnStop(ctx context.Context, req *downloaderv1.VpnStopRequest) (*downloaderv1.VpnStopResponse, error) {
	m.natPMP.stop()
	m.vpn.stop()
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
