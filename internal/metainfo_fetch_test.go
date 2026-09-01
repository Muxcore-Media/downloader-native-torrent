package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

type stubMeshMeta struct {
	meta []byte
}

func (s *stubMeshMeta) GetMeta(_ context.Context, _ metainfo.Hash) ([]byte, error) {
	if len(s.meta) == 0 {
		return nil, http.ErrServerClosed
	}
	return s.meta, nil
}

func (s *stubMeshMeta) PutMeta(_ context.Context, _ metainfo.Hash, data []byte) error {
	s.meta = append([]byte(nil), data...)
	return nil
}

func TestFetchTorrentMetainfoSkipsPublicWhenFixtureEngine(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "fixture")
	t.Setenv("DOWNLOADER_PUBLIC_METAINFO_CACHE", "")
	publicCalled := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		publicCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	_, err := fetchTorrentMetainfoWith(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", srv.Client(), nil)
	if err == nil {
		t.Fatal("expected error without mesh or public cache")
	}
	if publicCalled {
		t.Fatal("public metainfo cache must not be contacted when DOWNLOADER_ENGINE=fixture")
	}
}

func TestFetchTorrentMetainfoPrefersMesh(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "fixture")
	mesh := &stubMeshMeta{meta: []byte("d8:announce")}
	hc := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("public HTTP must not run when mesh has meta")
		return nil, nil
	})}
	_, err := fetchTorrentMetainfoWith(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", hc, mesh)
	if err == nil {
		// invalid torrent bytes — mesh returned data but validation fails; still no public dial
		if !strings.Contains(err.Error(), "infohash") && !strings.Contains(err.Error(), "torrent") {
			t.Fatalf("unexpected err: %v", err)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestPublicMetainfoCacheOptIn(t *testing.T) {
	t.Setenv("DOWNLOADER_ENGINE", "fixture")
	t.Setenv("DOWNLOADER_PUBLIC_METAINFO_CACHE", "true")
	if !publicMetainfoCacheEnabled() {
		t.Fatal("expected opt-in public cache")
	}
}
