package internal

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func newTestModuleAt(t *testing.T, dir string, eng torrentEngine) *Module {
	t.Helper()
	m := NewModule(Config{
		GRPCAddr:    ":0",
		DownloadDir: dir,
		Engine:      eng,
		SeedMinutes: 60,
		SeedRatio:   99,
		InfoTimeout: 5 * time.Second,
	})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Stop(context.Background()) })
	return m
}

func TestTorrentSessionPersistsAcrossRestart(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	uri := "magnet:?xt=urn:btih:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB&dn=Persist+Me"
	m1 := newTestModuleAt(t, dir, &fakeEngine{stuck: true})
	resp, err := m1.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri:      uri,
		SavePath: "partials/item/pending_x",
		Label:    "tv",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(dir, "partials/item/btih_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	deadline := time.Now().Add(2 * time.Second)
	for {
		raw, err := os.ReadFile(m1.sessionPath())
		if err != nil {
			t.Fatalf("session file: %v", err)
		}
		var sess torrentSession
		if err := json.Unmarshal(raw, &sess); err != nil {
			t.Fatal(err)
		}
		if len(sess.Torrents) == 1 && sess.Torrents[0].ID == resp.Id && sess.Torrents[0].URI == uri {
			if sess.Torrents[0].SavePath == wantPath || time.Now().After(deadline) {
				if sess.Torrents[0].SavePath != wantPath {
					t.Fatalf("save path %q want %q", sess.Torrents[0].SavePath, wantPath)
				}
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := m1.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	m2 := newTestModuleAt(t, dir, &fakeEngine{stuck: true})
	m2.restorePersistedTorrents()
	got, err := m2.GetTorrent(context.Background(), &downloaderv1.GetTorrentRequest{Id: resp.Id})
	if err != nil {
		t.Fatalf("restore GetTorrent: %v", err)
	}
	if got.GetTorrent().GetId() != resp.Id {
		t.Fatalf("id %q want %q", got.GetTorrent().GetId(), resp.Id)
	}
	if got.GetTorrent().GetSavePath() != wantPath {
		t.Fatalf("save path %q want %q", got.GetTorrent().GetSavePath(), wantPath)
	}
}

func TestCompletedTorrentPersistsInSession(t *testing.T) {
	m := newTestModule(t)
	resp, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC&dn=Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		got, err := m.GetTorrent(context.Background(), &downloaderv1.GetTorrentRequest{Id: resp.Id})
		if err != nil {
			t.Fatal(err)
		}
		if got.GetTorrent().GetStatus() == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status %q never completed", got.GetTorrent().GetStatus())
		}
		time.Sleep(20 * time.Millisecond)
	}
	raw, err := os.ReadFile(m.sessionPath())
	if err != nil {
		t.Fatal(err)
	}
	var sess torrentSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatal(err)
	}
	if len(sess.Torrents) != 1 {
		t.Fatalf("expected completed torrent in session, got %+v", sess)
	}
	if sess.Torrents[0].Status != "completed" {
		t.Fatalf("status %q want completed", sess.Torrents[0].Status)
	}
}

func TestStopDoesNotWipeSessionOnCancel(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	m := newTestModuleAt(t, dir, &fakeEngine{stuck: true})
	resp, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD&dn=Keep",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	raw, err := os.ReadFile(filepath.Join(dir, torrentSessionFile))
	if err != nil {
		t.Fatal(err)
	}
	var sess torrentSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatal(err)
	}
	if len(sess.Torrents) != 1 || sess.Torrents[0].ID != resp.Id {
		t.Fatalf("stop wiped session: %+v", sess)
	}
}

func TestPausedTorrentPersistsPausedFlag(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	m := newTestModuleAt(t, dir, &fakeEngine{stuck: true})
	resp, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri:    "magnet:?xt=urn:btih:EEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEEE&dn=Paused",
		Paused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(m.sessionPath())
	if err != nil {
		t.Fatal(err)
	}
	var sess torrentSession
	if err := json.Unmarshal(raw, &sess); err != nil {
		t.Fatal(err)
	}
	if len(sess.Torrents) != 1 || !sess.Torrents[0].Paused || sess.Torrents[0].ID != resp.Id {
		t.Fatalf("paused session %+v", sess)
	}
}

type memMeshSessions struct {
	data map[string][]byte
}

func (s *memMeshSessions) PutBytes(_ context.Context, key string, data []byte) error {
	if s.data == nil {
		s.data = make(map[string][]byte)
	}
	s.data[key] = append([]byte(nil), data...)
	return nil
}

func (s *memMeshSessions) GetBytes(_ context.Context, key string, _, _ int64) ([]byte, error) {
	if s.data == nil {
		return nil, os.ErrNotExist
	}
	b, ok := s.data[key]
	if !ok {
		return nil, os.ErrNotExist
	}
	return b, nil
}

func TestMeshSessionPersistRestore(t *testing.T) {
	t.Setenv("DOWNLOAD_STORAGE", "mesh")
	t.Setenv("MUXCORE_GRPC_ADDR", "127.0.0.1:65534")
	store := &memMeshSessions{}
	m1 := NewModule(Config{
		GRPCAddr:     ":0",
		DownloadDir:  t.TempDir(),
		Engine:       &fakeEngine{stuck: true},
		MeshSessions: store,
		InfoTimeout:  5 * time.Second,
	})
	ctx := context.Background()
	if err := m1.Init(ctx); err != nil {
		t.Fatal(err)
	}
	uri := "magnet:?xt=urn:btih:5555555555555555555555555555555555555555&dn=Mesh"
	resp, err := m1.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: uri, Label: "mesh"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := store.data["torrent/sessions.json"]; ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, ok := store.data["torrent/sessions.json"]; !ok {
		t.Fatal("mesh sessions.json not written")
	}
	if err := m1.Stop(ctx); err != nil {
		t.Fatal(err)
	}

	m2 := NewModule(Config{
		GRPCAddr:     ":0",
		DownloadDir:  m1.dlDir,
		Engine:       &fakeEngine{stuck: true},
		MeshSessions: store,
		InfoTimeout:  5 * time.Second,
	})
	if err := m2.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m2.Stop(ctx) })
	m2.restorePersistedTorrents()
	got, err := m2.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: resp.Id})
	if err != nil {
		t.Fatalf("restore GetTorrent: %v", err)
	}
	if got.GetTorrent().GetLabel() != "mesh" {
		t.Fatalf("label %q", got.GetTorrent().GetLabel())
	}
}
