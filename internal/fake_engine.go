package internal

import (
	"context"
	"sync"
	"time"
)

// fakeEngine is an injectable torrentEngine for unit tests (no network / anacrolix).
type fakeEngine struct {
	mu      sync.Mutex
	failAdd error
	failInfo error
	instantComplete bool
	torrents []*fakeManaged
}

func (e *fakeEngine) Close() error { return nil }

func (e *fakeEngine) AddURI(ctx context.Context, uri, savePath string) (managedTorrent, error) {
	if e.failAdd != nil {
		return nil, e.failAdd
	}
	name := parseMagnetName(uri)
	hash := parseInfoHash(uri)
	if name == "unknown" {
		name = "fake-torrent"
	}
	if hash == "" {
		hash = "ffffffffffffffffffffffffffffffffffffffff"
	}
	ft := &fakeManaged{
		name:     name,
		hash:     hash,
		total:    1024 * 1024,
		failInfo: e.failInfo,
		instant:  e.instantComplete,
	}
	e.mu.Lock()
	e.torrents = append(e.torrents, ft)
	e.mu.Unlock()
	return ft, nil
}

type fakeManaged struct {
	mu       sync.Mutex
	name     string
	hash     string
	total    int64
	done     int64
	uploaded int64
	failInfo error
	instant  bool
	started  bool
	dropped  bool
}

func (f *fakeManaged) WaitInfo(ctx context.Context) error {
	if f.failInfo != nil {
		return f.failInfo
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (f *fakeManaged) Name() string     { return f.name }
func (f *fakeManaged) InfoHash() string { return f.hash }
func (f *fakeManaged) TotalLength() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total
}

func (f *fakeManaged) Files() []fileInfo {
	return []fileInfo{{Path: f.name + "/file.bin", Size: f.total, Downloaded: f.BytesCompleted()}}
}

func (f *fakeManaged) DownloadAll() {
	f.mu.Lock()
	if f.started {
		f.mu.Unlock()
		return
	}
	f.started = true
	instant := f.instant
	f.mu.Unlock()
	go func() {
		if instant {
			f.mu.Lock()
			f.done = f.total
			f.uploaded = f.total // ratio 1.0 immediately for seed exit
			f.mu.Unlock()
			return
		}
		for i := 1; i <= 10; i++ {
			time.Sleep(5 * time.Millisecond)
			f.mu.Lock()
			f.done = f.total * int64(i) / 10
			if i == 10 {
				f.uploaded = f.total
			}
			f.mu.Unlock()
		}
	}()
}

func (f *fakeManaged) PauseDownload() {
	f.mu.Lock()
	f.started = false
	f.mu.Unlock()
}

func (f *fakeManaged) BytesCompleted() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.done
}

func (f *fakeManaged) BytesMissing() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.total - f.done
}

func (f *fakeManaged) BytesUploaded() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.uploaded
}

func (f *fakeManaged) ActivePeers() int      { return 2 }
func (f *fakeManaged) ConnectedSeeders() int { return 1 }

func (f *fakeManaged) Drop() {
	f.mu.Lock()
	f.dropped = true
	f.mu.Unlock()
}
