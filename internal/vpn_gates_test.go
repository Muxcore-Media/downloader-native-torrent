package internal

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func TestEnforceLiveDownloaderVPN(t *testing.T) {
	t.Setenv("WG_CONF", "")
	if err := enforceLiveDownloaderVPN("", "fixture"); err != nil {
		t.Fatalf("fixture should skip VPN: %v", err)
	}
	if err := enforceLiveDownloaderVPN("", "live"); err == nil {
		t.Fatal("live engine without WG_CONF should fail")
	}
}

func TestEnforceLiveDownloaderVPNReadableConf(t *testing.T) {
	f, err := os.CreateTemp("", "wg-*.conf")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	_ = f.Close()
	t.Setenv("WG_CONF", f.Name())
	if err := enforceLiveDownloaderVPN("", "live"); err != nil {
		t.Fatalf("live engine with readable WG_CONF should pass: %v", err)
	}
}

func TestValidateKillSwitchRefusedOnSharedHost(t *testing.T) {
	t.Setenv("WG_KILL_SWITCH_ALLOW", "")
	t.Setenv("WG_KILL_SWITCH_SCOPE", "")
	if err := validateKillSwitch(true); err == nil {
		t.Fatal("expected kill switch refusal on shared host")
	}
	t.Setenv("WG_KILL_SWITCH_ALLOW", "true")
	if err := validateKillSwitch(true); err != nil {
		t.Fatalf("opt-in allow: %v", err)
	}
}

func TestModuleInitRejectsLiveWithoutVPN(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "live")
	t.Setenv("WG_CONF", "")
	m := NewModule(Config{GRPCAddr: ":0"})
	if err := m.Init(context.Background()); err == nil {
		t.Fatal("expected Init to fail without WG_CONF for live engine")
	}
}

type vpnLiveEngine struct {
	fakeEngine
}

func TestAddTorrentFailsFastWithoutVPNOnLiveEngine(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "live")
	t.Setenv("WG_CONF", "")
	m := newTestModuleWithEngine(t, &vpnLiveEngine{fakeEngine: fakeEngine{instantComplete: true}})
	_, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri: "magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&dn=VPN",
	})
	if err == nil || !strings.Contains(err.Error(), "WG_CONF") {
		t.Fatalf("expected WG_CONF fail-fast, got %v", err)
	}
}

func TestResolveEngineMode(t *testing.T) {
	for _, v := range []string{"", "  ", "fixture", "FAKE"} {
		if mode, err := resolveEngineMode(v); err != nil || mode != engineModeFixture {
			t.Fatalf("%q: mode=%q err=%v, want fixture", v, mode, err)
		}
	}
	if mode, err := resolveEngineMode("Live"); err != nil || mode != engineModeLive {
		t.Fatalf("live: mode=%q err=%v", mode, err)
	}
	for _, v := range []string{"true", "real"} {
		if _, err := resolveEngineMode(v); err == nil {
			t.Fatalf("%q: expected error", v)
		}
	}
}

func TestInitDefaultsToFixtureEngine(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "")
	t.Setenv("WG_CONF", "")
	t.Setenv("DOWNLOAD_STORAGE", "local")
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", DownloadDir: t.TempDir()})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.lis.Close() })
	if _, ok := m.engine.(*fixtureEngine); !ok {
		t.Fatalf("unset DOWNLOADER_ENGINE: engine=%T, want *fixtureEngine", m.engine)
	}
}

func TestInitRejectsUnknownEngine(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "bogus")
	t.Setenv("WG_CONF", "")
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0"})
	if err := m.Init(context.Background()); err == nil {
		t.Fatal("expected unknown DOWNLOADER_ENGINE to fail Init")
	}
}

func TestInitLiveWithVPNConfSelectsLiveEngine(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "wg0.conf")
	if err := os.WriteFile(conf, []byte("[Interface]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOWNLOADER_ENGINE", "live")
	t.Setenv("WG_CONF", conf)
	t.Setenv("DOWNLOAD_STORAGE", "local")
	built := 0
	origVPN := autoStartVPNOnInit
	autoStartVPNOnInit = false
	t.Cleanup(func() { autoStartVPNOnInit = origVPN })
	orig := newLiveEngine
	newLiveEngine = func(string, int, anacrolixEngineOpts) (torrentEngine, error) {
		built++
		return &fakeEngine{}, nil
	}
	t.Cleanup(func() { newLiveEngine = orig })
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", DownloadDir: t.TempDir()})
	if err := m.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.lis.Close() })
	if built != 1 {
		t.Fatalf("live engine constructor calls = %d, want 1", built)
	}
	if _, ok := m.engine.(*fixtureEngine); ok {
		t.Fatal("live selected but fixture engine in use")
	}
}

func TestEnsureEngineLiveWithoutVPNFailsClosed(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "live")
	t.Setenv("WG_CONF", "")
	orig := newLiveEngine
	newLiveEngine = func(string, int, anacrolixEngineOpts) (torrentEngine, error) {
		t.Fatal("live engine must not be constructed without VPN")
		return nil, nil
	}
	t.Cleanup(func() { newLiveEngine = orig })
	m := NewModule(Config{GRPCAddr: "127.0.0.1:0", DownloadDir: t.TempDir()})
	if err := m.ensureEngine(context.Background()); err == nil {
		t.Fatal("expected ensureEngine to refuse live without WG_CONF")
	}
}

func TestResolveEngineModeAnacrolixAlias(t *testing.T) {
	mode, err := resolveEngineMode("anacrolix")
	if err != nil || mode != engineModeLive {
		t.Fatalf("anacrolix alias = %q, %v; want live", mode, err)
	}
	if err := enforceLiveDownloaderVPN("", "anacrolix"); err == nil {
		t.Fatal("anacrolix alias without WG_CONF must fail closed")
	}
}
