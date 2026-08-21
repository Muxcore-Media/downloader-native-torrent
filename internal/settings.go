package internal

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/Muxcore-Media/core/pkg/contracts"
	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	"google.golang.org/grpc"
)

func (m *Module) settingsDefs() []contracts.SettingDef {
	m.mu.RLock()
	dlDir := m.dlDir
	listenPort := m.listenPort
	wgConf := m.wgConfPath
	ks := m.wgKillSwitch
	filePri := m.filePriorityMode
	dht := m.enableDHT
	pex := m.enablePEX
	m.mu.RUnlock()
	return []contracts.SettingDef{
		{
			Key:         "download_path",
			Label:       "Download Directory",
			Type:        contracts.SettingTypeString,
			Default:     "/var/lib/downloader-native-torrent/downloads",
			Value:       dlDir,
			Description: "Local save dir when DOWNLOAD_STORAGE=local (ignored for mesh piece store)",
			Required:    false,
			Group:       "Downloads",
		},
		{
			Key:         "recheck_torrent",
			Label:       "Recheck torrent id",
			Type:        contracts.SettingTypeString,
			Default:     "",
			Value:       "",
			Description: "Write a torrent id to re-hash pieces against storage/disk (one-shot)",
			Required:    false,
			Group:       "Downloads",
		},
		{
			Key:         "torrent_file_priority",
			Label:       "Per-torrent file priority",
			Type:        contracts.SettingTypeString,
			Default:     "",
			Value:       "",
			Description: "Format: <torrent_id>:<all|episodes|season_packs> — apply file selection to one torrent",
			Required:    false,
			Group:       "Downloads",
		},
		{
			Key:         "listen_port",
			Label:       "Torrent Listen Port",
			Type:        contracts.SettingTypeInt,
			Default:     "6881",
			Value:       strconv.Itoa(listenPort),
			Description: "BitTorrent listen port",
			Required:    false,
			Group:       "Downloads",
		},
		{
			Key:         "file_priority",
			Label:       "File selection priority",
			Type:        contracts.SettingTypeString,
			Default:     filePriorityAll,
			Value:       filePri,
			Description: "all | episodes (prefer SxxEyy, skip samples/packs when episodes exist) | season_packs (prefer season packs)",
			Required:    false,
			Group:       "Downloads",
		},
		{
			Key:         "enable_dht",
			Label:       "Enable DHT",
			Type:        contracts.SettingTypeBool,
			Default:     "true",
			Value:       strconv.FormatBool(dht),
			Description: "BitTorrent DHT peer discovery (applies on module restart)",
			Required:    false,
			Group:       "BitTorrent",
		},
		{
			Key:         "enable_pex",
			Label:       "Enable PEX",
			Type:        contracts.SettingTypeBool,
			Default:     "true",
			Value:       strconv.FormatBool(pex),
			Description: "Peer exchange (applies on module restart)",
			Required:    false,
			Group:       "BitTorrent",
		},
		{
			Key:         "wg_conf",
			Label:       "WireGuard Config Path",
			Type:        contracts.SettingTypeString,
			Default:     "",
			Value:       wgConf,
			Description: "Path to WireGuard configuration file",
			Required:    false,
			Group:       "VPN",
		},
		{
			Key:         "wg_kill_switch",
			Label:       "VPN Kill Switch",
			Type:        contracts.SettingTypeBool,
			Default:     "false",
			Value:       strconv.FormatBool(ks),
			Description: "Block non-VPN traffic when WireGuard is active",
			Required:    false,
			Group:       "VPN",
		},
	}
}

func (m *Module) updateSetting(key, value string) error {
	switch key {
	case "download_path", "DOWNLOAD_DIR":
		value = strings.TrimSpace(value)
		if value == "" {
			return fmt.Errorf("download_path required")
		}
		m.mu.Lock()
		m.dlDir = value
		m.mu.Unlock()
		return nil
	case "recheck_torrent":
		return m.recheckTorrent(strings.TrimSpace(value))
	case "torrent_file_priority":
		return m.applyTorrentFilePriority(strings.TrimSpace(value))
	case "listen_port", "TORRENT_LISTEN_PORT":
		p, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || p <= 0 {
			return fmt.Errorf("invalid listen_port")
		}
		m.mu.Lock()
		m.listenPort = p
		m.mu.Unlock()
		return nil
	case "file_priority", "TORRENT_FILE_PRIORITY":
		m.mu.Lock()
		m.filePriorityMode = normalizeFilePriorityMode(value)
		m.mu.Unlock()
		return nil
	case "enable_dht", "TORRENT_ENABLE_DHT":
		m.mu.Lock()
		m.enableDHT = envTruthyBool(value)
		m.mu.Unlock()
		return nil
	case "enable_pex", "TORRENT_ENABLE_PEX":
		m.mu.Lock()
		m.enablePEX = envTruthyBool(value)
		m.mu.Unlock()
		return nil
	case "wg_conf", "WG_CONF":
		m.mu.Lock()
		m.wgConfPath = strings.TrimSpace(value)
		m.mu.Unlock()
		return nil
	case "wg_kill_switch", "WG_KILL_SWITCH":
		m.mu.Lock()
		m.wgKillSwitch = value == "true" || value == "1" || value == "on"
		m.mu.Unlock()
		return nil
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
}

func (m *Module) recheckTorrent(id string) error {
	if id == "" {
		return fmt.Errorf("torrent id required")
	}
	m.mu.RLock()
	th := m.torrents[id]
	m.mu.RUnlock()
	if th == nil {
		return fmt.Errorf("torrent not found: %s", id)
	}
	th.mu.RLock()
	session := th.session
	th.mu.RUnlock()
	at, ok := session.(*anacrolixTorrent)
	if !ok || at == nil || at.t == nil {
		return fmt.Errorf("recheck requires live anacrolix session")
	}
	return at.t.VerifyData()
}

func (m *Module) applyTorrentFilePriority(spec string) error {
	parts := strings.SplitN(spec, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf("want <torrent_id>:<all|episodes|season_packs>")
	}
	id, mode := parts[0], normalizeFilePriorityMode(parts[1])
	m.mu.RLock()
	th := m.torrents[id]
	m.mu.RUnlock()
	if th == nil {
		return fmt.Errorf("torrent not found: %s", id)
	}
	th.mu.RLock()
	session := th.session
	th.mu.RUnlock()
	if applier, ok := session.(filePriorityApplier); ok {
		applier.ApplyFilePriorities(mode)
		return nil
	}
	return fmt.Errorf("torrent does not support file priority")
}

func (m *Module) Settings() []contracts.SettingDef {
	return m.settingsDefs()
}

func (m *Module) UpdateSetting(key, value string) error {
	return m.updateSetting(key, value)
}

func (m *Module) registerSettingsMesh(srv *grpc.Server) {
	modulesdk.RegisterSettings(srv, m.id, m)
}
