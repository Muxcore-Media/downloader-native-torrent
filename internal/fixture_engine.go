package internal

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Muxcore-Media/downloader-native-torrent/internal/meshstore"
)

// errURISavePath is returned when a URI-looking path (e.g. storage://...) is
// used where a local filesystem path is required.
var errURISavePath = fmt.Errorf("save path is a URI, not a local directory")

// rejectURIPath guards every local filesystem write: paths containing "://"
// must never be turned into directories (they would create a literal "storage:"
// directory relative to the CWD).
func rejectURIPath(p string) error {
	if strings.Contains(p, "://") {
		return fmt.Errorf("%w: %q", errURISavePath, p)
	}
	return nil
}

// mkdirAllLocal is os.MkdirAll behind the URI guard.
func mkdirAllLocal(p string, perm os.FileMode) error {
	if err := rejectURIPath(p); err != nil {
		return err
	}
	return os.MkdirAll(p, perm)
}

// fixtureStreamPutter is implemented by backends that can stream uploads.
type fixtureStreamPutter interface {
	PutReader(ctx context.Context, key string, r io.Reader) error
}

// fixtureEngine is a no-network torrentEngine for local MVP smoke.
// It materializes a parseable video file under savePath and completes instantly.
//
// In mesh storage mode (storage:// save paths) the payload is written through
// meshBackend (the same StorageService the live engine assembles into) under
// torrent/{infohash}/files/{rel}; no local files are created.
type fixtureEngine struct {
	// meshBackend resolves the mesh storage backend lazily (the core dial may
	// happen after the engine is created). Nil or returning nil means unavailable.
	meshBackend func() meshstore.Backend

	mu       sync.Mutex
	torrents []*fixtureManaged
}

func (e *fixtureEngine) Close() error { return nil }

func (e *fixtureEngine) AddURI(ctx context.Context, uri, savePath string) (managedTorrent, error) {
	name := parseMagnetName(uri)
	hash := parseInfoHash(uri)
	if name == "unknown" || name == "" {
		name = "Fixture.Movie.1999.1080p.BluRay"
	}
	if hash == "" {
		hash = "ffffffffffffffffffffffffffffffffffffffff"
	}
	ft := &fixtureManaged{
		name:     name,
		hash:     hash,
		savePath: savePath,
		backend:  e.meshBackend,
		total:    fixturePayloadSize(fixturePayloadDefaultSize),
	}
	e.mu.Lock()
	e.torrents = append(e.torrents, ft)
	e.mu.Unlock()
	return ft, nil
}

type fixtureManaged struct {
	mu       sync.Mutex
	name     string
	hash     string
	savePath string
	total    int64
	done     int64
	uploaded int64
	started  bool
	relPath  string
	backend  func() meshstore.Backend
}

func (f *fixtureManaged) WaitInfo(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (f *fixtureManaged) Name() string     { return f.name }
func (f *fixtureManaged) InfoHash() string { return f.hash }
func (f *fixtureManaged) TotalLength() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

func (f *fixtureManaged) Files() []fileInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	path := f.relPath
	if path == "" {
		path = filepath.ToSlash(filepath.Join(f.name, f.name+".mkv"))
	}
	return []fileInfo{{Path: path, Size: f.total, Downloaded: f.done}}
}

func (f *fixtureManaged) DownloadAll() {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return
	}
	f.started = true
	name := f.name
	savePath := f.savePath
	total := f.total
	f.mu.Unlock()

	go func() {
		rel := filepath.Join(name, name+".mkv")
		if strings.HasPrefix(savePath, "storage://") {
			f.materializeMesh(rel, total)
			return
		}
		abs := filepath.Join(savePath, rel)
		if err := mkdirAllLocal(filepath.Dir(abs), 0o755); err != nil {
			slog.Warn("fixture engine: cannot create save dir", "error", err)
			return
		}
		if err := materializeFixtureFile(abs, total); err != nil {
			return
		}
		f.mu.Lock()
		f.relPath = filepath.ToSlash(rel)
		if info, err := os.Stat(abs); err == nil && info.Size() > 0 {
			f.total = info.Size()
			f.done = info.Size()
		} else {
			f.done = total
		}
		f.uploaded = f.done // satisfy default seed ratio immediately
		f.mu.Unlock()
	}()
}

// materializeMesh writes the fixture payload to mesh storage at
// torrent/{hash}/files/{rel} and records the storage:// URI as the file path,
// matching what the live engine's AssembleFiles reports.
func (f *fixtureManaged) materializeMesh(rel string, total int64) {
	var b meshstore.Backend
	if f.backend != nil {
		b = f.backend()
	}
	if b == nil {
		slog.Warn("fixture engine: mesh storage backend unavailable; payload not written", "hash", f.hash)
		return
	}
	r, size, closeFn, err := fixturePayloadReader(total)
	if err != nil {
		slog.Warn("fixture engine: open payload", "error", err)
		return
	}
	defer closeFn()
	key := fmt.Sprintf("torrent/%s/files/%s", strings.ToLower(f.hash), filepath.ToSlash(rel))
	ctx := context.Background()
	if ps, ok := b.(fixtureStreamPutter); ok {
		err = ps.PutReader(ctx, key, r)
	} else {
		var data []byte
		if data, err = io.ReadAll(r); err == nil {
			err = b.PutBytes(ctx, key, data)
		}
	}
	if err != nil {
		slog.Warn("fixture engine: mesh put", "key", key, "error", err)
		return
	}
	f.mu.Lock()
	f.relPath = "storage://" + key
	f.total = size
	f.done = size
	f.uploaded = size
	f.mu.Unlock()
}

func (f *fixtureManaged) PauseDownload() {
	f.mu.Lock()
	f.started = false
	f.mu.Unlock()
}

func (f *fixtureManaged) BytesCompleted() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *fixtureManaged) BytesMissing() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total - f.done
}

func (f *fixtureManaged) BytesUploaded() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploaded
}

func (f *fixtureManaged) ActivePeers() int      { return 0 }
func (f *fixtureManaged) ConnectedSeeders() int { return 0 }

func (f *fixtureManaged) Drop() {}
