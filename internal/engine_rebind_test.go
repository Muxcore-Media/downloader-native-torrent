package internal

import (
	"context"
	"testing"
	"time"

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func TestRebindMigratesLiveSessions(t *testing.T) {
	eng := &rebindTrackingEngine{}
	m := newTestModuleWithEngine(t, eng)
	ctx := context.Background()
	resp, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:4444444444444444444444444444444444444444&dn=Rebind",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		get, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: resp.Id})
		if err != nil {
			t.Fatal(err)
		}
		if get.GetTorrent().GetStatus() == "downloading" || get.GetTorrent().GetStatus() == "queued" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := m.rebindEngine("10.2.0.2", 54321); err != nil {
		t.Fatal(err)
	}
	if eng.rebinds != 1 {
		t.Fatalf("rebinds=%d want 1", eng.rebinds)
	}
	get, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: resp.Id})
	if err != nil {
		t.Fatal(err)
	}
	if st := get.GetTorrent().GetStatus(); st == "error" {
		t.Fatalf("torrent failed after rebind: %s", get.GetTorrent().GetError())
	}
}
