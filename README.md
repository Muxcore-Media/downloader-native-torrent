# Downloader Native Torrent

Native torrent download engine with no external dependencies (anacrolix/torrent), supporting WireGuard VPN binding and NAT-PMP.

## Key Features

- Built-in torrent client — no external downloader required
- WireGuard VPN integration with kill switch
- NAT-PMP port mapping
- Per-torrent status tracking and management
- Configurable download directory

## Configuration

| Env Var | Default | Description |
|---------|---------|-------------|
| `DOWNLOADER_GRPC_ADDR` | `:9460` | gRPC listen address |
| `DOWNLOAD_DIR` | `/var/lib/downloader-native-torrent/downloads` | Download directory |
| `WG_CONF` | `""` | WireGuard config file path |
| `WG_KILL_SWITCH` | `false` | Enable VPN kill switch |
| `NAT_PMP_PORT` | `0` | NAT-PMP port mapping |

## Capability

`downloader.native.torrent` — Native torrent downloading

## Dependencies

- `github.com/Muxcore-Media/core` — MuxCore SDK
- `golang.zx2c4.com/wireguard/wgctrl` — WireGuard control
