package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/anacrolix/torrent/metainfo"
)

const torrentSessionFile = ".muxcore-torrents.json"

type persistedTorrent struct {
	ID       string `json:"id"`
	URI      string `json:"uri"`
	SavePath string `json:"save_path"`
	Label    string `json:"label,omitempty"`
	Paused   bool   `json:"paused,omitempty"`
	Status   string `json:"status,omitempty"`
	Name     string `json:"name,omitempty"`
	InfoHash string `json:"info_hash,omitempty"`
	Error    string `json:"error,omitempty"`
}

type torrentSession struct {
	Torrents []persistedTorrent `json:"torrents"`
}

func (m *Module) sessionPath() string {
	return filepath.Join(m.dlDir, torrentSessionFile)
}

func isPersistableStatus(st string) bool {
	switch st {
	case "queued", "downloading", "paused", "seeding", "completed", "error":
		return true
	default:
		return false
	}
}

func isRestorableRunningStatus(st string) bool {
	switch st {
	case "queued", "downloading", "paused", "":
		return true
	default:
		return false
	}
}

// meshSessionStore persists torrent/sessions.json in mesh mode (tests inject a mock).
type meshSessionStore interface {
	PutBytes(ctx context.Context, key string, data []byte) error
	GetBytes(ctx context.Context, key string, offset, length int64) ([]byte, error)
}

func (m *Module) meshSessionBackend() meshSessionStore {
	if m.meshSessions != nil {
		return m.meshSessions
	}
	m.mu.RLock()
	mc := m.mc
	m.mu.RUnlock()
	if mc != nil {
		return mc.Storage
	}
	return nil
}

func (m *Module) persistActiveTorrents() {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()

	m.mu.RLock()
	if m.stopping {
		m.mu.RUnlock()
		return
	}
	recs := make([]persistedTorrent, 0, len(m.torrents))
	for _, th := range m.torrents {
		th.mu.RLock()
		st, uri, id, save, label := th.Status, th.URI, th.ID, th.SavePath, th.Label
		name, hash, errStr := th.Name, th.InfoHash, th.ErrorStr
		th.mu.RUnlock()
		if uri == "" || id == "" || !isPersistableStatus(st) {
			continue
		}
		recs = append(recs, persistedTorrent{
			ID:       id,
			URI:      uri,
			SavePath: save,
			Label:    label,
			Paused:   st == "paused",
			Status:   st,
			Name:     name,
			InfoHash: hash,
			Error:    errStr,
		})
	}
	m.mu.RUnlock()

	data, err := json.MarshalIndent(torrentSession{Torrents: recs}, "", "  ")
	if err != nil {
		slog.Warn("persist torrents marshal", "error", err)
		return
	}
	if storageMode() == "mesh" {
		store := m.meshSessionBackend()
		if store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := store.PutBytes(ctx, "torrent/sessions.json", data); err != nil {
				slog.Warn("persist torrents to mesh storage", "error", err)
			}
			return
		}
	}
	path := m.sessionPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		slog.Warn("persist torrents mkdir", "error", err)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("persist torrents write", "error", err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		slog.Warn("persist torrents rename", "error", err)
	}
}

func (m *Module) restorePersistedTorrents() {
	var data []byte
	var err error
	if storageMode() == "mesh" {
		if store := m.meshSessionBackend(); store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			data, err = store.GetBytes(ctx, "torrent/sessions.json", 0, 0)
			cancel()
			if err != nil {
				slog.Debug("no mesh torrent session", "error", err)
				data = nil
			}
		}
	}
	if data == nil {
		data, err = os.ReadFile(m.sessionPath())
		if err != nil {
			if !os.IsNotExist(err) {
				slog.Warn("read torrent session", "error", err)
			}
			return
		}
	}
	var sess torrentSession
	if err := json.Unmarshal(data, &sess); err != nil {
		slog.Warn("parse torrent session", "error", err)
		return
	}
	restored := 0
	for _, rec := range sess.Torrents {
		if rec.URI == "" || rec.ID == "" {
			continue
		}
		if _, err := classifyURI(rec.URI); err != nil {
			slog.Warn("skip persisted torrent", "id", rec.ID, "error", err)
			continue
		}
		m.mu.RLock()
		_, exists := m.torrents[rec.ID]
		m.mu.RUnlock()
		if exists {
			continue
		}
		if hash := strings.ToLower(parseInfoHash(rec.URI)); hash != "" && m.findHandleByInfoHash(hash) != nil {
			continue
		}
		savePath := rec.SavePath
		if !strings.HasPrefix(savePath, "storage://") {
			savePath = resolveSavePath(m.dlDir, rec.SavePath)
			if isRestorableRunningStatus(rec.Status) {
				if err := mkdirAllLocal(savePath, 0755); err != nil {
					slog.Warn("restore save path", "id", rec.ID, "error", err)
					continue
				}
			}
		}
		if rec.Status == "completed" || rec.Status == "error" || rec.Status == "seeding" {
			m.restoreHistoryTorrent(rec, savePath)
			restored++
			continue
		}
		m.spawnTorrent(rec.ID, rec.URI, savePath, rec.Label, rec.Paused)
		restored++
	}
	slog.Info("restored torrents", "count", restored)
}

// repairIncompleteAssemblies reassembles media for torrents that finished piece
// download but never wrote files/ objects (e.g. prior MaxObjectSize limits).
func (m *Module) repairIncompleteAssemblies() {
	if storageMode() != "mesh" || m.mesh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	keys, err := m.mesh.ListKeys(ctx, "torrent/")
	if err != nil {
		slog.Warn("repair assemble list", "error", err)
		return
	}
	type ihStat struct {
		completion bool
		media      bool
	}
	stats := map[string]*ihStat{}
	for _, k := range keys {
		// Keys may be namespaced: {module}/torrent/{ih}/...
		parts := strings.Split(k, "/")
		ti := -1
		for i, p := range parts {
			if p == "torrent" {
				ti = i
				break
			}
		}
		if ti < 0 || ti+2 >= len(parts) {
			continue
		}
		ih := strings.ToLower(parts[ti+1])
		if len(ih) != 40 {
			continue
		}
		st := stats[ih]
		if st == nil {
			st = &ihStat{}
			stats[ih] = st
		}
		rest := parts[ti+2]
		if rest == "completion" {
			st.completion = true
		}
		if rest == "files" {
			base := strings.ToLower(parts[len(parts)-1])
			if strings.HasSuffix(base, ".mkv") || strings.HasSuffix(base, ".mp4") ||
				strings.HasSuffix(base, ".avi") || strings.HasSuffix(base, ".m4v") {
				st.media = true
			}
		}
	}
	slog.Info("repair assemble scan", "keys", len(keys), "torrents", len(stats))
	for ihHex, st := range stats {
		if !st.completion || st.media {
			continue
		}
		slog.Info("repair: assembling torrent missing media files", "infohash", ihHex)
		if err := m.repairAssembleOne(ctx, ihHex); err != nil {
			slog.Warn("repair assemble", "infohash", ihHex, "error", err)
		}
	}
}

func (m *Module) repairAssembleOne(ctx context.Context, ihHex string) error {
	ih, ok := decodeInfoHash(ihHex)
	if !ok {
		return fmt.Errorf("bad infohash")
	}
	meta, err := m.mesh.GetMeta(ctx, ih)
	if err != nil || len(meta) == 0 {
		meta, err = fetchTorrentMetainfoWith(ctx, ihHex, defaultMetainfoHTTPClient(), m.mesh)
		if err != nil {
			return fmt.Errorf("metainfo: %w", err)
		}
		_ = m.mesh.PutMeta(ctx, ih, meta)
	}
	mi, err := metainfo.Load(bytes.NewReader(meta))
	if err != nil {
		return fmt.Errorf("parse torrent: %w", err)
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return fmt.Errorf("unmarshal info: %w", err)
	}
	if got := mi.HashInfoBytes(); got != ih {
		return fmt.Errorf("infohash mismatch: got %s want %s", got.HexString(), ihHex)
	}
	assembled, err := m.mesh.AssembleFiles(ctx, &info, ih)
	if err != nil && len(assembled) == 0 {
		return err
	}
	if err != nil {
		slog.Warn("repair assemble partial", "infohash", ihHex, "files", len(assembled), "error", err)
	} else {
		slog.Info("repair assembled files", "infohash", ihHex, "files", len(assembled))
	}
	return nil
}

func (m *Module) restoreHistoryTorrent(rec persistedTorrent, savePath string) {
	status := rec.Status
	if status == "seeding" {
		status = "completed"
	}
	th := &torrentHandle{
		ID:       rec.ID,
		URI:      rec.URI,
		Name:     rec.Name,
		InfoHash: rec.InfoHash,
		Label:    rec.Label,
		SavePath: savePath,
		AddedAt:  time.Now(),
		Status:   status,
		ErrorStr: rec.Error,
		Files:    []fileInfo{},
		done:     make(chan struct{}),
	}
	if th.Name == "" {
		th.Name = parseMagnetName(rec.URI)
	}
	if th.InfoHash == "" {
		th.InfoHash = parseInfoHash(rec.URI)
	}
	close(th.done)
	m.mu.Lock()
	m.torrents[th.ID] = th
	m.mu.Unlock()
}

func (m *Module) spawnTorrent(id, uri, savePath, label string, paused bool) *torrentHandle {
	if id == "" {
		id = fmt.Sprintf("torrent_%d", time.Now().UnixNano())
	}
	status := "queued"
	if paused {
		status = "paused"
	}
	runCtx, cancel := context.WithCancel(context.Background())
	th := &torrentHandle{
		ID:       id,
		URI:      uri,
		Name:     parseMagnetName(uri),
		InfoHash: parseInfoHash(uri),
		Label:    label,
		SavePath: savePath,
		AddedAt:  time.Now(),
		Status:   status,
		Files:    []fileInfo{},
		cancel:   cancel,
		done:     make(chan struct{}),
		resumeCh: make(chan struct{}, 1),
	}
	m.mu.Lock()
	m.torrents[th.ID] = th
	m.mu.Unlock()
	go m.runTorrent(runCtx, th, uri, paused)
	return th
}
