# AFC Desktop Sync MVP

## Product Goal

AFC is the primary Telegram product surface. One private forum group mirrors
user-visible Codex chats as topics and lets the operator continue the same chat
from either Codex Desktop or Telegram. Legacy direct-message behavior remains
compatible and tested, but it is lazy and cannot mutate Codex while AFC is
active.

The MVP succeeds when a Desktop turn appears in its Telegram topic with
progress and a final answer, the next Telegram message continues the same Codex
thread, and new chats created on either side become visible on the other side.

## Runtime Architecture

Codex Desktop and `codex-tg` connect to one managed local Codex App Server
daemon. Both use the daemon Unix socket directly. `codex-tg` uses a standard
WebSocket client with a Unix-domain dialer because the control socket requires
an HTTP Upgrade and one JSON-RPC-shaped message per text frame.

Daemon mode is explicit and fail-closed. If the managed daemon is unavailable,
AFC reports a degraded/unavailable state and does not silently spawn a private
App Server. Spawned stdio remains supported for legacy compatibility and tests.

The implementation keeps the existing long-lived read/reconciliation client
and lazy control connections for the first slice. They share one underlying
daemon, so closing a connection never unloads
the authoritative Desktop runtime.

## Exclusive Telegram Modes

The Telegram adapter has three AFC states:

- `off`: AFC does no synchronization. Legacy DM mutations may start their
  connection lazily after an explicit user command.
- `active`: AFC owns Telegram interaction. DM `/status` and `/help` remain
  available, but every legacy mutation fails before an App Server call with an
  `AFC active - use the group` response.
- `draining`: no new AFC or legacy mutations start while active work reaches a
  guarded terminal state.

`/afc off` never restores the legacy observer or starts a legacy connection.
Later explicit DM work may start legacy lazily.

## Activation And Reconciliation

`/afc on` validates the configured private forum group and permanent Control
topic, verifies the managed daemon, then creates topics for the most recent
eligible top-level Codex chats. The default initial limit is five and is
configured with `CTR_GO_AFC_INITIAL_TOPIC_LIMIT`. Invalid or non-positive values
fall back to five.

While AFC is active, a bounded reconciliation loop uses `thread/list` to find
new persisted top-level chats. Archived, internal, and spawned sub-agent threads
remain excluded. Every eligible `threadId` has at most one current AFC topic.
The mapping is persisted before presentation work can race with another sync.

For a tracked chat, `thread/resume` is sent with only `threadId`; it acts as a
connection subscription to the shared daemon, not as process ownership. Live
events drive prompt, progress, tool, status, and final presentation, while
`thread/read` remains the durable catch-up and repair source. `/sync` triggers
the same idempotent reconciliation without requiring `/afc off` and `/afc on`.

Codex thread names flow to Telegram topic names. Telegram topic renames do not
rename Codex threads in the MVP.

## Message And Turn Semantics

Every Telegram source message gets a durable receipt before App Server
mutation. Duplicate delivery and ambiguous dispatch are never replayed.

- An idle topic message starts a new turn.
- A message sent during an active turn steers the expected active `turnId`.
- If active state is stale, AFC re-reads the thread and may start a new turn only
  after terminal evidence.
- `/stop` targets the authoritative `threadId + turnId` regardless of whether
  Desktop or Telegram started the turn.
- The shared App Server is the final concurrency arbiter when Desktop and
  Telegram act at the same time.

Local writer leases remain useful for Telegram idempotency, guarded callbacks,
and unknown dispatch. They no longer claim that one client connection
exclusively owns a Codex thread relative to Desktop.

## Chat Creation

Desktop-created chats are discovered by reconciliation, assigned one durable
topic, subscribed, and caught up from `thread/read`. If the first prompt is not
yet readable, AFC creates the normal placeholder and edits it in place later.

Telegram creation keeps the durable draft flow. `/newchat` or `/projects`
creates a draft topic; its first message performs `thread/start` and the first
`turn/start` on one control connection, persists the returned `threadId`, and
names both the Codex thread and Telegram topic. The new thread is immediately
visible to Desktop because both clients share the daemon.

## Failure And Recovery

- Daemon unavailable: fail closed with no private runtime fallback.
- Socket disconnect: reconnect, list/read tracked threads, restore subscriptions,
  and never replay prompts.
- Ambiguous mutation: keep the durable receipt unknown until reconciliation
  proves terminal or active state.
- Duplicate discovery: the durable `threadId` mapping wins and no second topic
  is created.
- Manually deleted topic: mark the mapping stale and retain the Codex thread;
  automatic topic recreation is outside the MVP.
- Daemon or bridge restart: restore mappings and subscriptions without duplicate
  topics or messages.

## MVP Non-goals

- Syncing archive/delete operations between Desktop and Telegram.
- Renaming Codex threads from Telegram topic edits.
- Queueing several future turns while one turn is active.
- More than one AFC group or human operator.
- Importing all historical chats during activation.
- Desktop-origin approval and structured-input actions in Telegram; they are a
  follow-up slice after base synchronization.

## Acceptance Scenarios

1. `/afc on` creates at most five initial topics by default and reports the
   configured limit in status/diagnostics.
2. A new Desktop chat becomes one topic within the reconciliation window and
   shows user prompt, elapsed progress, and final answer.
3. A Telegram follow-up appears in the same Desktop chat and its progress is
   visible on both surfaces.
4. `/newchat` creates a thread visible in Desktop after the first topic prompt.
5. A Telegram message during an active turn steers that exact turn.
6. `/stop` interrupts both Desktop-origin and Telegram-origin active turns.
7. Restart restores mappings and subscriptions without replay or duplication.
8. While AFC is active, legacy DM mutation fails before any App Server request;
   after `/afc off`, an explicit DM mutation may start legacy lazily.
9. Existing legacy and AFC tests continue to pass.
