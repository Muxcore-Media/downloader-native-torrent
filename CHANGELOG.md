# Changelog

## Unreleased

- File selection priorities (`all` / `episodes` / `season_packs`) via `TORRENT_FILE_PRIORITY` and settings `file_priority`; skips samples/extras; `SupportsFileSelection=true`
- DHT / PEX toggles (`TORRENT_ENABLE_DHT` / `TORRENT_ENABLE_PEX`, settings `enable_dht` / `enable_pex`; apply on client create/restart)
- Pause / Resume RPCs on local `TorrentService` and `contracts-downloader`
- Register shared `contracts-downloader` `DownloaderService` alongside local proto
- Rebind torrent listen host to WireGuard IP on VPN auto-start
- Soften kill-switch rules to use WireGuard iface + peer endpoint
- Mesh `settings` capability (`download_path`, `listen_port`, `wg_conf`, `wg_kill_switch`)

## v0.2.0

- Replace simulated downloads with `anacrolix/torrent`
- Support magnet and HTTP(S) `.torrent` URIs
- Publish `download.started`, `download.completed`, `download.failed`
- Configurable seed ratio / seed minutes; honor `delete_files` on remove
- Injectable torrent engine for unit tests (no public trackers in CI)

## v0.1.0

- Initial release with simulated download progress
- WireGuard VPN and NAT-PMP scaffolding
