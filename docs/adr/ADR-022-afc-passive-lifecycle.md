# ADR-022: AFC Passive Lifecycle

- Status: accepted
- Related: ADR-002, ADR-020, ADR-021

## Context

AFC activation creates Telegram topics for a bounded snapshot of recent Codex
threads. It must coexist with legacy Telegram launches without sharing routing,
bindings, panels, or writer ownership. A crash can also occur after Telegram has
created only some topics.

## Decision

- AFC state uses a singleton `afc_state` row plus session-scoped `afc_topics`.
  AFC topics never appear in legacy bindings, observer targets, or panels.
- Exact AFC-group updates pass through an AFC router before every legacy
  message or callback handler. Unknown and cleanup topics fail closed.
- `/afc on` validates the private group, reads one fresh `thread/list`
  snapshot, filters archived/internal threads, sorts by
  `updated_at DESC, thread_id ASC`, and attempts at most eight topics once each.
- Every successful topic is persisted immediately. One or more successes make
  the session active and atomically disable the legacy global observer. Zero
  successes leave AFC off and do not change the observer.
- Passive sync uses only the poll session and `thread/read`: it edits one silent
  status message for the currently observed turn and sends each new Final as a
  notifying message. A different latest turn creates a fresh status message at
  the bottom of the topic; later snapshots of that turn edit the fresh message.
  This mirrors the legacy per-run presentation lifecycle without creating a
  legacy binding, panel, observer target, or writer. Passive sync never resumes
  a thread, starts a turn, or creates a writer.
- While an AFC writer lease is active, a poll snapshot for a different turn is
  stale presentation evidence and cannot replace or append after the active
  turn's status message.
- `/afc off` commits logical `off` before best-effort topic deletion. Failed
  deletes remain cleanup-only and unroutable. It does not restore the legacy
  observer and does not start, stop, or replace the legacy writer.
- Restart keeps an active session passive. An interrupted `activating` session
  becomes off and all known topics become cleanup-only.

## Consequences

- Partial activation is usable without pretending failed or ambiguous creates
  succeeded.
- Telegram cleanup failures cannot reopen AFC routing.
- Interactive AFC commands can be added later behind writer ownership without
  changing the passive lifecycle or legacy isolation contracts.
