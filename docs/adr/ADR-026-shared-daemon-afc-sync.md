# ADR-026: Shared Daemon AFC Sync

- Status: accepted
- Amends: ADR-019, ADR-020, ADR-022, ADR-023, ADR-024
- Related: ADR-025, `docs/plans/2026-08-16-afc-desktop-sync-mvp-design.md`

## Context

Separate Desktop and `codex-tg` App Server processes can load the same durable
thread independently. Process-local writer claims protect two bridge writers,
but cannot coordinate Desktop and produce `already has an active writer` or
stale rollout behavior. Passive `thread/read` polling also cannot provide the
same live lifecycle as a client subscribed to the runtime that owns the turn.

AFC is now the primary Telegram product surface. It must mirror user-visible
Desktop chats, accept follow-ups in the same thread, and create chats that
Desktop sees without maintaining a second App Server runtime.

## Decision

- Desktop and `codex-tg` use one managed local App Server daemon as the runtime
  authority.
- `codex-tg` daemon mode connects through `codex app-server proxy`; spawned
  stdio remains an explicit compatibility mode.
- Daemon mode never silently falls back to spawned stdio when the daemon is
  unavailable or incompatible.
- Proxy process lifetime is client-connection lifetime, not thread ownership or
  App Server lifetime.
- A tracked shared-daemon thread is subscribed with `thread/resume` containing
  only `threadId`. CWD and history options are not added to resume.
- Live events are preferred for presentation and `thread/read` remains the
  durable reconciliation source.
- AFC reconciliation continuously materializes newly created eligible
  top-level Desktop threads, using App Server `createdAt` rather than treating
  activity in an old thread as a new chat. Activation creates only a bounded recent snapshot;
  `CTR_GO_AFC_INITIAL_TOPIC_LIMIT` defaults to five.
- Durable `threadId` mapping makes topic creation idempotent. New Telegram chats
  retain the draft-first `thread/start + turn/start` contract from ADR-025.
- Local writer leases continue to guard Telegram dispatch, receipts, callbacks,
  and unknown results. They do not represent exclusive ownership relative to
  Desktop when all clients share the daemon.
- `/stop` may target the authoritative active turn regardless of origin. A
  normal topic message steers an active expected turn and starts a new turn only
  after terminal or stale-active recovery evidence.
- AFC active and legacy DM mutation are mutually exclusive. Legacy code remains
  lazy and compatible; `/afc off` does not restore it automatically.

## Consequences

- Desktop and Telegram observe and mutate the same in-memory thread state.
- Thin proxy connections may remain separate without recreating the
  cross-process writer problem.
- ADR-020 process-generation claims remain valid inside spawned-stdio mode and
  for bridge-local dispatch safety, but are not a distributed ownership claim
  against Desktop in shared-daemon mode.
- ADR-022's prohibition on passive `thread/resume` applies to spawned separate
  runtimes. In shared-daemon mode, exact-id resume is the subscription mechanism
  and must not override CWD or history.
- ADR-024's Telegram-origin restriction on Stop is superseded in shared-daemon
  AFC mode; guarded coordinates and terminal confirmation still apply.

## Non-goals

- Public or remote App Server exposure.
- Direct Go WebSocket transport in the MVP.
- Simultaneous AFC and legacy Telegram mutation.
- Automatic archive/delete or manual-topic repair synchronization.
- Automatic replay after ambiguous dispatch.
