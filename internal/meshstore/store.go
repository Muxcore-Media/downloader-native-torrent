// Package meshstore implements anacrolix torrent storage backed by MuxCore
// StorageService (gRPC). Piece bytes live as objects under
// torrent/{infohash}/p/{index}; completion bitfield under
// torrent/{infohash}/completion. No local DOWNLOAD_DIR is required.
package meshstore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// Backend is the subset of the mesh StorageClient used for piece I/O.
type Backend interface {
	PutBytes(ctx context.Context, key string, data []byte) error
	GetBytes(ctx context.Context, key string, offset, length int64) ([]byte, error)
	Delete(ctx context.Context, key string) error
	Stat(ctx context.Context, key string) (found bool, size int64, err error)
	List(ctx context.Context, prefix string) ([]string, error)
}

// Client is an anacrolix storage.ClientImplCloser over mesh storage.
type Client struct {
	backend Backend
	prefix  string // default "torrent"
	timeout time.Duration

	mu   sync.Mutex
	pc   storage.PieceCompletion
	open map[metainfo.Hash]*torrent
}

// New returns a mesh-backed torrent storage client.
func New(backend Backend) *Client {
	return &Client{
		backend: backend,
		prefix:  "torrent",
		timeout: 2 * time.Minute,
		pc:      storage.NewMapPieceCompletion(),
		open:    make(map[metainfo.Hash]*torrent),
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.open {
		_ = t.close()
	}
	c.open = make(map[metainfo.Hash]*torrent)
	if c.pc != nil {
		return c.pc.Close()
	}
	return nil
}

func (c *Client) OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if t, ok := c.open[infoHash]; ok {
		return t.impl(), nil
	}
	t := &torrent{
		client:   c,
		infoHash: infoHash,
		info:     info,
		pieces:   make(map[int]*piece),
	}
	if err := t.loadCompletion(ctx); err != nil {
		slog.Debug("meshstore: load completion", "hash", infoHash.HexString(), "error", err)
	}
	c.open[infoHash] = t
	return t.impl(), nil
}

func (c *Client) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), c.timeout)
}

func (c *Client) pieceKey(ih metainfo.Hash, index int) string {
	return fmt.Sprintf("%s/%s/p/%d", c.prefix, ih.HexString(), index)
}

func (c *Client) completionKey(ih metainfo.Hash) string {
	return fmt.Sprintf("%s/%s/completion", c.prefix, ih.HexString())
}

// Prefix returns the storage key prefix for a torrent (torrent/{hex}).
func (c *Client) Prefix(ih metainfo.Hash) string {
	return fmt.Sprintf("%s/%s", c.prefix, ih.HexString())
}

func (c *Client) metaKey(ih metainfo.Hash) string {
	return fmt.Sprintf("%s/%s/metainfo", c.prefix, ih.HexString())
}

// PutMeta stores the raw .torrent bytes for an infohash.
func (c *Client) PutMeta(ctx context.Context, ih metainfo.Hash, data []byte) error {
	return c.backend.PutBytes(ctx, c.metaKey(ih), data)
}

// GetMeta loads cached .torrent bytes if present.
func (c *Client) GetMeta(ctx context.Context, ih metainfo.Hash) ([]byte, error) {
	return c.backend.GetBytes(ctx, c.metaKey(ih), 0, 0)
}

// ListKeys lists storage object keys under prefix.
func (c *Client) ListKeys(ctx context.Context, prefix string) ([]string, error) {
	return c.backend.List(ctx, prefix)
}

// DeleteTorrent removes all objects under torrent/{hex}/.
func (c *Client) DeleteTorrent(ctx context.Context, ih metainfo.Hash) error {
	prefix := c.Prefix(ih) + "/"
	keys, err := c.backend.List(ctx, prefix)
	if err != nil {
		return err
	}
	var first error
	for _, k := range keys {
		if err := c.backend.Delete(ctx, k); err != nil && first == nil {
			first = err
		}
	}
	c.mu.Lock()
	delete(c.open, ih)
	c.mu.Unlock()
	return first
}

type torrent struct {
	client   *Client
	infoHash metainfo.Hash
	info     *metainfo.Info

	mu     sync.Mutex
	pieces map[int]*piece
}

func (t *torrent) impl() storage.TorrentImpl {
	return storage.TorrentImpl{
		PieceWithHash: func(p metainfo.Piece, _ g.Option[[]byte]) storage.PieceImpl {
			return t.piece(p)
		},
		Piece: func(p metainfo.Piece) storage.PieceImpl {
			return t.piece(p)
		},
		Close: func() error {
			return t.close()
		},
	}
}

func (t *torrent) piece(p metainfo.Piece) *piece {
	t.mu.Lock()
	defer t.mu.Unlock()
	idx := p.Index()
	if pe, ok := t.pieces[idx]; ok {
		return pe
	}
	pe := &piece{
		t:      t,
		index:  idx,
		length: p.Length(),
		key:    t.client.pieceKey(t.infoHash, idx),
	}
	t.pieces[idx] = pe
	return pe
}

func (t *torrent) close() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, pe := range t.pieces {
		pe.flushDirty()
	}
	return t.saveCompletion(context.Background())
}

func (t *torrent) pieceKey(index int) metainfo.PieceKey {
	return metainfo.PieceKey{InfoHash: t.infoHash, Index: index}
}

func (t *torrent) loadCompletion(ctx context.Context) error {
	data, err := t.client.backend.GetBytes(ctx, t.client.completionKey(t.infoHash), 0, 0)
	if err != nil || len(data) == 0 {
		return err
	}
	n := t.info.NumPieces()
	for i := 0; i < n && i/8 < len(data); i++ {
		if data[i/8]&(1<<uint(i%8)) != 0 {
			_ = t.client.pc.Set(t.pieceKey(i), true)
		}
	}
	return nil
}

func (t *torrent) saveCompletion(ctx context.Context) error {
	n := t.info.NumPieces()
	if n <= 0 {
		return nil
	}
	buf := make([]byte, (n+7)/8)
	for i := 0; i < n; i++ {
		c, err := t.client.pc.Get(t.pieceKey(i))
		if err != nil || !c.Ok || !c.Complete {
			continue
		}
		buf[i/8] |= 1 << uint(i%8)
	}
	return t.client.backend.PutBytes(ctx, t.client.completionKey(t.infoHash), buf)
}

type piece struct {
	t      *torrent
	index  int
	length int64
	key    string

	mu    sync.Mutex
	buf   []byte
	dirty bool
}

func (p *piece) ReadAt(b []byte, off int64) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buf != nil {
		return readFrom(p.buf, b, off)
	}
	ctx, cancel := p.t.client.ctx()
	defer cancel()
	data, err := p.t.client.backend.GetBytes(ctx, p.key, off, int64(len(b)))
	if err != nil {
		if off >= p.length {
			return 0, io.EOF
		}
		remain := p.length - off
		if int64(len(b)) > remain {
			b = b[:remain]
		}
		clear(b)
		return len(b), nil
	}
	n = copy(b, data)
	if n < len(b) {
		return n, io.EOF
	}
	return n, nil
}

func (p *piece) WriteAt(b []byte, off int64) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buf == nil {
		p.buf = make([]byte, p.length)
		ctx, cancel := p.t.client.ctx()
		existing, gerr := p.t.client.backend.GetBytes(ctx, p.key, 0, 0)
		cancel()
		if gerr == nil && len(existing) > 0 {
			copy(p.buf, existing)
		}
	}
	if off < 0 || off > p.length {
		return 0, io.ErrUnexpectedEOF
	}
	n = copy(p.buf[off:], b)
	p.dirty = true
	return n, nil
}

func (p *piece) MarkComplete() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buf == nil {
		p.buf = make([]byte, p.length)
	}
	ctx, cancel := p.t.client.ctx()
	defer cancel()
	if err := p.t.client.backend.PutBytes(ctx, p.key, p.buf); err != nil {
		return fmt.Errorf("meshstore put piece %d: %w", p.index, err)
	}
	p.dirty = false
	_ = p.t.client.pc.Set(p.t.pieceKey(p.index), true)
	_ = p.t.saveCompletion(ctx)
	return nil
}

func (p *piece) MarkNotComplete() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.t.client.pc.Set(p.t.pieceKey(p.index), false)
	ctx, cancel := p.t.client.ctx()
	defer cancel()
	_ = p.t.client.backend.Delete(ctx, p.key)
	_ = p.t.saveCompletion(ctx)
	return nil
}

func (p *piece) Completion() storage.Completion {
	c, err := p.t.client.pc.Get(p.t.pieceKey(p.index))
	if err != nil {
		return storage.Completion{Err: err}
	}
	return c
}

func (p *piece) flushDirty() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.dirty || p.buf == nil {
		return
	}
	ctx, cancel := p.t.client.ctx()
	defer cancel()
	if err := p.t.client.backend.PutBytes(ctx, p.key, p.buf); err != nil {
		slog.Warn("meshstore: flush dirty piece", "index", p.index, "error", err)
		return
	}
	p.dirty = false
}

func readFrom(src, dst []byte, off int64) (int, error) {
	if off < 0 {
		return 0, io.ErrUnexpectedEOF
	}
	if off >= int64(len(src)) {
		return 0, io.EOF
	}
	n := copy(dst, src[off:])
	if n < len(dst) {
		return n, io.EOF
	}
	return n, nil
}

// MemBackend is an in-process Backend for tests.
type MemBackend struct {
	mu   sync.Mutex
	data map[string][]byte
}

func NewMemBackend() *MemBackend {
	return &MemBackend{data: make(map[string][]byte)}
}

func (m *MemBackend) PutBytes(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = append([]byte(nil), data...)
	return nil
}

func (m *MemBackend) GetBytes(_ context.Context, key string, offset, length int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return nil, fmt.Errorf("not found: %s", key)
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= int64(len(b)) {
		return nil, io.EOF
	}
	end := int64(len(b))
	if length > 0 && offset+length < end {
		end = offset + length
	}
	return append([]byte(nil), b[offset:end]...), nil
}

func (m *MemBackend) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *MemBackend) Stat(_ context.Context, key string) (bool, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.data[key]
	if !ok {
		return false, 0, nil
	}
	return true, int64(len(b)), nil
}

func (m *MemBackend) List(_ context.Context, prefix string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for k := range m.data {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k)
		}
	}
	return out, nil
}

// Adapter wraps function pointers so a mesh StorageClient can satisfy Backend.
type Adapter struct {
	PutFn       func(ctx context.Context, key string, data []byte) error
	PutReaderFn func(ctx context.Context, key string, r io.Reader) error
	GetFn       func(ctx context.Context, key string, offset, length int64) ([]byte, error)
	DeleteFn    func(ctx context.Context, key string) error
	StatFn      func(ctx context.Context, key string) (found bool, size int64, err error)
	ListFn      func(ctx context.Context, prefix string) ([]string, error)
}

func (a Adapter) PutBytes(ctx context.Context, key string, data []byte) error {
	return a.PutFn(ctx, key, data)
}
func (a Adapter) PutReader(ctx context.Context, key string, r io.Reader) error {
	if a.PutReaderFn != nil {
		return a.PutReaderFn(ctx, key, r)
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return a.PutFn(ctx, key, data)
}
func (a Adapter) GetBytes(ctx context.Context, key string, offset, length int64) ([]byte, error) {
	return a.GetFn(ctx, key, offset, length)
}
func (a Adapter) Delete(ctx context.Context, key string) error { return a.DeleteFn(ctx, key) }
func (a Adapter) Stat(ctx context.Context, key string) (bool, int64, error) {
	return a.StatFn(ctx, key)
}
func (a Adapter) List(ctx context.Context, prefix string) ([]string, error) {
	return a.ListFn(ctx, prefix)
}
