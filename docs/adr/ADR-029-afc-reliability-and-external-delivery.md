# ADR-029: AFC Reliability And External Delivery Transparency

- Status: accepted
- Amends: ADR-027, ADR-028
- Related: ADR-019, ADR-020, ADR-026

## Context

The reset-on-start AFC boundary is intentionally simple, but an in-process
managed-daemon transport failure could still leave Telegram state looking
active. YMessenger launch requests also had durable execution claims but no
durable acknowledgement, and some terminal outcomes could remain visible only
in logs. A repeatedly failing History root lookup could block every later
update behind one message.

## Decision

- `external_launch_requests` has two fixed delivery slots: `ack_*` for the
  source acknowledgement and `reply_*` for the terminal outcome. This is not a
  generic outbox. Each slot is claimed conditionally, retried with bounded
  backoff, recovered from `sending` after restart, and becomes `dead` after the
  configured attempt limit.
- Allowed YMessenger mentions persist their acknowledgement in the same
  transaction as the request and source cursor. Dismissal, dispatch failure,
  ambiguous dispatch recovery, and terminal App Server snapshots persist a
  source reply. A terminal turn without final text gets a small explicit
  fallback instead of silence.
- An explicit robot mention from a disallowed sender persists only a
  `rejected_sender` policy acknowledgement. It stores no Codex prompt, creates
  no Telegram approval topic, and cannot enter auto-start or dispatch.
- History authorization failures (`401`/`403`) remain global and never advance
  the cursor. Other failures for one exact root retry twice; on the third
  failure the request advances with an explicit unavailable-context marker so
  unrelated later updates are not blocked forever.
- Important failure episodes use namespaced `daemon_state` and the existing
  durable Telegram delivery queue. External polling waits through a four-minute
  resume warm-up, requires one minute of continuous failure, and requires 30
  seconds of continuous success before recovery. A polling gap longer than 90
  seconds resets the warm-up, treating sleep and DarkWake as unknown rather
  than failed service time.
- Health deliveries carry their episode identity. A stale warning is
  superseded instead of sent, and a recovery is sent only when its matching
  warning was already delivered. This prevents delayed queue retries from
  rendering recovery before failure or replaying an older episode after a
  newer one.
- Diagnostic events, durable delivery retry metadata, and raw App Server thread
  and snapshot projections sanitize Telegram Bot API credentials before writing
  logs or SQLite.
- A launch failure that is known to precede the first Codex turn may be retried
  by an explicit operator action. Retry is a conditional durable transition
  from `failed` to `starting`; ambiguous outcomes remain non-replayable.
- Failed and ambiguous launch requests may be closed explicitly. Closing an
  ambiguous request stops tracking it and does not claim that no Codex thread
  exists.
- Source pollers persist cycle observations in the existing `daemon_state`
  table. Operator status derives liveness from the observation age and poll
  interval, so a stale process cannot remain visibly healthy.
- Startup reconciles an existing `session_started` request when its exact
  durable `(thread_id, turn_id)` snapshot is terminal, or when a valid newer
  turn id in the same thread proves that the original turn is no longer
  active. It also invalidates the Telegram render marker for failed and
  ambiguous requests so an upgrade adds recovery controls to already-rendered
  cards.
- In managed-daemon mode, a periodic bounded `thread/list` heartbeat detects a
  half-open poll connection. Transport loss makes the connection status false,
  requests poll repair, and applies the ADR-027 reset boundary immediately:
  AFC becomes `off`, old topics/drafts become cleanup-only, accepted receipts
  become `unknown`, and no input is replayed. Closing bridge WebSocket clients
  does not interrupt the authoritative Codex runtime.
- Transport repair never re-enables AFC. A recovery notice tells the operator
  to run `/sync on` explicitly.
- Telegram captions are accepted as text. A supported message containing only
  media receives an explicit plain-text-only response after normal
  authorization checks.

## Consequences

Source delivery and Telegram projection state are inspectable and retryable
without adding a new subsystem. The implementation accepts at-least-once
network delivery at the crash boundary: the local slot prevents duplicate
claims, but a remote service may accept a message immediately before the local
completion write fails.

A single missing History root can lose only that root context after three
attempts; the user request remains explicit about the loss. Managed-daemon
connection loss sacrifices Telegram continuity, matching process restart, in
exchange for truthful state and no replay ambiguity.

Short network transitions after host resume are intentionally not operator
incidents. A persistent external polling outage may take up to five minutes
after resume to become visible, trading alert latency for a stable Control
signal on laptops.

## Non-goals

- Exactly-once delivery across remote API and SQLite commit boundaries.
- Preserving or rebinding AFC topics across transport loss.
- Automatically replaying prompts or re-enabling AFC.
- A generic external outbox or separate health database schema.
- A launch-attempt history table or automatic replay of ambiguous dispatch.
- Uploading Telegram or YMessenger attachments to Codex.
