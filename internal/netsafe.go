package internal

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Muxcore-Media/core/sdk/go/module/netguard"
)

// SSRF guard for .torrent / indexer downloads (NFR-SEC-009 / RULE-VAL-2).
//
// Torrent URLs come from callers (indexer results, AddTorrent RPC) and are
// untrusted: by default they are fetched with the netguard UserURL profile
// (no private, loopback, link-local or metadata targets; checked at dial time
// and on every redirect). Operators who run an indexer proxy on the LAN
// (e.g. Prowlarr) list its host[:port] in DOWNLOADER_INDEXER_HOSTS; only those
// hosts are then reachable on private addresses (Integration profile pinned to
// the list, cloud metadata and link-local always blocked).

// envIndexerHosts is a comma-separated host[:port] allow-list of trusted
// LAN indexer proxies.
const envIndexerHosts = "DOWNLOADER_INDEXER_HOSTS"

func indexerHosts() []string {
	var out []string
	for _, h := range strings.Split(os.Getenv(envIndexerHosts), ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func userNetOptions(timeout time.Duration) netguard.Options {
	return netguard.Options{Timeout: timeout, MaxRedirects: 10, UseEnvProxy: true}
}

func newUserHTTPClient(timeout time.Duration) *http.Client {
	return netguard.NewClient(netguard.UserURL, userNetOptions(timeout))
}

// defaultTorrentHTTP is the engine's default fetch client (UserURL profile).
var defaultTorrentHTTP = newUserHTTPClient(30 * time.Second)

// trustedIndexerOptions returns Integration options pinned to the operator's
// indexer hosts, or ok=false when uri's host is not on the list.
func trustedIndexerOptions(uri string, timeout time.Duration) (netguard.Options, bool) {
	hosts := indexerHosts()
	if len(hosts) == 0 {
		return netguard.Options{}, false
	}
	opts := netguard.Options{
		Timeout: timeout, MaxRedirects: 10, UseEnvProxy: true,
		AllowPrivate: true, AllowLoopback: true, AllowedHosts: hosts,
	}
	if err := netguard.ValidateURL(uri, netguard.Integration, opts); err != nil {
		return netguard.Options{}, false
	}
	return opts, true
}

// validateTorrentURL rejects an http(s) torrent URL that the fetch client
// would refuse, so callers get a clear error up front.
func validateTorrentURL(uri string) error {
	uri = strings.TrimSpace(uri)
	if opts, ok := trustedIndexerOptions(uri, 0); ok {
		return netguard.ValidateURL(uri, netguard.Integration, opts)
	}
	return netguard.ValidateURL(uri, netguard.UserURL, userNetOptions(0))
}

// torrentFetchClient picks the client for uri. A caller-supplied client other
// than the default (tests, custom engines) is used as given.
func torrentFetchClient(hc *http.Client, uri string) *http.Client {
	if hc != nil && hc != defaultTorrentHTTP {
		return hc
	}
	if opts, ok := trustedIndexerOptions(strings.TrimSpace(uri), 30*time.Second); ok {
		return netguard.NewClient(netguard.Integration, opts)
	}
	return defaultTorrentHTTP
}
