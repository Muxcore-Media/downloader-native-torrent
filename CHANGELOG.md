# Changelog

## [0.3.10] - 2026-10-05


### Changed
- Fixture engine payload is now 6 MiB (sparse: 8 KiB pattern header, remainder via `Truncate`) so media-scanner's default 5 MiB minimum no longer skips fixture grabs; reported total/progress sizes follow the file.

## [0.3.8] - 2026-10-05

### Changed
- CI runs on GitHub-hosted runners from the umbrella template; retired-origin workflows removed.
- Dependencies resolve from published GitHub tags (no filesystem `replace`); requires core v0.6.0.

## v0.3.8 (2026-08-31)

- VPN rebind migrates live torrent sessions onto the new anacrolix client instead of dropping them.
- Public itorrents metainfo cache is opt-in (`DOWNLOADER_PUBLIC_METAINFO_CACHE`); fixture/fake engines skip it; mesh `GetMeta`/`PutMeta` preferred.
- `contracts-downloader` adapter: `SupportsFileSelection=true`, AND category/status filters, uploaded bytes and per-file `Wanted`.
- Live settings: `listen_port` rebinds engine, `download_path` retargets local dir, `file_priority` applies to active sessions; `seed_ratio` / `seed_minutes` exposed.
- VPN: honor WireGuard `DNS=`, iptables fail-closed, refuse `WG_KILL_SWITCH` on shared hosts unless `WG_KILL_SWITCH_ALLOW=true`.
- Live engine gates `AddURI` until WireGuard connects; completed/error torrents persist in session history; seeding reports upload rate and `seeding` status.
- Tests: contracts server, mesh `torrent/sessions.json`, rebind migration; CI runs `golangci-lint`.

## v0.3.1 (2026-08-20)

- Settings: `recheck_torrent` (VerifyData) and `torrent_file_priority` (`<id>:<all|episodes|season_packs>`).
- Mesh session list already persisted to `torrent/sessions.json` (documented).

## v0.3.0 (2026-08-20)

- **Mesh storage (default):** piece I/O streams through core `StorageService` (`DOWNLOAD_STORAGE=mesh`). Keys `torrent/{infohash}/p/{n}` + completion bitfield; seeding reads the same objects. No local `DOWNLOAD_DIR` required. Set `DOWNLOAD_STORAGE=local` for the legacy file client.
- Events use `save_path=storage://torrent/{infohash}`; `RemoveTorrent(delete_files)` deletes the storage prefix.
- Call-policy: grant `downloader-native-torrent` storage read/write (see call-policy-default v0.3.4).

## v0.2.12 (2026-08-18)

- Persist active torrents to `.muxcore-torrents.json` and restore the same ids/URIs/save paths on start so a downloader restart does not drop in-flight grabs.

## v0.2.11 (2026-08-18)

- After metadata, `pending_*` save dirs under `partials/{item}/` are renamed to `btih_{infohash}`. Same-hash resume reuses an existing `btih_*` dir. Download events then persist the short identity.

## v0.2.10 (2026-08-18)

- Piece-completion DB is opened once per engine and reused across VPN rebind, with retries on lock timeout. A clean stop/start no longer logs `couldn't open piece completion db … timeout` or fall back to in-memory completion.

## v0.2.9 (2026-08-18)

- Download events join torrent file paths onto `save_path` without duplicating a relative prefix (`partials/item/…` + `partials/item/file.mkv`).

## v0.2.8 (2026-08-18)

- Relative `save_path` is always joined to the download dir and cleaned; paths that would escape (`../outside`) are clamped to the download dir so torrents never land in process cwd.

## v0.2.7 (2026-08-18)

- Download events always emit an absolute `save_path` and file paths (relative `partials/…` joined with the download dir) so scanner ImportPath stays under the watch directory.

## v0.2.4 (2026-08-18)

- HTTP `.torrent` fetches that redirect to `magnet:` (Prowlarr proxy) are added as magnets instead of failing with `unsupported protocol scheme "magnet"`.

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
