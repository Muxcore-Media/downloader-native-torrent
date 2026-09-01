package internal

import (
	"context"
	"fmt"
	"log/slog"
)

type engineRebinder interface {
	rebind(listenHost string, listenPort int) error
	setListenPort(port int)
	currentListenHost() string
}

func (e *anacrolixEngine) setListenPort(port int) { e.listenPort = port }

func (e *anacrolixEngine) currentListenHost() string { return e.listenHost }

// rebindTrackingEngine wraps fakeEngine to exercise VPN/NAT-PMP rebind migration in tests.
type rebindTrackingEngine struct {
	fakeEngine
	rebinds int
}

func (e *rebindTrackingEngine) rebind(listenHost string, listenPort int) error {
	e.rebinds++
	e.listenHost = listenHost
	if listenPort > 0 {
		e.listenPort = listenPort
	}
	return nil
}

func (e *rebindTrackingEngine) setListenPort(port int) { e.listenPort = port }

func (e *rebindTrackingEngine) currentListenHost() string { return e.listenHost }

func (e *fakeEngine) rebind(listenHost string, listenPort int) error {
	e.listenHost = listenHost
	if listenPort > 0 {
		e.listenPort = listenPort
	}
	return nil
}

func (e *fakeEngine) setListenPort(port int) { e.listenPort = port }

func (e *fakeEngine) currentListenHost() string { return e.listenHost }

func (m *Module) rebindEngine(host string, port int) error {
	eng, ok := m.engine.(engineRebinder)
	if !ok {
		return nil
	}
	if err := eng.rebind(host, port); err != nil {
		return err
	}
	eng.setListenPort(port)
	m.migrateSessionsAfterRebind(context.Background())
	return nil
}

func (m *Module) migrateSessionsAfterRebind(ctx context.Context) {
	m.mu.RLock()
	type snap struct {
		th     *torrentHandle
		uri    string
		save   string
		paused bool
		status string
	}
	var snaps []snap
	for _, th := range m.torrents {
		th.mu.RLock()
		if th.session == nil {
			th.mu.RUnlock()
			continue
		}
		st := th.Status
		if st == "completed" || st == "error" {
			th.mu.RUnlock()
			continue
		}
		snaps = append(snaps, snap{
			th: th, uri: th.URI, save: th.SavePath,
			paused: st == "paused", status: st,
		})
		th.mu.RUnlock()
	}
	engine := m.engine
	m.mu.RUnlock()
	if engine == nil || len(snaps) == 0 {
		return
	}

	for _, s := range snaps {
		s.th.mu.Lock()
		old := s.th.session
		s.th.session = nil
		s.th.mu.Unlock()
		if old != nil {
			old.Drop()
		}
		session, err := engine.AddURI(ctx, s.uri, s.save)
		if err != nil {
			slog.Warn("rebind: re-add torrent failed", "id", s.th.ID, "error", err)
			m.failTorrent(s.th, err)
			continue
		}
		infoCtx, cancel := context.WithTimeout(ctx, m.infoTimeout)
		err = session.WaitInfo(infoCtx)
		cancel()
		if err != nil {
			session.Drop()
			slog.Warn("rebind: wait metadata failed", "id", s.th.ID, "error", err)
			m.failTorrent(s.th, fmt.Errorf("rebind wait metadata: %w", err))
			continue
		}
		s.th.mu.Lock()
		s.th.session = session
		s.th.mu.Unlock()
		m.syncHandleFromTorrent(s.th, session)
		switch {
		case s.paused:
			session.PauseDownload()
			s.th.mu.Lock()
			s.th.Status = "paused"
			s.th.mu.Unlock()
		case s.status == "downloading" || s.status == "queued":
			m.beginDownload(session)
			s.th.mu.Lock()
			s.th.Status = "downloading"
			s.th.mu.Unlock()
		}
		slog.Info("rebind: restored torrent session", "id", s.th.ID)
	}
	m.persistActiveTorrents()
}
