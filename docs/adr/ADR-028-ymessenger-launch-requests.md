# ADR-028: YMessenger Launch Requests

- Status: accepted
- Related: ADR-019, ADR-020, ADR-021, ADR-025, ADR-026

## Context

The operator wants a Yandex Messenger robot mention to become an explicitly
approved Codex task in the existing AFC Telegram group. Running an external
scheduler would add another process and local API. Letting that process consume
the existing Telegram bot would also create two competing `getUpdates`
consumers. Direct shared-database integration would couple source code to
`codex-tg` storage and transaction details.

The target is one-command operation with source-specific code kept separate
from Telegram and App Server orchestration.

## Decision

- The first implementation is a modular monolith inside the existing Go
  daemon, not a sidecar.
- `internal/ymessenger` is an optional source adapter behind a small launch
  request sink. It does not import Telegram or App Server packages.
- The adapter polls Yandex Messenger Bot API updates and persists its update
  cursor in the existing SQLite store.
- Source ingestion inserts launch requests and advances the cursor in one
  transaction. `(source, external_id)` is unique.
- Authorization uses the configured sender-login allowlist plus an explicit
  configured robot entry in `mentioned_users`. Source chat id is not an
  authorization filter.
- Telegram approval is required by default. A source-specific configuration
  flag may auto-start allowed explicit mentions; the durable conditional claim
  is still stored before any external effect.
- Approval messages use the existing Telegram bot and one manually created,
  configured forum topic. That topic is not stored as an AFC topic or draft and
  is never renamed or deleted by AFC lifecycle cleanup.
- Approved work uses the existing App Server writer-ownership rules to create a
  thread and first turn. Existing AFC materialization owns the resulting normal
  session topic and subsequent live UI.
- App Server remains authoritative for thread and turn state. A dispatch with
  an ambiguous outcome is recorded as `outcome_unknown` and is never replayed
  automatically.
- Telegram delivery is reconciled from durable request state. Telegram failure
  never rolls back a committed request or App Server transition.
- Configuration extends the existing private `config.env`; the adapter is off
  by default and secrets remain redacted from public and diagnostic surfaces.
- Bot API reply payloads and observed top-level messages provide the only MVP
  context. Direct replies use `reply_to_message`; thread roots are resolved by
  `(chat_id, thread_id)` from a durable cache because Bot API does not return a
  thread parent or expose `getMessage`. History API and user tokens remain out
  of scope.
- The existing AFC App Server subscription and authoritative `thread/read`
  snapshot queue one Messenger reply for the exact external `(thread_id,
  turn_id)`. A durable retry worker sends the final answer as a reply to the
  invoking source message. Codex never receives the OAuthTeam token and does
  not choose the destination.

## Consequences

- `ctr-go` remains the only daemon the operator starts.
- Yandex Messenger code stays isolated and can later be removed or replaced
  without changing Telegram callback parsing or App Server transport.
- The launch-request domain is generic enough for another in-process source,
  but this change does not introduce a public plugin or scheduler framework.
- A permanent approval topic is an operator-managed prerequisite. Its absence
  causes delivery failure without losing the durable request when approval is
  enabled; it is optional for auto-start.
- Duplicate source delivery and duplicate callbacks are expected and safe.
- Exactly-once external execution is not claimed across an ambiguous App Server
  boundary; safety prefers a visible non-replayable state over duplicate work.

## Non-goals

- Generic cron or user-script execution.
- External write Control API.
- Shared database access from another process.
- Yandex Messenger History API or chat-id allowlisting for this flow.
- Automatic dispatch retry, topic recreation, or multi-operator routing.
- Full thread history, arbitrary message lookup, or replies to a destination
  selected by Codex.
