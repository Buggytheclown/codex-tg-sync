# ADR-027: Sync Reset On Bridge Startup

- Status: accepted
- Amends: ADR-024, ADR-026 restart recovery
- Related: `docs/plans/2026-08-19-sync-reset-on-start-mvp-design.md`

## Context

Durable Sync writer recovery originally preserved `starting` and `active` work
as `ownership-unknown` after a bridge restart. That fail-closed rule prevents
prompt replay, but it also leaves an MVP operator unable to run safe `/sync off`
when the bridge restarts after the authoritative Codex turn has already
finished. A machine reboot can therefore preserve stale Telegram ownership
long after the shared App Server runtime is gone or has moved on.

The full solution would reconcile every durable receipt and writer lease with
authoritative App Server history. The MVP instead needs a small, predictable
restart boundary.

## Decision

- Every `codex-tg` process start atomically resets the current Sync session to
  `off` before background reconciliation starts.
- Topics and drafts from the previous session become cleanup-only. Topic-local
  active-turn and writer-generation fields are cleared.
- Accepted receipts from that session become `unknown` and remain durable for
  audit. They are never replayed.
- Non-Sync SQLite state is preserved. A Codex turn already running in the shared
  daemon is neither interrupted nor deleted.
- Telegram topic deletion runs only after the bot is ready. Failed deletion
  remains a durable cleanup target; Control is never deleted.
- Persisted App Server connection flags reset to disconnected before the new
  process attempts a connection.
- In explicit shared-daemon mode, one failed startup connection attempt sends
  one recovery warning to Control when the Sync forum is configured.
- Sync never reactivates automatically. The operator runs `/sync on` to create a
  fresh session after startup health is restored.

## Consequences

Bridge restart is a deliberate Telegram Sync session boundary. Existing Codex
work continues in Desktop, but Telegram does not resume its old topics or
writer ownership. This trades transparent restart continuation for predictable
MVP recovery and eliminates stale `/sync off` refusal after reboot.

ADR-026 shared-daemon reconciliation remains valid for connection repair
within one process lifetime. Its rule that a bridge restart preserves an active
`ownership-unknown` Sync session is superseded by this ADR.

## Non-goals

- Deleting the whole SQLite database.
- Interrupting authoritative Codex turns during bridge startup.
- Automatically enabling Sync after restart.
- Supervising or restarting Codex Desktop or the managed App Server daemon.
