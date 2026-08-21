package internal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func TestStorageModeMesh(t *testing.T) {
	t.Setenv("DOWNLOAD_STORAGE", "mesh")
	t.Setenv("MUXCORE_GRPC_ADDR", "127.0.0.1:9090")
	if got := storageMode(); got != "mesh" {
		t.Fatalf("got %q", got)
	}
}

func TestInitMeshDoesNotCreateDownloadDir(t *testing.T) {
	t.Setenv("DOWNLOAD_STORAGE", "mesh")
	t.Setenv("MUXCORE_GRPC_ADDR", "127.0.0.1:65534")
	t.Setenv("DOWNLOADER_ENGINE", "fixture")
	missing := filepath.Join(t.TempDir(), "absent", "downloads")
	m := NewModule(Config{
		DownloadDir: missing,
		GRPCAddr:    ":0",
		Engine:      &fixtureEngine{},
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Stop(ctx) })
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("mesh mode must not create DOWNLOAD_DIR; err=%v", err)
	}
	if err := m.Health(ctx); err == nil {
		t.Fatal("expected health failure without mesh client")
	}
}

func TestMeshPendingDest(t *testing.T) {
	got, ok := meshPendingDest("storage://torrent/pending", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if !ok || got != "storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if _, ok := meshPendingDest("storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); ok {
		t.Fatal("already hashed path should not relocate")
	}
	if _, ok := meshPendingDest("storage://torrent/pending", "short"); ok {
		t.Fatal("short hash should not relocate")
	}
}

func TestMagnetFallbackURI(t *testing.T) {
	got := magnetFallbackURI("http://127.0.0.1:9696/8/download?apikey=x", "storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if got != "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("got %q", got)
	}
	if magnetFallbackURI("magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "storage://torrent/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") != "" {
		t.Fatal("magnet uri should not fallback")
	}
}

func TestMeshPendingSavePathUpdatesToInfoHash(t *testing.T) {
	t.Setenv("DOWNLOAD_STORAGE", "mesh")
	t.Setenv("MUXCORE_GRPC_ADDR", "127.0.0.1:65534")
	m := newTestModuleWithEngine(t, &fakeEngine{instantComplete: true})
	add, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri: "https://indexer.example/download.php?id=99",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "storage://torrent/ffffffffffffffffffffffffffffffffffffffff"
	deadline := time.Now().Add(3 * time.Second)
	var gotPath string
	for time.Now().Before(deadline) {
		get, err := m.GetTorrent(context.Background(), &downloaderv1.GetTorrentRequest{Id: add.Id})
		if err != nil {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		gotPath = get.GetTorrent().GetSavePath()
		if gotPath == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("save path %q want %q", gotPath, want)
}
