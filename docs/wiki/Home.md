# codex-tg Wiki

`codex-tg` is a local Codex Control Plane whose supported Telegram product is one private AFC forum group.

## Start here

- [Quickstart](Quickstart.md)
- [Telegram UX](Telegram-UX.md)
- [Architecture](Architecture.md)
- [Operations](Operations.md)
- [Control Plane](Control-Plane.md)
- [Security](Security.md)

## Core idea

Keep Codex App Server local and authoritative while mirroring each Codex chat into a Telegram topic. Control commands, launch requests, poller status, approvals, health, and terminal notifications live in the same configured forum group.

Direct-message observer workflows are retired. See [ADR-032](../adr/ADR-032-sync-only-telegram-surface.md).
