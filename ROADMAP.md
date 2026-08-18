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

## Remaining

- [x] File selection priorities
- [x] DHT / PEX tuning knobs via settings
- [x] Persist / restore active torrents across process restart
