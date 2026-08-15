# ADR-023: AFC Concurrent Turn Ownership

- Status: accepted
- Related: ADR-020, ADR-022

## Context

Managed AFC topics must accept prompts concurrently without allowing two turns
in one topic, duplicating Telegram updates, or racing legacy launches for the
same Codex thread. A request timeout after `turn/start` may mean the call was
executed even though no response arrived.

## Decision

- Telegram passes the source `message_id` to the AFC router. The router inserts
  an `accepted` receipt before any App Server call; its primary key is
  `chat_id + topic_id + message_id`. Receipts contain routing ids and state but
  never prompt text.
- Duplicate source messages return their durable receipt state and never call
  App Server again. `unknown` is never replayed automatically.
- One lazy shared `afc` WriterManager owns all managed topic turns. Different
  threads may hold active leases in the same process; the same thread/topic may
  hold only one unfinished lease.
- AFC and legacy managers share one `ThreadClaimRegistry`. Cross-manager claim
  conflict is rejected before `thread/resume` or `turn/start`; claims remain
  until the owning process closes.
- AFC persists `starting` before `thread/resume`, then atomically records the
  receipt as `dispatched` and topic turn as `active` after a successful
  `turn/start` response.
- Timeout, EOF, canceled response wait, broken pipe, or a success response
  without a turn id produces `unknown`; the lease remains fail-closed. A
  definitive RPC rejection produces `rejected` and releases the reservation.
- Events are accepted only for the current process generation, session, thread,
  topic, and turn. Terminal evidence from either the writer event path or
  passive `thread/read` passes through the existing Telegram-origin terminal
  gate before it can release that lease. A transient implicit `interrupted`
  remains active and pollable through the grace window; recovery resumes normal
  progress delivery, while expiry confirms terminal. Explicit topic Stop and
  force-off interrupts bypass the grace window. The shared process closes only
  after the last lease is confirmed terminal.
- Restart changes unfinished `accepted/starting/active` state to `unknown` but
  does not create a writer and does not replay input.
- Until safe/force draining is implemented, `/afc off` rejects while any AFC
  lease is starting, active, or unknown.

## Consequences

- Two topics can run overlapping turns with one App Server process and
  serialized JSON-RPC writes.
- A lost Telegram update or ambiguous App Server response cannot duplicate a
  prompt.
- Legacy and AFC launches have a symmetric, testable ownership boundary.
