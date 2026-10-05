package internal

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	downloaderv1 "github.com/Muxcore-Media/downloader-native-torrent/proto/downloaderv1"
)

func TestConfineSavePath(t *testing.T) {
	base := t.TempDir()
	dl := filepath.Join(base, "downloads")
	evil := dl + "-evil" // sibling with common prefix
	outside := filepath.Join(base, "outside")
	for _, d := range []string{dl, evil, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dl, "link")); err != nil {
		t.Fatal(err)
	}
	bad := map[string]string{
		"absolute outside": outside,
		"etc":              "/etc",
		"root":             "/",
		"sibling prefix":   evil,
		"dotdot":           "../outside",
		"dotdot abs":       dl + "/../outside",
		"symlink escape":   filepath.Join(dl, "link", "x"),
		"symlink rel":      "link/x",
		"nul":              "a\x00b",
	}
	for name, p := range bad {
		if got, err := confineSavePath(dl, p); err == nil {
			t.Errorf("%s: %q accepted as %q", name, p, got)
		}
	}
	for name, p := range map[string]string{"empty": "", "rel": "tv/show", "abs inside": filepath.Join(dl, "movies")} {
		if _, err := confineSavePath(dl, p); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := confineSavePath("", "x"); err == nil {
		t.Error("empty download dir must fail closed")
	}
}

func TestAddTorrentRejectsEscapingSavePath(t *testing.T) {
	m := newTestModule(t)
	_, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{
		Uri:      "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SavePath: "/etc",
	})
	if err == nil || !strings.Contains(err.Error(), "save_path rejected") {
		t.Fatalf("expected save_path rejection, got %v", err)
	}
}

func TestAddTorrentRejectsPrivateTorrentURL(t *testing.T) {
	m := newTestModule(t)
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/x.torrent",
		"http://127.0.0.1:8080/x.torrent",
		"http://localhost/x.torrent",
		"http://10.1.2.3/x.torrent",
		"http://[::1]/x.torrent",
		"http://prowlarr:9696/1/download?x=1", // intranet name, not allow-listed
	} {
		_, err := m.AddTorrent(context.Background(), &downloaderv1.AddTorrentRequest{Uri: u})
		if err == nil || !strings.Contains(err.Error(), "torrent url rejected") {
			t.Errorf("%s: expected rejection, got %v", u, err)
		}
	}
}

func TestFetchTorrentFileDefaultClientBlocksPrivate(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	t.Cleanup(srv.Close)
	t.Setenv(envIndexerHosts, "")
	for _, hc := range []*http.Client{nil, defaultTorrentHTTP} {
		if _, _, err := fetchTorrentFile(context.Background(), hc, srv.URL+"/x.torrent"); err == nil || !strings.Contains(err.Error(), "netguard") {
			t.Fatalf("expected netguard block, got %v", err)
		}
	}
	if hits != 0 {
		t.Fatalf("loopback server contacted %d times", hits)
	}
}

func TestFetchTorrentFileIndexerAllowList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	t.Setenv(envIndexerHosts, host)
	if err := validateTorrentURL(srv.URL + "/dl"); err != nil {
		t.Fatalf("allow-listed indexer rejected: %v", err)
	}
	// A listed indexer cannot bounce the fetch to metadata/off-list hosts.
	if _, _, err := fetchTorrentFile(context.Background(), nil, srv.URL+"/dl"); err == nil || !strings.Contains(err.Error(), "netguard") {
		t.Fatalf("expected redirect to metadata blocked, got %v", err)
	}
	// Other private hosts stay blocked.
	if err := validateTorrentURL("http://192.168.1.5:9696/dl"); err == nil {
		t.Fatal("non-listed LAN host must be rejected")
	}
}
