package meshstore

import (
	"bytes"
	"context"
	"crypto/sha1"
	"io"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

func TestAssembleFilesFromPieces(t *testing.T) {
	be := NewMemBackend()
	c := New(be)
	t.Cleanup(func() { _ = c.Close() })

	const pieceLen = 16
	payload := bytes.Repeat([]byte("abcdefghijklmnop"), 2) // 32 bytes, 2 pieces
	h0 := sha1.Sum(payload[:16])
	h1 := sha1.Sum(payload[16:])
	pieces := append(h0[:], h1[:]...)

	info := &metainfo.Info{
		PieceLength: pieceLen,
		Pieces:      pieces,
		Files: []metainfo.FileInfo{
			{Length: 20, Path: []string{"dir", "a.mkv"}},
			{Length: 12, Path: []string{"b.mkv"}},
		},
	}
	var ih metainfo.Hash
	copy(ih[:], h0[:])

	impl, err := c.OpenTorrent(context.Background(), info, ih)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		p := info.Piece(i)
		pi := impl.Piece(p)
		start := i * pieceLen
		end := start + pieceLen
		if _, err := pi.WriteAt(payload[start:end], 0); err != nil {
			t.Fatal(err)
		}
		if err := pi.MarkComplete(); err != nil {
			t.Fatal(err)
		}
	}

	out, err := c.AssembleFiles(context.Background(), info, ih)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Fatalf("assembled %d want 2", len(out))
	}
	if out[0].URI != "storage://torrent/"+ih.HexString()+"/files/dir/a.mkv" {
		t.Fatalf("uri0=%q", out[0].URI)
	}
	got, err := be.GetBytes(context.Background(), out[0].Key, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload[:20]) {
		t.Fatalf("file0 mismatch")
	}
	got, err = be.GetBytes(context.Background(), out[1].Key, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload[20:]) {
		t.Fatalf("file1 mismatch")
	}
	_ = io.EOF
}
