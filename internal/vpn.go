package internal

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"

	"github.com/Muxcore-Media/core/pkg/contracts"
)

type parsedConfig struct {
	Interface struct {
		PrivateKey string
		Address    string
		Address6   string
		DNS        []string
		MTU        string
	}
	Peers []parsedPeer
}

type parsedPeer struct {
	PublicKey           string
	AllowedIPs          []string
	Endpoint            string
	PersistentKeepalive string
}

type vpnManager struct {
	mu           sync.RWMutex
	configFile   string
	ifaceName    string
	localIP      string
	peerEndpoint string // host:port from WireGuard Peer.Endpoint
	connected    bool
	connectedAt  time.Time
	killSwitch   bool
}

func newVPNManager() *vpnManager {
	return &vpnManager{}
}

func (v *vpnManager) start(configPath string, enableKill bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.connected {
		return fmt.Errorf("VPN already connected")
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("config not found: %s", configPath)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	cfg := parseConfig(string(raw))

	iface := deriveInterfaceName(configPath)

	slog.Info("vpn: bringing up wireguard", "interface", iface)
	if err := wgUp(iface, cfg); err != nil {
		return err
	}

	localIP := getInterfaceIP(iface)
	peerEP := ""
	if len(cfg.Peers) > 0 {
		peerEP = cfg.Peers[0].Endpoint
	}

	if enableKill {
		if err := applyKillSwitch(iface, peerEP); err != nil {
			slog.Warn("vpn: kill switch setup failed", "error", err)
		}
	}

	v.ifaceName = iface
	v.localIP = localIP
	v.peerEndpoint = peerEP
	v.configFile = configPath
	v.connected = true
	v.connectedAt = time.Now()
	v.killSwitch = enableKill

	slog.Info("vpn: connected", "interface", iface, "ip", localIP, "peer", peerEP)
	return nil
}

func (v *vpnManager) stop() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.connected {
		return nil
	}
	if v.killSwitch {
		removeKillSwitch(v.ifaceName, v.peerEndpoint)
	}
	teardownInterface(v.ifaceName)
	v.ifaceName = ""
	v.localIP = ""
	v.peerEndpoint = ""
	v.configFile = ""
	v.connected = false
	v.killSwitch = false
	slog.Info("vpn: disconnected")
	return nil
}

func (v *vpnManager) status() (connected bool, iface, localIP string, _ string, configFile string, connectedAt time.Time, killSwitch bool) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.connected, v.ifaceName, v.localIP, "", v.configFile, v.connectedAt, v.killSwitch
}

// ── WireGuard (wgctrl + shell ip commands) ───────────────────

func wgUp(iface string, cfg parsedConfig) error {
	_ = exec.Command("ip", "link", "delete", iface).Run()

	run := func(args ...string) error {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w\n%s", args[0], err, string(out))
		}
		return nil
	}
	runIgnore := func(args ...string) {
		exec.Command(args[0], args[1:]...).Run()
	}

	// Create interface
	if err := run("ip", "link", "add", iface, "type", "wireguard"); err != nil {
		return err
	}

	// Add addresses
	if cfg.Interface.Address != "" {
		run("ip", "address", "add", cfg.Interface.Address, "dev", iface)
	}
	if cfg.Interface.Address6 != "" {
		run("ip", "-6", "address", "add", cfg.Interface.Address6, "dev", iface)
	}

	// Configure via wgctrl (Go netlink – more reliable than `wg set`)
	wgc, err := wgctrl.New()
	if err != nil {
		return fmt.Errorf("wgctrl: %w", err)
	}
	defer wgc.Close()

	privKey, err := wgtypes.ParseKey(cfg.Interface.PrivateKey)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}

	peers := make([]wgtypes.PeerConfig, 0, len(cfg.Peers))
	for _, p := range cfg.Peers {
		pubKey, err := wgtypes.ParseKey(p.PublicKey)
		if err != nil {
			return fmt.Errorf("parse public key: %w", err)
		}
		host, portStr, err := net.SplitHostPort(p.Endpoint)
		if err != nil {
			return fmt.Errorf("parse endpoint: %w", err)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("invalid endpoint IP: %s", host)
		}
		port, _ := strconv.Atoi(portStr)

		var keepalive *time.Duration
		if p.PersistentKeepalive != "" {
			sec, _ := strconv.Atoi(p.PersistentKeepalive)
			if sec > 0 {
				d := time.Duration(sec) * time.Second
				keepalive = &d
			}
		}

		allowed := []net.IPNet{
			{IP: net.IPv4(0, 0, 0, 0), Mask: net.CIDRMask(0, 32)},
			{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
		}

		peers = append(peers, wgtypes.PeerConfig{
			PublicKey:                   pubKey,
			Endpoint:                    &net.UDPAddr{IP: ip, Port: port},
			AllowedIPs:                  allowed,
			PersistentKeepaliveInterval: keepalive,
			ReplaceAllowedIPs:           true,
		})
	}

	fwmark := 51820
	if err := wgc.ConfigureDevice(iface, wgtypes.Config{
		PrivateKey:   &privKey,
		FirewallMark: &fwmark,
		ReplacePeers: true,
		Peers:        peers,
	}); err != nil {
		return fmt.Errorf("configure device: %w", err)
	}

	// Bring up and set routing
	run("ip", "link", "set", "mtu", "1420", "up", "dev", iface)
	run("ip", "route", "add", "0.0.0.0/0", "dev", iface, "table", "51820")
	run("ip", "rule", "add", "not", "fwmark", "51820", "table", "51820")
	run("ip", "rule", "add", "table", "main", "suppress_prefixlength", "0")
	if cfg.Interface.Address6 != "" {
		run("ip", "-6", "route", "add", "::/0", "dev", iface, "table", "51820")
		run("ip", "-6", "rule", "add", "not", "fwmark", "51820", "table", "51820")
		run("ip", "-6", "rule", "add", "table", "main", "suppress_prefixlength", "0")
	}

	// Remove stale duplicate rules
	runIgnore("ip", "rule", "del", "not", "fwmark", "51820", "table", "51820")
	run("ip", "rule", "add", "not", "fwmark", "51820", "table", "51820")

	// Set DNS
	_ = exec.Command("sh", "-c", "echo 'nameserver 1.1.1.1\nnameserver 1.0.0.1' > /etc/resolv.conf").Run()

	// Wait for handshake using wgctrl (more precise)
	if err := waitForHandshakeWGCTL(wgc, iface, 90*time.Second); err != nil {
		teardownInterface(iface)
		return err
	}

	return nil
}

func teardownInterface(iface string) {
	_ = exec.Command("ip", "link", "delete", iface).Run()
}

// ── Kill switch (iptables – no policy changes, host-safe) ───

func applyKillSwitch(iface, peerEndpoint string) error {
	_ = exec.Command("sh", "-c",
		"echo 'nameserver 1.1.1.1\nnameserver 1.0.0.1' > /etc/resolv.conf",
	).Run()
	rules := [][]string{
		{"--append", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
		{"--append", "OUTPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	if host, port, ok := splitEndpoint(peerEndpoint); ok {
		rules = append(rules, []string{"--append", "OUTPUT", "-d", host, "-p", "udp", "--dport", port, "-j", "ACCEPT"})
		rules = append(rules, []string{"--append", "OUTPUT", "-d", host, "-p", "tcp", "--dport", port, "-j", "ACCEPT"})
	}
	if iface != "" {
		rules = append(rules, []string{"--append", "OUTPUT", "-o", iface, "-j", "ACCEPT"})
	}
	rules = append(rules, []string{"--append", "OUTPUT", "-j", "DROP"})
	return iptables(rules)
}

func removeKillSwitch(iface, peerEndpoint string) {
	rules := [][]string{
		{"--delete", "OUTPUT", "-o", "lo", "-j", "ACCEPT"},
		{"--delete", "OUTPUT", "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED", "-j", "ACCEPT"},
	}
	if host, port, ok := splitEndpoint(peerEndpoint); ok {
		rules = append(rules, []string{"--delete", "OUTPUT", "-d", host, "-p", "udp", "--dport", port, "-j", "ACCEPT"})
		rules = append(rules, []string{"--delete", "OUTPUT", "-d", host, "-p", "tcp", "--dport", port, "-j", "ACCEPT"})
	}
	if iface != "" {
		rules = append(rules, []string{"--delete", "OUTPUT", "-o", iface, "-j", "ACCEPT"})
	}
	rules = append(rules, []string{"--delete", "OUTPUT", "-j", "DROP"})
	iptables(rules)
}

func splitEndpoint(endpoint string) (host, port string, ok bool) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", "", false
	}
	h, p, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", "", false
	}
	h = strings.Trim(h, "[]")
	if h == "" || p == "" {
		return "", "", false
	}
	return h, p, true
}

func iptables(rules [][]string) error {
	for _, r := range rules {
		cmd := exec.Command("iptables", r...)
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Warn("vpn: iptables", "rule", r, "error", err, "output", string(out))
		}
	}
	return nil
}

// ── Config parsing ───────────────────────────────────────────

func parseConfig(raw string) parsedConfig {
	var cfg parsedConfig
	section := ""
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line[1 : len(line)-1])
			continue
		}
		key, val, ok := cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)

		switch section {
		case "interface":
			switch key {
			case "privatekey":
				cfg.Interface.PrivateKey = val
			case "address":
				for _, p := range splitCSV(val) {
					if strings.Contains(p, ":") {
						cfg.Interface.Address6 = p
					} else {
						cfg.Interface.Address = p
					}
				}
			case "dns":
				cfg.Interface.DNS = splitCSV(val)
			}
		case "peer":
			if len(cfg.Peers) == 0 {
				cfg.Peers = append(cfg.Peers, parsedPeer{})
			}
			p := &cfg.Peers[len(cfg.Peers)-1]
			switch key {
			case "publickey":
				p.PublicKey = val
			case "allowedips":
				p.AllowedIPs = splitCSV(val)
			case "endpoint":
				p.Endpoint = val
			case "persistentkeepalive":
				p.PersistentKeepalive = val
			}
		}
	}
	return cfg
}

func cut(s, sep string) (before, after string, found bool) {
	i := strings.Index(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ── Helpers ──────────────────────────────────────────────────

func deriveInterfaceName(configPath string) string {
	base := filepath.Base(configPath)
	ext := filepath.Ext(base)
	if ext != "" {
		return strings.TrimSuffix(base, ext)
	}
	return base
}

func getInterfaceIP(iface string) string {
	i, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, err := i.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return ""
}

func waitForHandshakeWGCTL(wgc *wgctrl.Client, iface string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		dev, err := wgc.Device(iface)
		if err == nil && len(dev.Peers) > 0 {
			hs := dev.Peers[0].LastHandshakeTime
			if !hs.IsZero() && time.Since(hs) < 180*time.Second {
				return nil
			}
		}
		<-ticker.C
	}
	return errors.New("no wireguard handshake completed within timeout")
}

func hasNATPMPEnabled(cfg string) bool {
	for _, line := range strings.Split(cfg, "\n") {
		if strings.Contains(line, "NAT-PMP") && strings.Contains(line, "on") {
			return true
		}
	}
	return false
}

var _ contracts.Module = (*Module)(nil)
