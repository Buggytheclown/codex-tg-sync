# Sync-only Telegram Surface Brief

## Goal

Retire the legacy direct-message observer and make the configured private Sync
forum group the only Telegram control surface. Keep Sync topics, Control
commands, approvals, external launch requests, health delivery, and pollers
working without a compatibility router.

## Non-goals

- Reworking the App Server protocol or writer ownership model.
- Rewriting YMessenger, cron, or Arcanum pollers.
- Dropping old SQLite tables from existing installations.
- Adding a migration framework or a second routing abstraction.

## Product contract

- Exactly one configured Telegram user may act in the exact Sync group.
- Messages and callbacks outside that group are ignored before storage or App
  Server access.
- The default Bot API command scope is empty. The Sync group exposes exactly
  `/sync`, `/status`, `/pollers`, `/requests`, `/refresh`, `/projects`,
  `/newchat`, and `/stop`; `/repair` remains an unadvertised Control command.
- Startup supersedes undelivered observer traffic and any undelivered Telegram
  traffic addressed outside the Sync group. Current health, external terminal,
  and Sync activation notifications addressed to the group remain deliverable.
- Normal Sync turns use the thread-preferred model or App Server defaults and do
  not read retired Telegram model/reasoning settings. Explicit request-local
  execution options remain supported for external launch requests.
- Fresh setup requires an Sync group id and exactly one allowed user.

## Acceptance

- Sync prompt, steer, stop, approvals, structured input, project topics, and
  Control commands pass their existing tests.
- Requests cards, retry/check/close, terminal notifications, auto-start, and
  external replies pass their existing tests.
- Poller status and durable observations remain available.
- A fresh database has no legacy-only Telegram tables; an existing database is
  opened without destructive cleanup and its current-mode data is preserved.
- Current docs no longer advertise DM observer, panels, Details, Plan, or
  Telegram settings.
- Targeted race tests, `go test ./...`, and `go build -buildvcs=false ./...`
  pass. Live QA checks command scopes, silent DM behavior, Control commands,
  topic lifecycle, and an available approval callback.
