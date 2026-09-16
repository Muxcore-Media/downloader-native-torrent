# AGENTS.md — downloader-native-torrent

MuxCore sidecar module (`downloader-native-torrent`). Workspace deploy and SSH: [`../AGENTS.md`](../AGENTS.md). Default ports: [`_mvp/PORTS.md`](../_mvp/PORTS.md).

## Module identity

| Field | Value |
|-------|-------|
| Directory | `downloader-native-torrent` |
| Capabilities | see muxcore.json |
| Contracts | `contracts-downloader` `Downloader` v0.1.0 |

## Agent rules

- Modules run as gRPC sidecars; capabilities are the security boundary.
- TLS required in production (`MUXCORE_INSECURE_DISABLE_TLS` is dev-only).
- Match existing Go patterns; run `gofmt` and package tests before finishing.
- Cross-module events: prefer `github.com/Muxcore-Media/contracts-media/events` over deprecated `core/pkg/contracts` aliases.
- Do not edit polluted workspace dumps (see `MASTER-ROADMAP.md` Appendix H).
- Roadmaps, task lists, and remaining-work checklists live in workspace [`MASTER-ROADMAP.md`](../MASTER-ROADMAP.md) and umbrella GitHub Issues. Do not add `ROADMAP.md` / `TASKS.md` in this repo.

## Build

```bash
cd downloader-native-torrent
nix-shell -p go --run 'go test ./...'
```
