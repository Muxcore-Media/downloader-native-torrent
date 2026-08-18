# Changelog

## v0.2.3 (2026-08-18)

- Relative `save_path` is joined with the download dir (automation `partials/{item}/{hash}`).
- Adding a magnet whose infohash is already active merges trackers onto the existing torrent instead of a second session.
- After metadata, sibling dirs under `partials/{item}/` with the same file-size fingerprint are piece-verified; matching bytes are hardlinked (copy fallback) then rechecked.

## v0.2.2 (2026-08-09)

- File selection priorities (`all` / `episodes` / `season_packs`) via `TORRENT_FILE_PRIORITY` and settings `file_priority`; skips samples/extras; `SupportsFileSelection=true`
- DHT / PEX toggles (`TORRENT_ENABLE_DHT` / `TORRENT_ENABLE_PEX`, settings `enable_dht` / `enable_pex`; apply on client create/restart)
- Pause / Resume RPCs on local `TorrentService` and `contracts-downloader`
- Register shared `contracts-downloader` `DownloaderService` alongside local proto
- Rebind torrent listen host to WireGuard IP on VPN auto-start
- Soften kill-switch rules to use WireGuard iface + peer endpoint
- Mesh `settings` capability (`download_path`, `listen_port`, `wg_conf`, `wg_kill_switch`, `file_priority`, `enable_dht`, `enable_pex`)

## v0.2.1 (2026-08-09)

- Patch release for MVP host stack (see GitHub Release notes)

## v0.2.0

- Replace simulated downloads with `anacrolix/torrent`
- Support magnet and HTTP(S) `.torrent` URIs
- Publish `download.started`, `download.completed`, `download.failed`
- Configurable seed ratio / seed minutes; honor `delete_files` on remove
- Injectable torrent engine for unit tests (no public trackers in CI)

## v0.1.0

- Initial release with simulated download progress
- WireGuard VPN and NAT-PMP scaffolding
