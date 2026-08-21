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

func TestCompletedTorrentDroppedFromSession(t *testing.T) {
	m := newTestModule(t)
	resp, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC&dn=Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
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
	if len(sess.Torrents) != 0 {
		t.Fatalf("expected empty session after complete, got %+v", sess)
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
