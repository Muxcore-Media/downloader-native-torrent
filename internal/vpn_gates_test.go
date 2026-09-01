package internal

import (
	"context"
	"os"
	"testing"
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
