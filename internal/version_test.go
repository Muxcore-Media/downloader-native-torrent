package internal

import (
	"testing"

	modulesdk "github.com/Muxcore-Media/core/sdk/go/module"
	manifest "github.com/Muxcore-Media/downloader-native-torrent"
)

// TestInfoVersionFromManifest asserts the reported version has a single source (ADR-0021).
func TestInfoVersionFromManifest(t *testing.T) {
	want := modulesdk.ManifestVersion(manifest.ManifestJSON)
	if want == "" {
		t.Fatal("muxcore.json has no version")
	}
	if got := NewModule(Config{Engine: &fakeEngine{}}).Info().Version; got != want {
		t.Errorf("Info().Version = %q, want manifest version %q", got, want)
	}
}
