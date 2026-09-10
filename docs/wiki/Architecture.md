# Architecture

`codex-tg` is a local Codex Control Plane with an Sync forum adapter.

```text
Telegram Sync forum     cron / YMessenger / Arcanum
        |                         |
        +-----------+-------------+
                    v
             daemon/control core
        routing, requests, health,
        pollers, delivery, ownership
                    |
                    v
             Codex App Server
         threads, turns, approvals,
          input, events, snapshots
                    |
                    v
             local workspaces
```

## Runtime authority

Codex App Server is authoritative for interactive state. The daemon may spawn App Server over stdio or connect to a managed Unix-socket/loopback WebSocket endpoint. Shared modes fail closed and do not spawn a private fallback.

One poll session discovers threads and receives App Server events. Important events coalesce into bounded latest-turn reads; an infrequent summary reconciliation covers reconnects and missed events. Generation-aware writer leases serialize mutations and prevent two processes from owning the same thread.

## Telegram adapter

The adapter accepts one user in one exact private forum group. Sync topic state maps Telegram topic ids to durable Codex thread ids. Callback routes contain the coordinates required to fail closed on stale actions.

The Telegram adapter does not contain a second direct-message router.

## State

SQLite stores current Sync state and topics, topic drafts, callback routes, receipts, external launch requests, delivery metadata, thread snapshots, and daemon state.

Fresh databases do not create retired DM binding, observer, panel, or approval tables. Old databases are opened non-destructively and any such tables remain inert.

## External adapters

Cron, Yandex Messenger, and Arcanum pollers normalize work into the same durable launch-request lifecycle. Source-specific cursors and health observations are persisted. Telegram renders approval/status cards; terminal delivery can reply to the original source.

Further reading: [ADR-019](../adr/ADR-019-codex-control-plane.md), [ADR-032](../adr/ADR-032-sync-only-telegram-surface.md).
