# Downloader Native Torrent

Native BitTorrent engine for MuxCore using [anacrolix/torrent](https://github.com/anacrolix/torrent), with optional WireGuard VPN and NAT-PMP.

> **Note:** Default gRPC port `:9460` collides with `media-automation` if both run on one host — set `DOWNLOADER_GRPC_ADDR` (e.g. `:9461`) when needed.

## Key Features

- Real torrent client (magnets and HTTP(S) `.torrent` URLs)
- Per-torrent progress via `GetTorrent` / `ListTorrents`
- Publishes `download.started`, `download.completed`, `download.failed` on the core event bus when mesh is connected
- Seed until ratio ≥ 1.0 or 60 minutes (configurable), then leave the swarm and keep files
- WireGuard VPN + kill switch and NAT-PMP (VPN traffic binding to the torrent socket is still TODO)

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `DOWNLOADER_GRPC_ADDR` | `:9460` | gRPC listen address (registered as `HTTPAddr`) |
| `DOWNLOAD_DIR` | `/var/lib/downloader-native-torrent/downloads` | Client data dir / default save path |
| `TORRENT_LISTEN_PORT` | `6881` | BitTorrent listen port (falls back to `NAT_PMP_PORT` if set) |
| `SEED_RATIO` | `1.0` | Stop seeding when uploaded/size reaches this ratio |
| `SEED_MINUTES` | `60` | Max seeding time after complete |
| `WG_CONF` | `""` | WireGuard config file path |
| `WG_KILL_SWITCH` | `false` | Enable VPN kill switch |
| `NAT_PMP_PORT` | `0` | NAT-PMP port mapping |
| `MUXCORE_GRPC_ADDR` | — | Core mesh (optional; required for download.* events) |
| `MUXCORE_GRPC_INSECURE` | `false` | Disable TLS for local core |

## Capability

`downloader` — discovered by `media-automation` for `AddTorrent` dispatch.

## Events

| Type | When |
|------|------|
| `download.started` | Metadata resolved and download begun |
| `download.completed` | All pieces present; payload includes `save_path` and `files` |
| `download.failed` | Add/metadata/download hard failure |

## Dependencies

- `github.com/anacrolix/torrent` — BitTorrent engine
- `github.com/Muxcore-Media/core` — SDK / contracts / mesh client
- `golang.zx2c4.com/wireguard/wgctrl` — WireGuard control

## Development

```bash
make test   # fake engine + httptest only; no public trackers
make build
```
