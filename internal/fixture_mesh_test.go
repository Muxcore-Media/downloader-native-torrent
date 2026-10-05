package internal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Muxcore-Media/downloader-native-torrent/internal/meshstore"
)

const fixtureMeshHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestFixtureMeshWritesToStorageNotLocalDir(t *testing.T) {
	t.Setenv("QBIT_FIXTURE_MEDIA_SEED", "")
	t.Setenv("FIXTURE_MEDIA_SEED", "")
	cwd := t.TempDir()
	t.Chdir(cwd)

	mem := meshstore.NewMemBackend()
	e := &fixtureEngine{meshBackend: func() meshstore.Backend { return mem }}
	mt, err := e.AddURI(context.Background(), "magnet:?xt=urn:btih:"+fixtureMeshHash+"&dn=Mesh.Movie", storageSavePath(fixtureMeshHash))
	if err != nil {
		t.Fatal(err)
	}
	mt.DownloadAll()

	wantKey := "torrent/" + fixtureMeshHash + "/files/Mesh.Movie/Mesh.Movie.mkv"
	deadline := time.Now().Add(10 * time.Second)
	for mt.BytesMissing() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if mt.BytesMissing() != 0 {
		t.Fatal("fixture did not complete")
	}
	found, size, err := mem.Stat(context.Background(), wantKey)
	if err != nil || !found || size != mt.TotalLength() || size < 6*1024*1024 {
		t.Fatalf("storage object: found=%v size=%d total=%d err=%v", found, size, mt.TotalLength(), err)
	}
	files := mt.Files()
	if len(files) != 1 || files[0].Path != "storage://"+wantKey {
		t.Fatalf("files = %+v", files)
	}
	entries, _ := os.ReadDir(cwd)
	if len(entries) != 0 {
		t.Fatalf("fixture+mesh created local entries in CWD: %v", entries)
	}
}

func TestFixtureMeshWithoutBackendWritesNothingLocally(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	e := &fixtureEngine{}
	mt, _ := e.AddURI(context.Background(), "magnet:?xt=urn:btih:"+fixtureMeshHash, "storage://torrent/pending")
	mt.DownloadAll()
	time.Sleep(100 * time.Millisecond)
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("local entries created: %v", entries)
	}
}

func TestFixtureLocalModeUnchanged(t *testing.T) {
	t.Setenv("QBIT_FIXTURE_MEDIA_SEED", "")
	t.Setenv("FIXTURE_MEDIA_SEED", "")
	dir := t.TempDir()
	e := &fixtureEngine{}
	mt, _ := e.AddURI(context.Background(), "magnet:?xt=urn:btih:"+fixtureMeshHash+"&dn=Local.Movie", dir)
	mt.DownloadAll()
	deadline := time.Now().Add(10 * time.Second)
	for mt.BytesMissing() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(dir, "Local.Movie", "Local.Movie.mkv")); err != nil {
		t.Fatal(err)
	}
	if got := mt.Files()[0].Path; got != "Local.Movie/Local.Movie.mkv" {
		t.Fatalf("path = %q", got)
	}
}

func TestRejectURIPathGuard(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	for _, p := range []string{"storage://torrent/x", "storage:/torrent/x/../../y://z", "http://h/p"} {
		if strings.Contains(p, "://") {
			err := mkdirAllLocal(p, 0o755)
			if !errors.Is(err, errURISavePath) {
				t.Fatalf("%q: err=%v", p, err)
			}
		}
	}
	if entries, _ := os.ReadDir(cwd); len(entries) != 0 {
		t.Fatalf("guard let a dir through: %v", entries)
	}
	if err := mkdirAllLocal(filepath.Join(cwd, "ok", "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
}
