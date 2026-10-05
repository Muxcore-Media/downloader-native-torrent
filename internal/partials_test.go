package internal

import (
	"crypto/sha1"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashPartialDest(t *testing.T) {
	t.Parallel()
	pending := "/data/downloads/partials/mv1/pending_https___example_com_long"
	got, ok := hashPartialDest(pending, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if !ok {
		t.Fatal("expected dest")
	}
	want := "/data/downloads/partials/mv1/btih_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if _, ok := hashPartialDest(want, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); ok {
		t.Fatal("already-canonical path should not rename")
	}
	if _, ok := hashPartialDest("/data/downloads/Show", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); ok {
		t.Fatal("non-partials path should not rename")
	}
}

func TestMovePendingPartial(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "partials", "mv1", "pending_https_x")
	to := filepath.Join(root, "partials", "mv1", "btih_abc")
	if err := os.MkdirAll(from, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "f"), []byte("n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := movePendingPartial(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(to, "f")); err != nil {
		t.Fatalf("moved file missing: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatal("source still present")
	}
	if err := os.MkdirAll(from, 0755); err != nil {
		t.Fatal(err)
	}
	if err := movePendingPartial(from, to); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(to, "f")); err != nil {
		t.Fatal("existing dest lost")
	}
}

func TestJoinSaveAndRelPathNoDoubleJoin(t *testing.T) {
	t.Parallel()
	got := joinSaveAndRelPath("/data/downloads/partials/item1", "partials/item1/Show.mkv")
	if got != "/data/downloads/partials/item1/Show.mkv" {
		t.Fatalf("got %q", got)
	}
	got = joinSaveAndRelPath("/downloads/Show", "Show/S01E01.mkv")
	if got != "/downloads/Show/S01E01.mkv" {
		t.Fatalf("dir prefix %q", got)
	}
	got = joinSaveAndRelPath("/downloads", "Show/S01E01.mkv")
	if got != "/downloads/Show/S01E01.mkv" {
		t.Fatalf("plain join %q", got)
	}
}

func TestResolveSavePathRelative(t *testing.T) {
	got := resolveSavePath("/data/downloads", "partials/mv/btih_abc")
	want := filepath.Join("/data/downloads", "partials/mv/btih_abc")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if resolveSavePath("/data/downloads", "") != "/data/downloads" {
		t.Fatal("empty")
	}
	// An absolute path outside the download dir is clamped, never honoured.
	abs := filepath.Join(t.TempDir(), "abs")
	if resolveSavePath("/data/downloads", abs) != "/data/downloads" {
		t.Fatal("abs outside must clamp to dataDir")
	}
}

func TestResolveSavePathNeverCwdOrEscape(t *testing.T) {
	dataDir := t.TempDir()
	got := resolveSavePath(dataDir, "partials/mv/btih_abc")
	if !filepath.IsAbs(got) {
		t.Fatalf("not absolute: %q", got)
	}
	if !strings.HasPrefix(got, dataDir) {
		t.Fatalf("%q not under dataDir %q", got, dataDir)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cwdPartials := filepath.Join(cwd, "partials")
	if got == cwdPartials || strings.HasPrefix(got, cwdPartials+string(filepath.Separator)) {
		t.Fatalf("resolved to cwd partials: %q", got)
	}
	escaped := resolveSavePath(dataDir, filepath.Join("..", "outside"))
	if escaped != filepath.Clean(dataDir) {
		t.Fatalf("escape clamped to dataDir, got %q", escaped)
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
