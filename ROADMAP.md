# Roadmap

## Done

- [x] Real anacrolix torrent client (replace simulateDownload)
- [x] Magnet + HTTP(S) `.torrent` add paths
- [x] Progress / complete / fail status on TorrentService
- [x] `download.started` / `download.completed` / `download.failed` events
- [x] Seed ratio / time policy; Remove with `delete_files`
- [x] Bind BitTorrent traffic to WireGuard interface
- [x] Pause / Resume RPCs (align with contracts-downloader)
- [x] Expose shared `contracts-downloader` `DownloaderService` (local `TorrentService` retained)
- [x] Soften hardcoded VPN kill-switch iface/endpoint assumptions
- [x] File selection priorities
- [x] DHT / PEX tuning knobs via settings
- [x] Persist / restore active torrents across process restart
- [x] Mesh StorageService piece store + seed-from-storage (`DOWNLOAD_STORAGE=mesh`)

## Remaining

- [x] Assemble completed piece objects into per-file storage keys for scanner ImportPath without a local watch dir
- [x] Per-file wanted / priority via settings (`torrent_file_priority`, `recheck_torrent`)
- [x] Persist active torrent session list in storage (`torrent/sessions.json`) instead of local JSON when mesh mode
