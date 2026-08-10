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
			Description: "Directory where torrents are saved",
			Required:    true,
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

func (m *Module) registerSettingsMesh(srv *grpc.Server) {
	modulesdk.RegisterMeshHandler(srv, m.id, modulesdk.SettingsHandler{
		List:   m.settingsDefs,
		Update: m.updateSetting,
	})
}
