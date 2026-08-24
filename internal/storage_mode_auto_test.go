package internal

import (
	"os"
	"testing"
)

func TestStorageModeAutoLocal(t *testing.T) {
	t.Setenv("DOWNLOAD_STORAGE", "")
	t.Setenv("MUXCORE_GRPC_ADDR", "")
	if got := storageMode(); got != "local" {
		t.Fatalf("got %q want local (DOWNLOAD_STORAGE=%q MUX=%q)", got, os.Getenv("DOWNLOAD_STORAGE"), os.Getenv("MUXCORE_GRPC_ADDR"))
	}
}
