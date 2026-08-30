# Loopback WebSocket App Server Brief

## Problem

Codex Desktop can share a standalone App Server listening on a loopback TCP
WebSocket, but `codex-tg` can only spawn a private stdio server or connect to the
managed daemon Unix socket. Selecting the WebSocket listener through the
existing `--listen` configuration incorrectly spawns another process and then
tries to use its stdio streams.

## Goal

Allow `codex-tg` to connect directly to an already running loopback App Server,
for example `ws://127.0.0.1:4500`, without spawning or owning that process. The
connection must retain the shared-runtime heartbeat, recovery, and Sync safety
rules used by managed-daemon mode.

## Non-goals

- Remote plaintext WebSocket endpoints.
- WebSocket authentication or custom TLS configuration.
- Changing the default spawned-stdio mode.
- Starting or supervising the standalone App Server from `codex-tg`.

## UX / Operator Flow

Configure:

```env
CTR_GO_APP_SERVER_MODE=websocket
CTR_GO_APP_SERVER_LISTEN=ws://127.0.0.1:4500
```

The operator starts the shared App Server before `codex-tg`. If it is absent,
the bridge stays fail-closed, reports the configured shared App Server as
unavailable, and never falls back to a private process.

## Architecture

`internal/appserver` owns transport validation and the TCP WebSocket dial. A
small shared-runtime predicate keeps `daemon` and `websocket` lifecycle behavior
aligned in `internal/daemon`. Existing JSON-RPC framing and normalization remain
unchanged.

## Testing

- TCP WebSocket integration test for `initialize` / `initialized` framing.
- Reject non-loopback plaintext WebSocket URLs before dialing.
- Config parsing test for the new mode and endpoint.
- Lifecycle tests proving WebSocket mode uses shared-server heartbeat and
  startup-warning behavior.
- Full Go test and build checks.

## Acceptance Criteria

- [x] WebSocket mode connects to a loopback App Server without spawning Codex.
- [x] WebSocket mode uses shared-runtime heartbeat and Sync recovery rules.
- [x] Spawned stdio and managed daemon Unix-socket modes remain supported.
- [x] Public configuration and architecture docs describe the new mode.
