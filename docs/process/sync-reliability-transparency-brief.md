# Sync Reliability And Transparency Brief

## Problem

The happy path is durable, but several asynchronous failures remain visible only
in daemon logs. A bad YMessenger thread-root lookup can block later updates,
Sync can retain stale turn ownership, source requests can finish silently, and a
dead or half-open App Server transport can still look connected.

## Goal

- Every accepted YMessenger request receives one durable acknowledgement and
  one durable terminal outcome.
- An explicit robot mention from a disallowed sender receives a static policy
  reply without creating Telegram approval or Codex work.
- Sync never loses a prompt at the terminal-turn boundary and does not retain a
  lease after an authoritative superseding turn.
- Important infrastructure incidents produce one durable Control warning and
  one recovery notice without repeated-message spam.
- A managed-daemon transport failure is detected, reported, and handled as a
  disposable Telegram projection: Sync resets to `off`, no prompt is replayed,
  Codex runtime work continues, and the poll session reconnects.

## Non-goals

- Preserving Telegram topics across daemon or transport failure.
- Rebinding active writer leases to a replacement WebSocket.
- Automatically replaying prompts or automatically enabling Sync after repair.
- Uploading Telegram or YMessenger attachments to Codex.
- A generic external-message outbox or a new health database schema.
- Changing spawned-stdio ownership semantics.

## UX / Operator Flow

- Plain captions are accepted as prompt text. Unsupported media receives an
  explicit reply instead of silence.
- YMessenger acknowledgement text is short and static. Terminal failures use a
  small sanitized status matrix.
- Disallowed explicit mentions receive: the bot is owner-only and no Codex
  session was started. The owner login and allowlist are never disclosed.
- Control receives actionable warning/recovered messages for YMessenger poll,
  App Server transport/session, external reply dead-letter, and Sync reset.
- After transport recovery Control says Sync remains off and instructs the
  operator to run `/sync on`.

## Domain Model

- `external_launch_requests` gains a fixed durable `ack_*` delivery slot; the
  existing `reply_*` slot remains the terminal delivery.
- `rejected_sender` is a terminal external-request state that can only send the
  policy acknowledgement and can never dispatch.
- Health episodes use namespaced `daemon_state` keys and deterministic delivery
  event ids.
- Transport-triggered Sync reset mirrors the ADR-027 startup boundary and keeps
  unfinished receipts non-replayable.

## Architecture

- `internal/ymessenger` classifies explicit mentions and bounds root lookup
  failures without importing Telegram or App Server code.
- `internal/storage` owns atomic request/cursor/delivery transitions.
- `internal/daemon` owns health policy, Sync lifecycle, source delivery, and
  transport reset orchestration.
- Existing `delivery_queue` delivers Control health messages. Existing external
  request rows deliver YMessenger ack/terminal messages.
- App Server remains authoritative for threads and turns.

## Testing

- Unit tests for root lookup degradation, unauthorized classification, fixed
  delivery slots, duplicate updates/callbacks, and every terminal outcome.
- Sync regression tests for stale steer, superseding turn, media rejection,
  startup notice, and transport reset.
- App Server tests for critical close delivery, pending RPC failure, heartbeat,
  and stale-generation safety.
- Full `go test ./...` and `go build -buildvcs=false ./...`.
- Live Telegram/App Server restart QA remains required before release.

## Acceptance Criteria

- [x] One bad YMessenger root cannot block unrelated later requests forever.
- [x] Accepted requests have one ack and one terminal delivery record.
- [x] Unauthorized explicit mentions receive one policy reply and make zero
      Telegram approval, `thread/start`, and `turn/start` calls.
- [x] Sync terminal-boundary input is steered or starts exactly one new turn.
- [x] Authoritative supersession releases stale local Sync ownership.
- [x] Health warnings and recovery notices are durable and deduplicated.
- [x] Sleep-sized polling gaps and short network transitions do not create
      flapping warnings or out-of-order recovery notices.
- [x] Managed-daemon transport loss makes status truthful, resets Sync to off,
      never replays input, and reconnects the poll session.
