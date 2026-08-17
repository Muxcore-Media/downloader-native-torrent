package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

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
	http       *http.Client
	dataDir    string
	listenPort int
	listenHost string
	enableDHT  bool
	enablePEX  bool
}

type anacrolixEngineOpts struct {
	ListenHost string
	EnableDHT  bool
	EnablePEX  bool
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
	cl, err := e.newClient(opts.ListenHost, listenPort)
	if err != nil {
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
	cfg.DataDir = e.dataDir
	cfg.Seed = true
	cfg.NoDHT = !e.enableDHT
	cfg.DisablePEX = !e.enablePEX
	applyListenConfig(cfg, host, port)
	return torrent.NewClient(cfg)
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
		return e.addMagnet(uri, savePath)
	case "http":
		data, err := fetchTorrentFile(ctx, e.http, uri)
		if err != nil {
			return nil, err
		}
		return e.addTorrentBytes(data, savePath)
	default:
		return nil, fmt.Errorf("unsupported uri scheme")
	}
}

func (e *anacrolixEngine) addMagnet(uri, savePath string) (managedTorrent, error) {
	spec, err := torrent.TorrentSpecFromMagnetUri(uri)
	if err != nil {
		return nil, fmt.Errorf("parse magnet: %w", err)
	}
	if savePath != "" && savePath != e.dataDir {
		spec.Storage = storage.NewFile(savePath)
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
	spec := torrent.TorrentSpecFromMetaInfo(mi)
	if savePath != "" && savePath != e.dataDir {
		spec.Storage = storage.NewFile(savePath)
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

func fetchTorrentFile(ctx context.Context, hc *http.Client, uri string) ([]byte, error) {
	if hc == nil {
		hc = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch torrent: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch torrent: status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxTorrentFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTorrentFileBytes {
		return nil, fmt.Errorf("torrent file exceeds %d bytes", maxTorrentFileBytes)
	}
	return data, nil
}
