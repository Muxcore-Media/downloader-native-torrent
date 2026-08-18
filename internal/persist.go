package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const torrentSessionFile = ".muxcore-torrents.json"

type persistedTorrent struct {
	ID       string `json:"id"`
	URI      string `json:"uri"`
	SavePath string `json:"save_path"`
	Label    string `json:"label,omitempty"`
	Paused   bool   `json:"paused,omitempty"`
}

type torrentSession struct {
	Torrents []persistedTorrent `json:"torrents"`
}

func (m *Module) sessionPath() string {
	return filepath.Join(m.dlDir, torrentSessionFile)
}

func isPersistableStatus(st string) bool {
	switch st {
	case "queued", "downloading", "paused":
		return true
	default:
		return false
	}
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
		})
	}
	m.mu.RUnlock()

	data, err := json.MarshalIndent(torrentSession{Torrents: recs}, "", "  ")
	if err != nil {
		slog.Warn("persist torrents marshal", "error", err)
		return
	}
	path := m.sessionPath()
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
	data, err := os.ReadFile(m.sessionPath())
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("read torrent session", "error", err)
		}
		return
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
		savePath := resolveSavePath(m.dlDir, rec.SavePath)
		if err := os.MkdirAll(savePath, 0755); err != nil {
			slog.Warn("restore save path", "id", rec.ID, "error", err)
			continue
		}
		m.spawnTorrent(rec.ID, rec.URI, savePath, rec.Label, rec.Paused)
		restored++
	}
	slog.Info("restored torrents", "count", restored)
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
