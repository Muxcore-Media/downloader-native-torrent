package internal

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"
)

func envFlagTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// Engine modes for DOWNLOADER_ENGINE (NFR-SEC-010, FR-INS-003, ADR-0008).
const (
	engineModeFixture = "fixture"
	engineModeLive    = "live"
)

// resolveEngineMode maps DOWNLOADER_ENGINE to an engine mode. Unset/empty selects
// the fixture engine; "live" is the only way to reach the network engine.
// Anything else is an error (fail closed, never a silent fallback).
func resolveEngineMode(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "fixture", "fake":
		return engineModeFixture, nil
	case "live":
		return engineModeLive, nil
	case "anacrolix":
		// Deprecated alias kept so existing operator .env files keep working;
		// still subject to the same VPN requirement as "live".
		slog.Warn("DOWNLOADER_ENGINE=anacrolix is deprecated; use DOWNLOADER_ENGINE=live")
		return engineModeLive, nil
	default:
		return "", fmt.Errorf("unknown DOWNLOADER_ENGINE %q (want unset/fixture or live)", v)
	}
}

// liveEngineRequiresVPN reports whether the engine value selects live acquisition.
// Unknown values are treated as live (requiring VPN); resolveEngineMode rejects them.
func liveEngineRequiresVPN(engine string) bool {
	mode, err := resolveEngineMode(engine)
	return err != nil || mode == engineModeLive
}

func enforceLiveDownloaderVPN(wgConfPath, engine string) error {
	mode, err := resolveEngineMode(engine)
	if err != nil {
		return err
	}
	if mode != engineModeLive {
		return nil
	}
	path := strings.TrimSpace(wgConfPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("WG_CONF"))
	}
	if path == "" {
		return fmt.Errorf("DOWNLOADER_ENGINE=live requires WG_CONF (WireGuard config); refusing to start live acquisition without VPN")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("WG_CONF %q: %w", path, err)
	}
	return nil
}

func killSwitchAllowedOnHost() bool {
	if envFlagTruthy(os.Getenv("WG_KILL_SWITCH_ALLOW")) {
		return true
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("WG_KILL_SWITCH_SCOPE"))) {
	case "container", "netns", "namespace":
		return true
	}
	return false
}

func validateKillSwitch(enable bool) error {
	if !enable {
		return nil
	}
	if killSwitchAllowedOnHost() {
		return nil
	}
	return fmt.Errorf("WG_KILL_SWITCH refused on shared host (vault): set WG_KILL_SWITCH_ALLOW=true after reviewing risk, run in a container, or set WG_KILL_SWITCH_SCOPE=container")
}

func (m *Module) engineNeedsVPN() bool {
	if !liveEngineRequiresVPN(os.Getenv("DOWNLOADER_ENGINE")) {
		return false
	}
	m.mu.RLock()
	eng := m.engine
	m.mu.RUnlock()
	if eng == nil {
		return true
	}
	switch eng.(type) {
	case *fakeEngine, *fixtureEngine, *rebindTrackingEngine:
		return false
	default:
		return true
	}
}

func (m *Module) requireVPNReadyNow() error {
	if !m.engineNeedsVPN() {
		return nil
	}
	if m.wgConfPath == "" && os.Getenv("WG_CONF") == "" {
		return fmt.Errorf("WG_CONF required for live torrent engine")
	}
	connected, _, _, _, _, _, _ := m.vpn.status()
	if !connected {
		return fmt.Errorf("wireguard not connected")
	}
	return nil
}

func (m *Module) requireVPNConnected(ctx context.Context) error {
	if !m.engineNeedsVPN() {
		return nil
	}
	if m.wgConfPath == "" && os.Getenv("WG_CONF") == "" {
		return fmt.Errorf("WG_CONF required for live torrent engine")
	}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		connected, _, _, _, _, _, _ := m.vpn.status()
		if connected {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("wireguard not connected")
}
