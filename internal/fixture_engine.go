package internal

import (
	"context"
	"os"
	"path/filepath"
	"sync"
)

// fixtureEngine is a no-network torrentEngine for local MVP smoke.
// It materializes a parseable video file under savePath and completes instantly.
type fixtureEngine struct {
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
		abs := filepath.Join(savePath, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
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
