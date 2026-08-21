package meshstore

import (
	"bytes"
	"context"
	"crypto/sha1"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

func TestPieceRoundTripAndSeed(t *testing.T) {
	be := NewMemBackend()
	c := New(be)
	t.Cleanup(func() { _ = c.Close() })

	const pieceLen = 32
	payload := bytes.Repeat([]byte("abcdefgh"), pieceLen/8)
	sum := sha1.Sum(payload)

	info := &metainfo.Info{
		PieceLength: pieceLen,
		Pieces:      sum[:],
		Files:       []metainfo.FileInfo{{Length: int64(len(payload)), Path: []string{"f.bin"}}},
	}
	var ih metainfo.Hash
	copy(ih[:], sum[:])

	impl, err := c.OpenTorrent(context.Background(), info, ih)
	if err != nil {
		t.Fatal(err)
	}
	p := info.Piece(0)
	pi := impl.Piece(p)

	if _, err := pi.WriteAt(payload, 0); err != nil {
		t.Fatal(err)
	}
	if err := pi.MarkComplete(); err != nil {
		t.Fatal(err)
	}
	comp := pi.Completion()
	if !comp.Ok || !comp.Complete {
		t.Fatalf("completion=%+v", comp)
	}

	// New client (simulates restart) must seed from storage.
	c2 := New(be)
	t.Cleanup(func() { _ = c2.Close() })
	impl2, err := c2.OpenTorrent(context.Background(), info, ih)
	if err != nil {
		t.Fatal(err)
	}
	pi2 := impl2.Piece(p)
	got := make([]byte, pieceLen)
	n, err := pi2.ReadAt(got, 0)
	if n != pieceLen {
		t.Fatalf("read n=%d err=%v", n, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("seed mismatch")
	}
	if !pi2.Completion().Complete {
		t.Fatal("expected complete after reload")
	}
}

func TestDeleteTorrent(t *testing.T) {
	be := NewMemBackend()
	c := New(be)
	payload := bytes.Repeat([]byte("x"), 16)
	sum := sha1.Sum(payload)
	info := &metainfo.Info{
		PieceLength: 16,
		Pieces:      sum[:],
		Files:       []metainfo.FileInfo{{Length: 16, Path: []string{"a"}}},
	}
	var ih metainfo.Hash
	copy(ih[:], sum[:])
	impl, err := c.OpenTorrent(context.Background(), info, ih)
	if err != nil {
		t.Fatal(err)
	}
	pi := impl.Piece(info.Piece(0))
	_, _ = pi.WriteAt(payload, 0)
	_ = pi.MarkComplete()
	if err := c.DeleteTorrent(context.Background(), ih); err != nil {
		t.Fatal(err)
	}
	keys, _ := be.List(context.Background(), c.Prefix(ih)+"/")
	if len(keys) != 0 {
		t.Fatalf("leftover keys: %v", keys)
	}
}
