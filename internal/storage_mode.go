package internal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/Muxcore-Media/core/sdk/go/client"
	"github.com/Muxcore-Media/downloader-native-torrent/internal/meshstore"
	"github.com/anacrolix/torrent/metainfo"
)

// storageMode returns "mesh", "local". Empty/auto picks mesh when
// MUXCORE_GRPC_ADDR is set (production host stack), else local (unit tests / offline).
func storageMode() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DOWNLOAD_STORAGE")))
	switch v {
	case "local", "file", "disk":
		return "local"
	case "mesh", "storage", "grpc":
		return "mesh"
	case "", "auto":
		if strings.TrimSpace(os.Getenv("MUXCORE_GRPC_ADDR")) != "" {
			return "mesh"
		}
		return "local"
	default:
		return v
	}
}

func meshBackendFromClient(c *client.Client) meshstore.Backend {
	st := c.Storage
	return meshstore.Adapter{
		PutFn: func(ctx context.Context, key string, data []byte) error {
			return st.PutBytes(ctx, key, data)
		},
		PutReaderFn: func(ctx context.Context, key string, r io.Reader) error {
			return st.Put(ctx, key, r)
		},
		GetFn: func(ctx context.Context, key string, offset, length int64) ([]byte, error) {
			return st.GetBytes(ctx, key, offset, length)
		},
		DeleteFn: func(ctx context.Context, key string) error {
			return st.Delete(ctx, key)
		},
		StatFn: func(ctx context.Context, key string) (bool, int64, error) {
			resp, err := st.Stat(ctx, key)
			if err != nil {
				return false, 0, err
			}
			if resp == nil {
				return false, 0, nil
			}
			return true, resp.GetSize(), nil
		},
		ListFn: func(ctx context.Context, prefix string) ([]string, error) {
			objs, err := st.List(ctx, prefix)
			if err != nil {
				return nil, err
			}
			out := make([]string, 0, len(objs))
			for _, o := range objs {
				if o != nil && o.GetKey() != "" {
					out = append(out, o.GetKey())
				}
			}
			return out, nil
		},
	}
}

func newMeshStorage(c *client.Client) *meshstore.Client {
	return meshstore.New(meshBackendFromClient(c))
}

// storageSavePath returns the logical save path for events / automation.
// Mesh mode uses a storage:// URI so ImportPath callers can detect non-local paths.
func storageSavePath(infoHash string) string {
	h := strings.ToLower(strings.TrimSpace(infoHash))
	return fmt.Sprintf("storage://torrent/%s", h)
}

// meshPendingDest maps storage://torrent/pending → storage://torrent/{ih} once
// metadata is known. HTTP indexer links have no infohash until fetch/redirect.
func meshPendingDest(savePath, infoHash string) (string, bool) {
	infoHash = strings.ToLower(strings.TrimSpace(infoHash))
	if infoHash == "" || len(infoHash) != 40 {
		return "", false
	}
	cur := strings.TrimSpace(savePath)
	if cur != "storage://torrent/pending" && !strings.HasSuffix(cur, "/pending") {
		return "", false
	}
	dest := storageSavePath(infoHash)
	if dest == cur {
		return "", false
	}
	return dest, true
}

// magnetFromInfoHash builds a minimal magnet for session restore when the
// original indexer HTTP link is gone.
func magnetFromInfoHash(infoHash, name string) string {
	infoHash = strings.ToLower(strings.TrimSpace(infoHash))
	if len(infoHash) != 40 {
		return ""
	}
	mag := "magnet:?xt=urn:btih:" + infoHash
	name = strings.TrimSpace(name)
	if name != "" && !strings.EqualFold(name, "unknown") {
		mag += "&dn=" + url.QueryEscape(name)
	}
	return mag
}

func magnetFallbackURI(uri, savePath string) string {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(uri)), "http") {
		return ""
	}
	if h, ok := parseStorageInfoHash(savePath); ok {
		return magnetFromInfoHash(h.HexString(), "")
	}
	return ""
}

// canonicalizePersistedURI replaces ephemeral indexer HTTP links with a magnet
// once the infohash is known so restarts do not depend on Prowlarr download URLs.
func (m *Module) canonicalizePersistedURI(th *torrentHandle) bool {
	th.mu.Lock()
	defer th.mu.Unlock()
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(th.URI)), "http") {
		return false
	}
	mag := magnetFromInfoHash(th.InfoHash, th.Name)
	if mag == "" {
		return false
	}
	th.URI = mag
	return true
}

func parseStorageInfoHash(savePath string) (metainfo.Hash, bool) {
	const pref = "storage://torrent/"
	if !strings.HasPrefix(savePath, pref) {
		return metainfo.Hash{}, false
	}
	hex := strings.TrimPrefix(savePath, pref)
	hex = strings.Split(hex, "/")[0]
	return decodeInfoHash(hex)
}

func decodeInfoHash(hex string) (metainfo.Hash, bool) {
	hex = strings.ToLower(strings.TrimSpace(hex))
	if len(hex) != 40 {
		return metainfo.Hash{}, false
	}
	var h metainfo.Hash
	for i := 0; i < 20; i++ {
		var v byte
		if _, err := fmt.Sscanf(hex[i*2:i*2+2], "%02x", &v); err != nil {
			return metainfo.Hash{}, false
		}
		h[i] = v
	}
	return h, true
}

func (m *Module) ensureEngine(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.engine != nil {
		return nil
	}
	switch os.Getenv("DOWNLOADER_ENGINE") {
	case "fixture", "fake":
		slog.Info("using fixture torrent engine (no network)")
		m.engine = &fixtureEngine{}
		return nil
	}
	mode := storageMode()
	if mode == "mesh" {
		if m.mc == nil {
			return fmt.Errorf("DOWNLOAD_STORAGE=mesh requires mesh connection (MUXCORE_GRPC_ADDR)")
		}
		ms := meshstore.New(meshBackendFromClient(m.mc))
		m.mesh = ms
		eng, err := newAnacrolixEngineOpts(m.dlDir, m.listenPort, nil, anacrolixEngineOpts{
			EnableDHT:   m.enableDHT,
			EnablePEX:   m.enablePEX,
			MeshStorage: ms,
		})
		if err != nil {
			return err
		}
		m.engine = eng
		slog.Info("torrent storage: mesh (gRPC StorageService)", "prefix", "torrent/")
		return nil
	}
	if err := os.MkdirAll(m.dlDir, 0755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}
	eng, err := newAnacrolixEngineOpts(m.dlDir, m.listenPort, nil, anacrolixEngineOpts{
		EnableDHT: m.enableDHT,
		EnablePEX: m.enablePEX,
	})
	if err != nil {
		return err
	}
	m.engine = eng
	slog.Info("torrent storage: local file", "dir", m.dlDir)
	return nil
}
