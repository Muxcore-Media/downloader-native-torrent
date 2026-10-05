package internal

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestMaterializeFixtureFileSparsePayload(t *testing.T) {
	t.Setenv("QBIT_FIXTURE_MEDIA_SEED", "")
	t.Setenv("FIXTURE_MEDIA_SEED", "")
	size := fixturePayloadSize(fixturePayloadDefaultSize)
	if size < 6*1024*1024 {
		t.Fatalf("payload size %d below 6 MiB", size)
	}
	abs := filepath.Join(t.TempDir(), "f.mkv")
	if err := materializeFixtureFile(abs, size); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != size {
		t.Fatalf("size = %d, want %d", info.Size(), size)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < fixtureHeaderSize; i++ {
		if data[i] != byte(i%251) {
			t.Fatalf("header byte %d = %d, want %d", i, data[i], byte(i%251))
		}
	}
	for _, b := range data[fixtureHeaderSize:] {
		if b != 0 {
			t.Fatal("tail is not zero-filled")
		}
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		used := st.Blocks * 512
		if used > size/2 {
			t.Skipf("filesystem does not appear to support sparse files (%d bytes used of %d)", used, size)
		}
	}
}

func TestFixtureEngineReportsPayloadSize(t *testing.T) {
	t.Setenv("QBIT_FIXTURE_MEDIA_SEED", "")
	t.Setenv("FIXTURE_MEDIA_SEED", "")
	e := &fixtureEngine{}
	mt, err := e.AddURI(t.Context(), "magnet:?xt=urn:btih:"+"aa"+"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa&dn=X", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if mt.TotalLength() < 6*1024*1024 {
		t.Fatalf("total %d below 6 MiB", mt.TotalLength())
	}
}
