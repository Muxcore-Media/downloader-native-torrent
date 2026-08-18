package internal

import (
	"crypto/sha1"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveSavePathRelative(t *testing.T) {
	got := resolveSavePath("/data/downloads", "partials/mv/btih_abc")
	want := filepath.Join("/data/downloads", "partials/mv/btih_abc")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if resolveSavePath("/data/downloads", "") != "/data/downloads" {
		t.Fatal("empty")
	}
	abs := filepath.Join(t.TempDir(), "abs")
	if resolveSavePath("/data/downloads", abs) != abs {
		t.Fatal("abs")
	}
}

func TestTryLinkSiblingPartialsMatch(t *testing.T) {
	root := t.TempDir()
	wanted := filepath.Join(root, "partials", "mv1")
	a := filepath.Join(wanted, "btih_aaa")
	c := filepath.Join(wanted, "btih_ccc")
	if err := os.MkdirAll(a, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c, 0755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 32)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(a, "movie.bin"), data, 0644); err != nil {
		t.Fatal(err)
	}
	h0 := sha1.Sum(data[:16])
	h1 := sha1.Sum(data[16:])
	layout := pieceLayout{
		PieceLength: 16,
		PieceHashes: [][]byte{h0[:], h1[:]},
		Files:       []fileInfo{{Path: "movie.bin", Size: 32}},
		TotalLength: 32,
	}
	linked, err := tryLinkSiblingPartials(c, layout)
	if err != nil {
		t.Fatal(err)
	}
	if !linked {
		t.Fatal("expected link")
	}
	got, err := os.ReadFile(filepath.Join(c, "movie.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 32 || got[0] != 0 || got[31] != 31 {
		t.Fatalf("linked bytes: %v", got)
	}
}

func TestTryLinkSiblingPartialsMismatch(t *testing.T) {
	root := t.TempDir()
	wanted := filepath.Join(root, "partials", "mv1")
	a := filepath.Join(wanted, "btih_aaa")
	c := filepath.Join(wanted, "btih_ccc")
	if err := os.MkdirAll(a, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c, 0755); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 32)
	other := make([]byte, 32)
	for i := range data {
		data[i] = 1
		other[i] = 2
	}
	if err := os.WriteFile(filepath.Join(a, "movie.bin"), other, 0644); err != nil {
		t.Fatal(err)
	}
	h0 := sha1.Sum(data[:16])
	h1 := sha1.Sum(data[16:])
	layout := pieceLayout{
		PieceLength: 16,
		PieceHashes: [][]byte{h0[:], h1[:]},
		Files:       []fileInfo{{Path: "movie.bin", Size: 32}},
		TotalLength: 32,
	}
	linked, err := tryLinkSiblingPartials(c, layout)
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Fatal("mismatch should not link")
	}
	if _, err := os.Stat(filepath.Join(c, "movie.bin")); !os.IsNotExist(err) {
		t.Fatal("dest should stay empty")
	}
}

func TestTryLinkSiblingPartialsInconclusive(t *testing.T) {
	root := t.TempDir()
	wanted := filepath.Join(root, "partials", "mv1")
	a := filepath.Join(wanted, "btih_aaa")
	c := filepath.Join(wanted, "btih_ccc")
	if err := os.MkdirAll(a, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(c, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a, "movie.bin"), []byte("short"), 0644); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 32)
	h0 := sha1.Sum(data[:16])
	layout := pieceLayout{
		PieceLength: 16,
		PieceHashes: [][]byte{h0[:]},
		Files:       []fileInfo{{Path: "movie.bin", Size: 32}},
		TotalLength: 32,
	}
	linked, err := tryLinkSiblingPartials(c, layout)
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Fatal("short sibling should be inconclusive")
	}
}
