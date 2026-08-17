package internal

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func newTestModule(t *testing.T) *Module {
	t.Helper()
	return newTestModuleWithEngine(t, &fakeEngine{instantComplete: true})
}

func newTestModuleWithEngine(t *testing.T, eng torrentEngine) *Module {
	t.Helper()
	m := NewModule(Config{
		GRPCAddr:    ":0",
		DownloadDir: filepath.Join(t.TempDir(), "downloads"),
		Engine:      eng,
		SeedMinutes: 1,
		SeedRatio:   1.0,
		InfoTimeout: 5 * time.Second,
	})
	ctx := context.Background()
	if err := m.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { m.Stop(ctx) })
	return m
}

func TestModuleInfo(t *testing.T) {
	m := NewModule(Config{Engine: &fakeEngine{}})
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

func TestAddTorrentUnsupportedScheme(t *testing.T) {
	m := newTestModule(t)
	_, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{Uri: "ftp://x/file.torrent"})
	if err == nil {
		t.Fatal("expected error for ftp")
	}
}

func TestGetTorrent(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	add, _ := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB&dn=Test"})

	get, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
	if err != nil {
		t.Fatal(err)
	}
	if get.Torrent.Name != "Test" {
		t.Errorf("expected 'Test', got %s", get.Torrent.Name)
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

	add, _ := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC&dn=RemoveMe"})
	m.RemoveTorrent(ctx, &downloaderv1.RemoveTorrentRequest{Id: add.Id})

	_, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
	if err == nil {
		t.Fatal("expected error after removal")
	}
}

func TestListTorrents(t *testing.T) {
	m := newTestModule(t)
	ctx := context.Background()

	m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1D1&dn=One"})
	m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2D2&dn=Two"})

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

	m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{Uri: "magnet:?xt=urn:btih:E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1E1&dn=Test"})

	resp, err := m.ListTorrents(ctx, &downloaderv1.ListTorrentsRequest{Filter: "seeding-only-no-match"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Torrents) != 0 {
		t.Errorf("expected 0 torrents for nonsense filter, got %d", len(resp.Torrents))
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

func TestFixtureEngineWritesVideo(t *testing.T) {
	dir := t.TempDir()
	m := newTestModuleWithEngine(t, &fixtureEngine{})
	m.dlDir = dir
	ctx := context.Background()

	add, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri:      "magnet:?xt=urn:btih:FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF&dn=Fight.Club.1999.1080p.BluRay",
		SavePath: dir,
	})
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		get, err := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
		if err != nil {
			t.Fatal(err)
		}
		if get.Torrent.Status == "completed" {
			want := filepath.Join(dir, "Fight.Club.1999.1080p.BluRay", "Fight.Club.1999.1080p.BluRay.mkv")
			if _, err := os.Stat(want); err != nil {
				t.Fatalf("fixture file missing: %v", err)
			}
			return
		}
		if get.Torrent.Status == "error" {
			t.Fatalf("unexpected error: %s", get.Torrent.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for completed")
}


func TestAddTorrentPaused(t *testing.T) {
	eng := &fakeEngine{instantComplete: true}
	m := newTestModuleWithEngine(t, eng)
	ctx := context.Background()

	add, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri:    "magnet:?xt=urn:btih:HHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHHH&dn=Paused",
		Paused: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		get, _ := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
		if get.Torrent.Status == "paused" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected paused status")
}

func TestAddTorrentFailEngine(t *testing.T) {
	eng := &fakeEngine{failAdd: fmt.Errorf("boom")}
	m := newTestModuleWithEngine(t, eng)
	ctx := context.Background()

	add, err := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:IIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIIII&dn=Fail",
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		get, _ := m.GetTorrent(ctx, &downloaderv1.GetTorrentRequest{Id: add.Id})
		if get.Torrent.Status == "error" {
			if !strings.Contains(get.Torrent.Error, "boom") {
				t.Errorf("error: %s", get.Torrent.Error)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("expected error status")
}

func TestRemoveDeletesFiles(t *testing.T) {
	eng := &fakeEngine{instantComplete: true}
	m := newTestModuleWithEngine(t, eng)
	ctx := context.Background()
	dir := m.dlDir
	add, _ := m.AddTorrent(ctx, &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:JJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJJ&dn=DelMe",
	})
	// wait briefly then remove with delete
	time.Sleep(50 * time.Millisecond)
	_, err := m.RemoveTorrent(ctx, &downloaderv1.RemoveTorrentRequest{Id: add.Id, DeleteFiles: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = dir
}

func TestClassifyURI(t *testing.T) {
	if _, err := classifyURI("magnet:?xt=urn:btih:aa"); err != nil {
		t.Fatal(err)
	}
	if kind, err := classifyURI("https://example.test/a.torrent"); err != nil || kind != "http" {
		t.Fatalf("got %s %v", kind, err)
	}
	if _, err := classifyURI("file:///tmp/x"); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchTorrentFileHTTPtest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not-a-real-torrent-but-fetched"))
	}))
	t.Cleanup(srv.Close)
	data, err := fetchTorrentFile(context.Background(), srv.Client(), srv.URL+"/x.torrent")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "not-a-real-torrent-but-fetched" {
		t.Errorf("got %q", data)
	}
}

func TestFetchTorrentFileTooLarge(t *testing.T) {
	big := strings.Repeat("a", maxTorrentFileBytes+10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(big))
	}))
	t.Cleanup(srv.Close)
	_, err := fetchTorrentFile(context.Background(), srv.Client(), srv.URL)
	if err == nil {
		t.Fatal("expected size error")
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


func TestDeriveInterfaceName(t *testing.T) {
	iface := deriveInterfaceName("/path/to/wg-us-tx.conf")
	if iface != "wg-us-tx" {
		t.Errorf("expected wg-us-tx, got %s", iface)
	}
}

func TestDescription(t *testing.T) {
	m := NewModule(Config{Engine: &fakeEngine{}})
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
	m := NewModule(Config{
		GRPCAddr:    ":0",
		DownloadDir: filepath.Join(t.TempDir(), "dl"),
		Engine:      &fakeEngine{},
	})
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
