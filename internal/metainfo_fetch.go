package internal

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Muxcore-Media/downloader-native-torrent/internal/meshstore"
	"github.com/anacrolix/torrent/metainfo"
)

// meshMetaReader loads .torrent bytes from mesh storage when available.
type meshMetaReader interface {
	GetMeta(ctx context.Context, ih metainfo.Hash) ([]byte, error)
	PutMeta(ctx context.Context, ih metainfo.Hash, data []byte) error
}

func publicMetainfoCacheEnabled() bool {
	if envFlagTruthy(os.Getenv("DOWNLOADER_PUBLIC_METAINFO_CACHE")) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DOWNLOADER_ENGINE"))) {
	case "fixture", "fake":
		return false
	default:
		return true
	}
}

func defaultMetainfoHTTPClient() *http.Client {
	return &http.Client{Timeout: 45 * time.Second}
}

func fetchTorrentMetainfoWith(ctx context.Context, ihHex string, hc *http.Client, mesh meshMetaReader) ([]byte, error) {
	ihHex = strings.ToLower(strings.TrimSpace(ihHex))
	if len(ihHex) != 40 {
		return nil, fmt.Errorf("invalid infohash")
	}
	if usableMeshMeta(mesh) {
		if ih, ok := decodeInfoHash(ihHex); ok {
			if meta, err := mesh.GetMeta(ctx, ih); err == nil && len(meta) > 0 {
				if err := validateMetainfoBytes(meta, ihHex); err == nil {
					return meta, nil
				}
			}
		}
	}
	if !publicMetainfoCacheEnabled() {
		return nil, fmt.Errorf("public metainfo cache disabled (set DOWNLOADER_PUBLIC_METAINFO_CACHE=true to opt in)")
	}
	if hc == nil {
		hc = defaultMetainfoHTTPClient()
	}
	meta, err := fetchPublicMetainfoCacheHTTP(ctx, ihHex, hc)
	if err != nil {
		return nil, err
	}
	if usableMeshMeta(mesh) {
		if ih, ok := decodeInfoHash(ihHex); ok {
			_ = mesh.PutMeta(ctx, ih, meta)
		}
	}
	return meta, nil
}

func usableMeshMeta(mesh meshMetaReader) bool {
	if mesh == nil {
		return false
	}
	if ms, ok := mesh.(*meshstore.Client); ok {
		return ms != nil
	}
	return true
}

func validateMetainfoBytes(data []byte, ihHex string) error {
	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil {
		return err
	}
	if got := strings.ToLower(mi.HashInfoBytes().HexString()); got != ihHex {
		return fmt.Errorf("infohash mismatch (want %s got %s)", ihHex, got)
	}
	return nil
}

func fetchPublicMetainfoCacheHTTP(ctx context.Context, ihHex string, hc *http.Client) ([]byte, error) {
	upper := strings.ToUpper(ihHex)
	urls := []string{
		"https://itorrents.net/torrent/" + upper + ".torrent",
		"https://itorrents.org/torrent/" + upper + ".torrent",
		"https://itorrents.org/torrent/" + ihHex + ".torrent",
	}
	var last error
	for _, u := range urls {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			last = err
			continue
		}
		req.Header.Set("User-Agent", "MuxCore-downloader/1.0")
		resp, err := hc.Do(req)
		if err != nil {
			last = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			last = readErr
			continue
		}
		if resp.StatusCode != http.StatusOK {
			last = fmt.Errorf("%s: status %d", u, resp.StatusCode)
			continue
		}
		if len(data) < 16 || data[0] != 'd' {
			last = fmt.Errorf("%s: not a torrent file", u)
			continue
		}
		if err := validateMetainfoBytes(data, ihHex); err != nil {
			last = fmt.Errorf("%s: %w", u, err)
			continue
		}
		return data, nil
	}
	if last == nil {
		last = fmt.Errorf("no torrent URL candidates")
	}
	return nil, last
}

var _ meshMetaReader = (*meshstore.Client)(nil)
