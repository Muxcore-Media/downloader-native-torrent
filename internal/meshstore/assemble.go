package meshstore

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/anacrolix/torrent/metainfo"
)

// AssembledFile is one torrent file materialised as a storage object.
type AssembledFile struct {
	RelPath string // path inside the torrent
	Key     string // storage key (torrent/{ih}/files/...)
	URI     string // storage://… URI for ImportPath
	Size    int64
}

// AssembleFiles reads completed pieces and writes whole-file objects under
// torrent/{infohash}/files/{relpath}. Call after all pieces are complete.
func (c *Client) AssembleFiles(ctx context.Context, info *metainfo.Info, ih metainfo.Hash) ([]AssembledFile, error) {
	if info == nil {
		return nil, fmt.Errorf("nil info")
	}
	pieceLen := info.PieceLength
	if pieceLen <= 0 {
		return nil, fmt.Errorf("invalid piece length")
	}
	var out []AssembledFile
	var offset int64
	var firstErr error
	for _, fi := range info.UpvertedFiles() {
		rel := path.Join(fi.BestPath()...)
		rel = strings.TrimPrefix(rel, "/")
		if rel == "" || strings.Contains(rel, "..") {
			offset += fi.Length
			continue
		}
		key := fmt.Sprintf("%s/%s/files/%s", c.prefix, ih.HexString(), rel)
		ra := &pieceRangeReaderAt{
			backend:  c.backend,
			prefix:   c.prefix,
			ih:       ih.HexString(),
			pieceLen: pieceLen,
			start:    offset,
			length:   fi.Length,
		}
		sr := io.NewSectionReader(ra, 0, fi.Length)
		if err := putReader(ctx, c.backend, key, sr); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("assemble %s: %w", rel, err)
			}
			offset += fi.Length
			continue
		}
		out = append(out, AssembledFile{
			RelPath: rel,
			Key:     key,
			URI:     "storage://" + key,
			Size:    fi.Length,
		})
		offset += fi.Length
	}
	if len(out) == 0 && firstErr != nil {
		return nil, firstErr
	}
	return out, firstErr
}

func putReader(ctx context.Context, b Backend, key string, r io.Reader) error {
	if ps, ok := b.(streamPutter); ok {
		return ps.PutReader(ctx, key, r)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return b.PutBytes(ctx, key, data)
}

type streamPutter interface {
	PutReader(ctx context.Context, key string, r io.Reader) error
}

// pieceRangeReaderAt reads [start, start+length) of the torrent payload from piece objects.
type pieceRangeReaderAt struct {
	backend  Backend
	prefix   string
	ih       string
	pieceLen int64
	start    int64
	length   int64
}

func (r *pieceRangeReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= r.length {
		return 0, io.EOF
	}
	abs := r.start + off
	remain := r.length - off
	want := int64(len(p))
	if want > remain {
		want = remain
	}
	n := 0
	for want > 0 {
		idx := abs / r.pieceLen
		within := abs % r.pieceLen
		chunk := r.pieceLen - within
		if chunk > want {
			chunk = want
		}
		key := fmt.Sprintf("%s/%s/p/%d", r.prefix, r.ih, idx)
		ctx := context.Background()
		data, err := r.backend.GetBytes(ctx, key, within, chunk)
		if err != nil {
			return n, err
		}
		copied := copy(p[n:], data)
		n += copied
		abs += int64(copied)
		want -= int64(copied)
		if int64(copied) < chunk {
			break
		}
	}
	if n == 0 {
		return 0, io.EOF
	}
	if int64(n) < int64(len(p)) && off+int64(n) >= r.length {
		return n, io.EOF
	}
	return n, nil
}
