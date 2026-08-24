package internal

import (
	"fmt"
	"os"
	"strings"
)

func envFlagTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func liveEngineRequiresVPN(engine string) bool {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "", "fixture", "fake":
		return false
	default:
		return true
	}
}

func validateWGConfReadable(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("WG_CONF"))
	}
	if path == "" {
		return fmt.Errorf("WG_CONF is required for live torrent engine (set WG_CONF, or DOWNLOADER_ENGINE=fixture)")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("WG_CONF missing or unreadable (%s): %w", path, err)
	}
	if fi.IsDir() {
		return fmt.Errorf("WG_CONF is a directory, not a file: %s", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("WG_CONF unreadable (%s): %w", path, err)
	}
	_ = f.Close()
	return nil
}

func enforceLiveDownloaderVPN(wgConf, engine string) error {
	if !liveEngineRequiresVPN(engine) {
		return nil
	}
	if envFlagTruthy(os.Getenv("DOWNLOADER_REQUIRE_VPN")) || liveEngineRequiresVPN(engine) {
		return validateWGConfReadable(wgConf)
	}
	return nil
}
