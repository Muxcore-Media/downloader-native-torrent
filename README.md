# Downloader Native Torrent

Native BitTorrent engine for MuxCore using [anacrolix/torrent](https://github.com/anacrolix/torrent), with optional WireGuard VPN and NAT-PMP.

## Key Features

- Real torrent client (magnets and HTTP(S) `.torrent` URLs)
- **Mesh storage (default):** pieces stream to/from core `StorageService` over gRPC — ready for local, S3, Ceph, or overlay backends. Seeding reads the same storage keys. Set `DOWNLOAD_STORAGE=local` only for legacy disk layouts.
- Per-torrent progress via `GetTorrent` / `ListTorrents`
- `PauseTorrent` / `ResumeTorrent` (local `TorrentService` and shared `contracts-downloader`)
- Publishes `download.started`, `download.completed`, `download.failed` on the core event bus when mesh is connected
- Seed until ratio ≥ 1.0 or 60 minutes (configurable), then leave the swarm and keep data in storage (or local dir)
- WireGuard VPN + kill switch and Proton NAT-PMP; after handshake the client maps UDP+TCP, then rebinds listen host/port to the WireGuard IPv4 address and the assigned port
- Mesh `settings` capability (`download_path`, `listen_port`, `wg_conf`, `wg_kill_switch`)

## Configuration

Proton WireGuard configs with `# NAT-PMP (Port Forwarding) = on` trigger a map to `10.2.0.1:5351` (UDP then TCP, 60s lease, renew every 45s). Proton assigns a random port; the torrent client is rebound to the WireGuard IPv4 address on that port. `TORRENT_LISTEN_PORT` is only used when NAT-PMP is off or the map fails.

| Env Var | Default | Description |
|---------|---------|-------------|
| `DOWNLOADER_GRPC_ADDR` | `:9461` | gRPC listen address (registered as `HTTPAddr`) |
| `DOWNLOAD_STORAGE` | `auto` | `mesh` = gRPC StorageService; `local` = `DOWNLOAD_DIR` files; `auto` = mesh when `MUXCORE_GRPC_ADDR` is set |
| `DOWNLOAD_DIR` | `/var/lib/downloader-native-torrent/downloads` | Used only when `DOWNLOAD_STORAGE=local` |
| `TORRENT_LISTEN_PORT` | `6881` | Fallback BitTorrent listen port when NAT-PMP is off or mapping fails (also used if `NAT_PMP_PORT` is set and this is unset) |
| `TORRENT_FILE_PRIORITY` | `all` | File selection: `all`, `episodes` (prefer SxxEyy; skip samples/packs when episodes exist), `season_packs` |
| `TORRENT_ENABLE_DHT` | `true` | Enable BitTorrent DHT (restart module after change via settings) |
| `TORRENT_ENABLE_PEX` | `true` | Enable peer exchange |
| `SEED_RATIO` | `1.0` | Stop seeding when uploaded/size reaches this ratio |
| `SEED_MINUTES` | `60` | Max seeding time after complete |
| `WG_CONF` | `""` | WireGuard config file path |
| `WG_KILL_SWITCH` | `false` | Enable VPN kill switch |
| `NAT_PMP_PORT` | `0` | Not sent to Proton. Optional fallback listen port if `TORRENT_LISTEN_PORT` is unset. Proton assigns a random 60s port via NAT-PMP (`10.2.0.1:5351`); the downloader listens on that port and renews every 45s |
| `MUXCORE_GRPC_ADDR` | — | Core mesh (required for mesh storage + download.* events) |
| `MUXCORE_INSECURE_DISABLE_TLS` | `false` | Disable TLS for local core |

## Capabilities

- `downloader` — discovered by `media-automation` for `AddTorrent` dispatch
- `downloader.native.torrent`
- `settings`

## gRPC services

- Local `muxcore.downloader.native.v1.TorrentService` (VPN / NAT-PMP helpers included)
- Shared `contracts-downloader` `DownloaderService` adapter on the same listener

## Events

| Type | When |
|------|------|
| `download.started` | Metadata resolved and download begun |
| `download.completed` | All pieces present; payload includes `save_path` and `files` |
| `download.failed` | Add/metadata/download hard failure |

## Dependencies

- `github.com/anacrolix/torrent` — BitTorrent engine
- `github.com/Muxcore-Media/contracts-downloader` — shared Downloader contract
- `github.com/Muxcore-Media/core` — SDK / contracts / mesh client
- `golang.zx2c4.com/wireguard/wgctrl` — WireGuard control

## Development

```bash
make test   # fake engine + httptest only; no public trackers
make build
```
