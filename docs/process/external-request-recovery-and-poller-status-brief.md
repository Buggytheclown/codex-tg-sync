# External Request Recovery And Poller Status Brief

## Problem

After an approved external launch fails, its Telegram card has no recovery
action. The operator also cannot inspect which in-process source pollers are
enabled, whether they still complete cycles, or which launch requests still
need attention.

## Goal

- A launch failure known to have happened before a first Codex turn can be
  retried exactly once per operator action or closed.
- An ambiguous dispatch remains fail-closed and can be closed, but is never
  replayed automatically.
- `/requests` lists launch requests that still need operator attention or have
  an active Codex turn.
- `/pollers` reports durable source-poller observations and makes stale workers
  visible after a daemon crash or hang.

## Non-goals

- Attempt-history storage or exactly-once execution across App Server network
  ambiguity.
- Cancelling a dispatch while it is inside the `starting` critical section.
- Replaying `outcome_unknown` requests.
- A generic scheduler, worker registry, or new health database schema.

## UX / Operator Flow

- A `Failed` launch card exposes `Close` and `Retry`.
- An `Outcome unknown` card exposes `Close` only and explains that it was not
  retried automatically.
- `Retry` conditionally moves the same durable request from `failed` to
  `starting`, clears the previous terminal error/reply slot, edits the same
  Telegram card, and dispatches once.
- `Close` conditionally moves `pending_approval`, `failed`, or
  `outcome_unknown` to `dismissed` and removes all buttons.
- `/requests [active|running|failed|all]` renders recent matching requests.
- `/pollers` renders enabled state, interval, current cycle, last successful
  cycle, consecutive failures, and the last sanitized error.

## Domain Model

- Existing `external_launch_requests.status` remains the request authority.
- Terminal Codex turns use `session_completed`, `session_interrupted`, or
  `session_failed`, so completed work does not stay in the active list.
- Poller observations are JSON values under `daemon_state` keys named
  `poller.<source>`.
- Startup reconciles legacy `session_started` rows against exact stored
  `(thread_id, turn_id)` terminal snapshots or a valid newer turn id in the
  same thread, and invalidates rendered error-card markers so upgraded cards
  gain their recovery buttons.
- Every action and terminal transition is a conditional SQLite update from the
  expected prior state.

## Architecture

- `internal/storage` owns request queries and atomic transitions.
- `internal/daemon` owns card actions, request/poller command rendering, and
  mapping authoritative App Server terminal snapshots to request state.
- Source pollers report cycle start and result through their existing sink
  boundary. They do not import Telegram code.

## Testing

- Storage tests cover conditional retry, close, active listing, and terminal
  turn completion.
- Daemon tests cover card buttons, stale callbacks, `/requests`, `/pollers`,
  and durable poller observations.
- Poller tests prove every run reports cycle start and result.
- Run `go test ./...`, `go build -buildvcs=false ./...`, diff checks, and the
  repository secret/local scan.
- Live Telegram callback/readback is required when the configured contour is
  available.

## Acceptance Criteria

- [x] Double-clicking Retry dispatches at most once.
- [x] Failed and ambiguous requests can be closed from the same card.
- [x] Ambiguous requests cannot be retried.
- [x] Terminal Codex turns disappear from `/requests active`.
- [x] `/pollers` distinguishes healthy, failing, stale, waiting, and disabled.
- [x] No database schema migration or new table is introduced.
