# Sync Reset-On-Start MVP

## Goal

Make `codex-tg` restarts predictable for the MVP. Every process start resets
the Telegram Sync surface to `off`, cleans up the previous Sync session, and
requires an explicit `/sync on` before Telegram can mutate Codex threads again.

The rest of the SQLite control-plane state remains intact. In particular,
thread snapshots, bindings, observer routes, delivery metadata, and Codex
runtime state are not deleted. A turn already running in the shared App Server
continues independently of the Telegram Sync reset.

## Startup Contract

1. Before background reconciliation starts, atomically mark the current Sync
   state `off`.
2. Mark every topic and draft from the previous Sync session for Telegram
   cleanup. Clear local active-turn ownership fields so stale `unknown` state
   cannot block the new process.
3. Keep durable receipts as audit records, but convert unfinished accepted
   receipts to `unknown`; they are never replayed.
4. Reset persisted App Server connection flags to disconnected before the new
   process attempts a connection.
5. After the Telegram bot is ready, try to delete cleanup-only Sync topics. The
   permanent Control topic is never deleted. Failed deletions remain durable
   cleanup targets for a later activation.
6. Attempt one shared-daemon connection immediately. If daemon mode is
   configured and the connection fails, send one concise warning to Control
   for this process start, including the operator recovery steps.

No Sync session is activated automatically. The operator starts a fresh session
with `/sync on` after runtime health is restored.

## Runtime Recovery

The supported macOS startup order is:

1. start the managed Codex App Server daemon;
2. start Codex Desktop in local-daemon mode;
3. start or restart `codex-tg`;
4. verify the shared connection and run `/sync on` in Control.

Shared-daemon mode remains fail-closed: `codex-tg` must not spawn a private App
Server when the configured Unix socket is unavailable.

## Scope

In scope:

- Sync-only reset and cleanup at `codex-tg` startup;
- one Control warning per failed startup in shared-daemon mode;
- public operator instructions for the supported shared-daemon startup order;
- storage, daemon, command wiring, and regression tests.

Out of scope:

- deleting or interrupting a turn that is already running in Codex;
- deleting the whole SQLite database;
- automatically enabling Sync after restart;
- a persistent macOS supervisor for the managed App Server daemon;
- automatic restart of Codex Desktop.

## Acceptance

- Restarting from an Sync topic with `starting`, `active`, or `unknown` work
  produces Sync `off`, not a `/sync off` refusal.
- The old Sync topics are cleanup-only and a later `/sync on` creates a fresh
  session without replaying Telegram input.
- Non-Sync SQLite records survive the reset.
- Stale persisted connection flags do not report a connection before the new
  process connects.
- An unavailable configured daemon produces exactly one Control warning during
  startup; a healthy daemon produces none.
- Targeted tests, `go test ./...`, and `go build -buildvcs=false ./...` pass.
