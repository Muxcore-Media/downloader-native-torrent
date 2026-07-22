# Changelog

## v0.2.0

- Replace simulated downloads with `anacrolix/torrent`
- Support magnet and HTTP(S) `.torrent` URIs
- Publish `download.started`, `download.completed`, `download.failed`
- Configurable seed ratio / seed minutes; honor `delete_files` on remove
- Injectible torrent engine for unit tests (no public trackers in CI)

## v0.1.0

- Initial release with simulated download progress
- WireGuard VPN and NAT-PMP scaffolding
