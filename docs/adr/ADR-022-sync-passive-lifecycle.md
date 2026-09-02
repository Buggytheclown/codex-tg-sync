# ADR-022: Sync Passive Lifecycle

- Status: accepted
- Related: ADR-002, ADR-020, ADR-021

## Context

Sync activation creates Telegram topics for a bounded snapshot of recent Codex
threads. It must coexist with legacy Telegram launches without sharing routing,
bindings, panels, or writer ownership. A crash can also occur after Telegram has
created only some topics.

## Decision

- Sync state uses a singleton `sync_state` row plus session-scoped `sync_topics`.
  Sync topics never appear in legacy bindings, observer targets, or panels.
- Exact Sync-group updates pass through an Sync router before every legacy
  message or callback handler. Unknown and cleanup topics fail closed.
- `/sync on` validates the private group, reads one fresh `thread/list`
  snapshot, filters archived/internal threads, sorts by
  `updated_at DESC, thread_id ASC`, and attempts at most eight topics once each.
- Every successful topic is persisted immediately. One or more successes make
  the session active and atomically disable the legacy global observer. Zero
  successes leave Sync off and do not change the observer.
- Passive sync uses only the poll session and `thread/read`: it edits one silent
  status message for the currently observed turn and sends each new Final as a
  notifying message. A different latest turn creates a fresh status message at
  the bottom of the topic; later snapshots of that turn edit the fresh message.
  This mirrors the legacy per-run presentation lifecycle without creating a
  legacy binding, panel, observer target, or writer. Passive sync never resumes
  a thread, starts a turn, or creates a writer.
- Sync reuses the observed-turn timing and bounded active-turn refresh lifecycle
  without reusing legacy delivery state. Status renders from the compacted
  snapshot. While a turn is active, the header timer and open-block durations
  advance in ten-second buckets, so elapsed-only snapshots inside the same
  bucket do not cause Telegram edits. Content and state changes may still edit
  immediately. Terminal durations use the authoritative timestamps without
  bucketing and remain stable on later polls.
- After a successful Sync topic prompt or steer acknowledgement, the current
  same-turn live status is best-effort deleted and recreated at the bottom of
  the topic. Later progress edits that tail message. Status history from older
  turns is retained.
- Telegram-origin Sync turns get the same bounded three-second hot-poll cadence
  as legacy turns. Normalized live tool evidence uses the same overlay and
  preservation rules so a lagging `thread/read` cannot immediately erase a
  fresher same-turn tool update. Desktop-origin turns remain on passive Sync
  polling and never acquire writer ownership.
- While an Sync writer lease is active, a poll snapshot for a different turn is
  stale presentation evidence and cannot replace or append after the active
  turn's status message.
- `/sync off` commits logical `off` before best-effort topic deletion. Failed
  deletes remain cleanup-only and unroutable. It does not restore the legacy
  observer and does not start, stop, or replace the legacy writer.
- Restart keeps an active session passive. An interrupted `activating` session
  becomes off and all known topics become cleanup-only.

## Consequences

- Partial activation is usable without pretending failed or ambiguous creates
  succeeded.
- Telegram cleanup failures cannot reopen Sync routing.
- Interactive Sync commands can be added later behind writer ownership without
  changing the passive lifecycle or legacy isolation contracts.
- Presentation behavior can stay aligned with legacy mode while Sync retains
  separate Telegram ids, callbacks, routing, and ownership.
