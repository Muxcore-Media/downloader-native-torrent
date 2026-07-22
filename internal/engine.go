package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
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
	BytesCompleted() int64
	BytesMissing() int64
	BytesUploaded() int64
	ActivePeers() int
	ConnectedSeeders() int
	Drop()
}

type anacrolixEngine struct {
	client  *torrent.Client
	http    *http.Client
	dataDir string
}

func newAnacrolixEngine(dataDir string, listenPort int, hc *http.Client) (*anacrolixEngine, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
	cfg.ListenPort = listenPort
	cfg.NoDefaultPortForwarding = true
	cfg.Seed = true
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("torrent client: %w", err)
	}
	return &anacrolixEngine{client: cl, http: hc, dataDir: dataDir}, nil
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
