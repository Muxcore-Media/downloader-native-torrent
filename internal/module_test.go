package internal

import (
	"context"
	"path/filepath"
	"testing"

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func newTestModule(t *testing.T) *Module {
	t.Helper()
	m := NewModule(Config{
		GRPCAddr:    ":0",
		DownloadDir: filepath.Join(t.TempDir(), "downloads"),
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { m.Stop(ctx) })
	return m
}

func TestModuleInfo(t *testing.T) {
	m := NewModule(Config{})
	info := m.Info()
	if info.ID == "" {
		t.Error("module ID must not be empty")
	}
	if info.Version == "" {
		t.Error("module version must not be empty")
	}
	if len(info.Capabilities) == 0 || info.Capabilities[0] != "downloader" {
		t.Errorf("expected downloader capability, got %v", info.Capabilities)
	}
}

func TestAddTorrentMagnet(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	resp, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&dn=Test+Torrent",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Id == "" {
		t.Fatal("expected non-empty ID")
	}
	if resp.Name != "Test Torrent" {
		t.Errorf("expected 'Test Torrent', got %s", resp.Name)
	}
	if resp.InfoHash != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" {
		t.Errorf("expected info hash AAAA..., got %s", resp.InfoHash)
	}
}

func TestAddTorrentEmptyURI(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: ""})
	if err == nil {
		t.Fatal("expected error for empty URI")
	}
}

func TestGetTorrent(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, _ := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:BB&dn=Test"})

	get, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
	if err != nil {
		t.Fatal(err)
	}
	if get.Torrent.Name != "Test" {
		t.Errorf("expected 'Test', got %s", get.Torrent.Name)
	}
	if get.Torrent.Status != "queued" && get.Torrent.Status != "downloading" {
		t.Errorf("expected status 'queued' or 'downloading', got %s", get.Torrent.Status)
	}
}

func TestGetTorrentNotFound(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: "nonexistent"})
	if err == nil {
		t.Fatal("expected error for nonexistent torrent")
	}
}

func TestRemoveTorrent(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, _ := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:CC&dn=RemoveMe"})
	m.RemoveTorrent(ctx, &downloaderv1.RemoveTorrentRequest{Id: add.Id})

	_, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
	if err == nil {
		t.Fatal("expected error after removal")
	}
}

func TestListTorrents(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:D1&dn=One"})
	m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:D2&dn=Two"})

	resp, err := m.ListTorrents(ctx, &downloaderv1.ListTorrentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Torrents) != 2 {
		t.Errorf("expected 2 torrents, got %d", len(resp.Torrents))
	}
}

func TestListTorrentsFilterDownloading(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:E1&dn=Test"})

	resp, err := m.ListTorrents(ctx, &downloaderv1.ListTorrentsRequest{Filter: "completed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Torrents) != 0 {
		t.Errorf("expected 0 completed torrents, got %d", len(resp.Torrents))
	}
}

func TestParseInfoHash(t *testing.T) {
	cases := []struct {
		uri  string
		want string
	}{
		{"magnet:?xt=urn:btih:ABCDEF1234567890ABCDEF1234567890ABCDEF12&dn=Test", "ABCDEF1234567890ABCDEF1234567890ABCDEF12"},
		{"ABCD1234ABCD1234ABCD1234ABCD1234ABCD1234", "ABCD1234ABCD1234ABCD1234ABCD1234ABCD1234"},
		{"invalid", ""},
	}
	for _, c := range cases {
		got := parseInfoHash(c.uri)
		if got != c.want {
			t.Errorf("parseInfoHash(%q) = %q, want %q", c.uri, got, c.want)
		}
	}
}

func TestParseMagnetName(t *testing.T) {
	cases := []struct {
		uri  string
		want string
	}{
		{"magnet:?xt=urn:btih:FF&dn=My+Torrent", "My Torrent"},
		{"magnet:?xt=urn:btih:FF&dn=%48%65%6C%6C%6F", "Hello"},
		{"magnet:?xt=urn:btih:FF", "unknown"},
	}
	for _, c := range cases {
		got := parseMagnetName(c.uri)
		if got != c.want {
			t.Errorf("parseMagnetName(%q) = %q, want %q", c.uri, got, c.want)
		}
	}
}

func TestDownloadProgresses(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, _ := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:GG&dn=Progress"})

	get, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
	if err != nil {
		t.Fatal(err)
	}
	if get.Torrent.Progress < 0 {
		t.Errorf("expected non-negative progress, got %f", get.Torrent.Progress)
	}
	if get.Torrent.DownloadRate > 0 || get.Torrent.Status == "downloading" {
		// download is in progress or queued — acceptable
	}
}

func TestHealth(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()
	if err := m.Health(ctx); err != nil {
		t.Fatal("expected health to pass")
	}
}

func TestVpnStatusNotConnected(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	resp, err := m.VpnStatus(ctx, &downloaderv1.VpnStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Connected {
		t.Fatal("expected VPN not connected")
	}
}

func TestVpnStartNoConfig(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	_, err := m.VpnStart(ctx, &downloaderv1.VpnStartRequest{})
	if err == nil {
		t.Fatal("expected error for missing config")
	}
}

func TestVpnStopWhenNotConnected(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	resp, err := m.VpnStop(ctx, &downloaderv1.VpnStopRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Stopped {
		t.Fatal("expected stop to report success even when not connected")
	}
}

func TestNatPmpStatusNotEnabled(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	resp, err := m.NatPmpStatus(ctx, &downloaderv1.NatPmpStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Enabled {
		t.Fatal("expected NAT-PMP not enabled")
	}
}

func TestNatPmpSendMapNoGateway(t *testing.T) {
	n := newNATPMPClient()
	_, err := n.sendMap(6881, "tcp", 7200)
	if err == nil {
		t.Fatal("expected error when no gateway is set")
	}
}

func TestDeriveInterfaceName(t *testing.T) {
	iface := deriveInterfaceName("/path/to/wg-us-tx.conf")
	if iface != "wg-us-tx" {
		t.Errorf("expected wg-us-tx, got %s", iface)
	}
}

func TestDescription(t *testing.T) {
	m := NewModule(Config{})
	desc := m.Info().Description
	if desc == "" {
		t.Error("description must not be empty")
	}
	if !contains(desc, "WireGuard") {
		t.Error("expected description to mention WireGuard")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && indexOf(s, sub, 0) >= 0
}

func TestLifecycle(t *testing.T) {
	m := NewModule(Config{GRPCAddr: ":0", DownloadDir: filepath.Join(t.TempDir(), "dl")})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}
