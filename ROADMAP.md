# Roadmap

## Done

- [x] Real anacrolix torrent client (replace simulateDownload)
- [x] Magnet + HTTP(S) `.torrent` add paths
- [x] Progress / complete / fail status on TorrentService
- [x] `download.started` / `download.completed` / `download.failed` events
- [x] Seed ratio / time policy; Remove with `delete_files`

## Remaining

- [ ] Bind BitTorrent traffic to WireGuard interface
- [ ] Pause / Resume RPCs (align with contracts-downloader)
- [ ] Migrate local `TorrentService` proto to shared `contracts-downloader`
- [ ] Soften hardcoded VPN kill-switch iface/endpoint assumptions
