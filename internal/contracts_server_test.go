package internal

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	cdlv1 "github.com/Muxcore-Media/contracts-downloader/muxcore/downloader/v1"
	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func TestContractsServerCapabilities(t *testing.T) {
	m := newTestModule(t)
	srv := &contractsServer{m: m}
	resp, err := srv.GetCapabilities(context.Background(), &cdlv1.GetCapabilitiesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetSupportsFileSelection() {
		t.Fatal("expected SupportsFileSelection=true")
	}
}

func TestContractsListTorrentsCategoryAndStatus(t *testing.T) {
	m := newTestModuleWithEngine(t, &fakeEngine{stuck: true})
	ctx := context.Background()
	one, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri:   "magnet:?xt=urn:btih:1111111111111111111111111111111111111111&dn=One",
		Label: "tv",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri:   "magnet:?xt=urn:btih:2222222222222222222222222222222222222222&dn=Two",
		Label: "movies",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := &contractsServer{m: m}
	byCat, err := srv.ListTorrents(ctx, &cdlv1.ListTorrentsRequest{Category: "tv"})
	if err != nil {
		t.Fatal(err)
	}
	if len(byCat.GetTorrents()) != 1 || byCat.GetTorrents()[0].GetId() != one.Id {
		t.Fatalf("category filter: %+v", byCat.GetTorrents())
	}
	byBoth, err := srv.ListTorrents(ctx, &cdlv1.ListTorrentsRequest{
		Category: "tv",
		Status:   cdlv1.TorrentStatus_TORRENT_STATUS_DOWNLOADING,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byBoth.GetTorrents()) != 1 {
		t.Fatalf("AND filter expected 1 got %d", len(byBoth.GetTorrents()))
	}
	byBothMovies, err := srv.ListTorrents(ctx, &cdlv1.ListTorrentsRequest{
		Category: "movies",
		Status:   cdlv1.TorrentStatus_TORRENT_STATUS_PAUSED,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(byBothMovies.GetTorrents()) != 0 {
		t.Fatalf("movies+paused should be empty, got %d", len(byBothMovies.GetTorrents()))
	}
}

func TestContractsTorrentFileWanted(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "downloads")
	m := NewModule(Config{
		GRPCAddr:         ":0",
		DownloadDir:      dir,
		Engine:           &fakeEngine{stuck: true},
		FilePriorityMode: filePriorityEpisodes,
		InfoTimeout:      5 * time.Second,
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })

	add, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:3333333333333333333333333333333333333333&dn=S01E01",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv := &contractsServer{m: m}
		resp, err := srv.GetTorrent(ctx, &cdlv1.GetTorrentRequest{TorrentId: add.Id})
		if err != nil {
			t.Fatal(err)
		}
		if len(resp.GetTorrent().GetFiles()) > 0 {
			if !resp.GetTorrent().GetFiles()[0].GetWanted() {
				t.Fatal("episode file should be wanted")
			}
			if resp.GetTorrent().GetUploaded() < 0 {
				t.Fatal("uploaded must be non-negative")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for files")
}
