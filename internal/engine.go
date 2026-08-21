package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Muxcore-Media/downloader-native-torrent/internal/meshstore"
	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

const maxTorrentFileBytes = 4 << 20 // 4 MiB

// torrentEngine adds torrents from URIs. Injected in tests; production uses anacrolix.
type torrentEngine interface {
	AddURI(ctx context.Context, uri, savePath string) (managedTorrent, error)
	Close() error
}

// managedTorrent is one active torrent session.
type managedTorrent interface {
	WaitInfo(ctx context.Context) error
	Name() string
	InfoHash() string
	TotalLength() int64
	Files() []fileInfo
	DownloadAll()
	PauseDownload()
	BytesCompleted() int64
	BytesMissing() int64
	BytesUploaded() int64
	ActivePeers() int
	ConnectedSeeders() int
	Drop()
}

type anacrolixEngine struct {
	client     *torrent.Client
	storage    storage.ClientImplCloser
	http       *http.Client
	dataDir    string
	listenPort int
	listenHost string
	enableDHT  bool
	enablePEX  bool
	meshMode   bool // when true, never attach per-torrent NewFile storage
}

type anacrolixEngineOpts struct {
	ListenHost  string
	EnableDHT   bool
	EnablePEX   bool
	MeshStorage storage.ClientImplCloser // when set, no local DOWNLOAD_DIR piece store
}

func newAnacrolixEngine(dataDir string, listenPort int, listenHost string, hc *http.Client) (*anacrolixEngine, error) {
	return newAnacrolixEngineOpts(dataDir, listenPort, hc, anacrolixEngineOpts{
		ListenHost: listenHost,
		EnableDHT:  true,
		EnablePEX:  true,
	})
}

func newAnacrolixEngineOpts(dataDir string, listenPort int, hc *http.Client, opts anacrolixEngineOpts) (*anacrolixEngine, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	e := &anacrolixEngine{
		http: hc, dataDir: dataDir, listenPort: listenPort, listenHost: opts.ListenHost,
		enableDHT: opts.EnableDHT, enablePEX: opts.EnablePEX,
	}
	if opts.MeshStorage != nil {
		e.storage = opts.MeshStorage
		e.meshMode = true
	} else {
		pc, err := openPieceCompletionRetry(dataDir)
		if err != nil {
			return nil, fmt.Errorf("piece completion db: %w", err)
		}
		e.storage = storage.NewFileOpts(storage.NewFileClientOpts{
			ClientBaseDir:   dataDir,
			PieceCompletion: pc,
		})
	}
	cl, err := e.newClient(opts.ListenHost, listenPort)
	if err != nil {
		_ = e.storage.Close()
		return nil, fmt.Errorf("torrent client: %w", err)
	}
	e.client = cl
	return e, nil
}

func applyListenConfig(cfg *torrent.ClientConfig, host string, port int) {
	cfg.ListenPort = port
	cfg.NoDefaultPortForwarding = true
	if host == "" {
		return
	}
	h := host
	ipv4Only := isIPv4Host(h)
	if ipv4Only {
		cfg.DisableIPv6 = true
	}
	cfg.ListenHost = func(network string) string {
		if ipv4Only && strings.Contains(network, "6") {
			return ""
		}
		return h
	}
}

func isIPv4Host(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.To4() != nil
}

func (e *anacrolixEngine) newClient(host string, port int) (*torrent.Client, error) {
	cfg := torrent.NewDefaultClientConfig()
	if e.dataDir != "" {
		cfg.DataDir = e.dataDir
	}
	cfg.Seed = true
	cfg.NoDHT = !e.enableDHT
	cfg.DisablePEX = !e.enablePEX
	cfg.DefaultStorage = e.storage
	applyListenConfig(cfg, host, port)
	return torrent.NewClient(cfg)
}

func openPieceCompletionRetry(dir string) (storage.PieceCompletion, error) {
	const attempts = 10
	wait := 200 * time.Millisecond
	var last error
	for i := 0; i < attempts; i++ {
		pc, err := storage.NewDefaultPieceCompletionForDir(dir)
		if err == nil {
			if i > 0 {
				slog.Info("opened piece completion db after retry", "dir", dir, "attempt", i+1)
			}
			return pc, nil
		}
		last = err
		slog.Warn("piece completion db busy; retrying", "dir", dir, "attempt", i+1, "error", err)
		time.Sleep(wait)
	}
	return nil, last
}

// rebind replaces the torrent client to listen on host:port. The previous client is
// kept until the new one starts successfully. listenPort 0 keeps the current port.
func (e *anacrolixEngine) rebind(listenHost string, listenPort int) error {
	if e == nil {
		return fmt.Errorf("engine nil")
	}
	port := e.listenPort
	if listenPort > 0 {
		port = listenPort
	}
	if listenHost == e.listenHost && port == e.listenPort && e.client != nil {
		return nil
	}
	cl, err := e.newClient(listenHost, port)
	if err != nil {
		return fmt.Errorf("torrent client rebind: %w", err)
	}
	old := e.client
	e.client = cl
	e.listenHost = listenHost
	e.listenPort = port
	if old != nil {
		old.Close()
	}
	return nil
}

func (e *anacrolixEngine) Close() error {
	if e.client != nil {
		e.client.Close()
		e.client = nil
	}
	if e.storage != nil {
		_ = e.storage.Close()
		e.storage = nil
	}
	return nil
}

func (e *anacrolixEngine) AddURI(ctx context.Context, uri, savePath string) (managedTorrent, error) {
	kind, err := classifyURI(uri)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "magnet":
		return e.addMagnetPreferMetainfo(ctx, uri, savePath)
	case "http":
		data, magnet, err := fetchTorrentFile(ctx, e.http, uri)
		if err != nil {
			return nil, err
		}
		if magnet != "" {
			// Prowlarr often 301s to magnet:; DHT metadata is unreliable behind VPN.
			return e.addMagnetPreferMetainfo(ctx, magnet, savePath)
		}
		return e.addTorrentBytes(data, savePath)
	default:
		return nil, fmt.Errorf("unsupported uri scheme")
	}
}

// addMagnetPreferMetainfo loads a .torrent from a public cache when possible so
// GotInfo is immediate instead of waiting on DHT/PEX for magnet metadata.
func (e *anacrolixEngine) addMagnetPreferMetainfo(ctx context.Context, magnet, savePath string) (managedTorrent, error) {
	if hash := strings.ToLower(parseInfoHash(magnet)); hash != "" {
		if meta, err := fetchTorrentMetainfo(ctx, hash); err == nil {
			slog.Info("resolved magnet via torrent cache", "infohash", hash, "bytes", len(meta))
			return e.addTorrentBytes(meta, savePath)
		} else {
			slog.Debug("torrent cache miss; falling back to magnet", "infohash", hash, "error", err)
		}
	}
	return e.addMagnet(magnet, savePath)
}

func (e *anacrolixEngine) addMagnet(uri, savePath string) (managedTorrent, error) {
	spec, err := torrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		return nil, fmt.Errorf("parse magnet: %w", err)
	}
	if !e.meshMode {
		resolved := resolveSavePath(e.dataDir, savePath)
		if resolved != "" && resolved != e.dataDir {
			spec.Storage = storage.NewFile(resolved)
		}
	}
	t, _, err := e.client.AddTorrentSpec(spec)
	if err != nil {
		return nil, err
	}
	return &anacrolixTorrent{t: t}, nil
}

func (e *anacrolixEngine) addTorrentBytes(data []byte, savePath string) (managedTorrent, error) {
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse torrent: %w", err)
	}
	if e.meshMode {
		if ms, ok := e.storage.(*meshstore.Client); ok {
			ih := mi.HashInfoBytes()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			_ = ms.PutMeta(ctx, ih, data)
			cancel()
		}
	}
	spec := torrent.TorrentSpecFromMetaInfo(mi)
	if !e.meshMode {
		resolved := resolveSavePath(e.dataDir, savePath)
		if resolved != "" && resolved != e.dataDir {
			spec.Storage = storage.NewFile(resolved)
		}
	}
	t, _, err := e.client.AddTorrentSpec(spec)
	if err != nil {
		return nil, err
	}
	return &anacrolixTorrent{t: t}, nil
}

type anacrolixTorrent struct {
	t *torrent.Torrent
}

func (a *anacrolixTorrent) WaitInfo(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.t.GotInfo():
		return nil
	}
}

func (a *anacrolixTorrent) Name() string { return a.t.Name() }

func (a *anacrolixTorrent) InfoHash() string { return a.t.InfoHash().HexString() }

func (a *anacrolixTorrent) TotalLength() int64 {
	info := a.t.Info()
	if info == nil {
		return 0
	}
	return info.TotalLength()
}

func (a *anacrolixTorrent) Files() []fileInfo {
	info := a.t.Info()
	if info == nil {
		return nil
	}
	out := make([]fileInfo, 0, len(info.UpvertedFiles()))
	for _, f := range info.UpvertedFiles() {
		path := strings.Join(f.Path, "/")
		if info.Name != "" && path != "" {
			path = info.Name + "/" + path
		} else if path == "" {
			path = info.Name
		}
		out = append(out, fileInfo{Path: path, Size: f.Length})
	}
	return out
}

func (a *anacrolixTorrent) PieceLayout() (pieceLayout, bool) {
	info := a.t.Info()
	if info == nil || !info.HasV1() || info.PieceLength <= 0 || len(info.Pieces) < 20 {
		return pieceLayout{}, false
	}
	n := len(info.Pieces) / 20
	hashes := make([][]byte, n)
	for i := 0; i < n; i++ {
		h := make([]byte, 20)
		copy(h, info.Pieces[i*20:(i+1)*20])
		hashes[i] = h
	}
	return pieceLayout{
		PieceLength: info.PieceLength,
		PieceHashes: hashes,
		Files:       a.Files(),
		TotalLength: info.TotalLength(),
	}, true
}

func (a *anacrolixTorrent) DownloadAll() { a.t.DownloadAll() }

// ApplyFilePriorities selects which files to download based on mode (see filepriority.go).
func (a *anacrolixTorrent) ApplyFilePriorities(mode string) {
	files := a.t.Files()
	if len(files) == 0 {
		a.t.DownloadAll()
		return
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path()
	}
	want := selectFilesToDownload(paths, mode)
	any := false
	for _, w := range want {
		if w {
			any = true
			break
		}
	}
	if !any || normalizeFilePriorityMode(mode) == filePriorityAll {
		a.t.DownloadAll()
		return
	}
	for i, f := range files {
		if want[i] {
			f.SetPriority(torrent.PiecePriorityNormal)
		} else {
			f.SetPriority(torrent.PiecePriorityNone)
		}
	}
}

func (a *anacrolixTorrent) PauseDownload() {
	for _, f := range a.t.Files() {
		f.SetPriority(torrent.PiecePriorityNone)
	}
}

func (a *anacrolixTorrent) BytesCompleted() int64 { return a.t.BytesCompleted() }

func (a *anacrolixTorrent) BytesMissing() int64 { return a.t.BytesMissing() }

func (a *anacrolixTorrent) BytesUploaded() int64 {
	stats := a.t.Stats()
	return stats.BytesWrittenData.Int64()
}

func (a *anacrolixTorrent) ActivePeers() int {
	return a.t.Stats().ActivePeers
}
func (a *anacrolixTorrent) ConnectedSeeders() int {
	return a.t.Stats().ConnectedSeeders
}
func (a *anacrolixTorrent) Drop()                 { a.t.Drop() }

func classifyURI(uri string) (string, error) {
	u := strings.TrimSpace(uri)
	lower := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lower, "magnet:"):
		return "magnet", nil
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		return "http", nil
	default:
		return "", fmt.Errorf("unsupported uri scheme (want magnet or http(s) .torrent)")
	}
}

func isMagnetURI(s string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "magnet:")
}

func httpClientStopOnMagnet(hc *http.Client) *http.Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	c := *hc
	prev := hc.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL != nil && strings.EqualFold(req.URL.Scheme, "magnet") {
			return http.ErrUseLastResponse
		}
		if prev != nil {
			return prev(req, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &c
}

func fetchTorrentFile(ctx context.Context, hc *http.Client, uri string) ([]byte, string, error) {
	hc = httpClientStopOnMagnet(hc)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("fetch torrent: %w", err)
	}
	defer resp.Body.Close()
	if loc := resp.Header.Get("Location"); isMagnetURI(loc) {
		return nil, strings.TrimSpace(loc), nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("fetch torrent: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentFileBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxTorrentFileBytes {
		return nil, "", fmt.Errorf("torrent file exceeds %d bytes", maxTorrentFileBytes)
	}
	if isMagnetURI(string(data)) {
		return nil, strings.TrimSpace(string(data)), nil
	}
	return data, "", nil
}
