# Aggregate Poller Health Notifications

## Goal

One continuous incident across external pollers produces at most one warning
and one recovery in the configured Requests topic. Additional failing sources
and partial recoveries do not notify. `/pollers` retains per-source diagnostics.

## Behavior

- Preserve the existing wake grace, sustained-failure threshold, and stable
  recovery threshold for each source.
- The first confirmed failure opens `health.external.pollers` through the
  existing durable health episode and delivery queue.
- Even an unconfirmed error blocks aggregate recovery until that source has
  demonstrated stable successful polling.
- Recovery requires fresh successful observations from every enabled source;
  missing or stale results cannot establish health.
- Disabled sources do not block recovery. Disabling the last source invalidates
  the incident silently; disabling alone never queues a recovery.
- Serialize source observations, registration changes, and aggregate decisions
  with the existing health mutex. No new schema, dependency, or scheduler.
- Before startup delivery, move open legacy source incidents into the aggregate
  tracker, retain unconfirmed source errors, and invalidate old per-source
  warning/recovery deliveries. Reuse a
  delivered legacy warning receipt so upgrade does not repeat a warning. If no
  legacy warning was delivered, the next live observation can enqueue one
  aggregate warning instead. Registration alone cannot open an incident before
  current configuration has been applied.
- Restarts preserve the aggregate episode. Existing delivery rules suppress
  recovery without a delivered warning and notices superseded by a new episode.
- Recheck current source health before delivering a queued aggregate recovery.
  A new error or missing/stale observation delays that same receipt without
  consuming Telegram send attempts; a later stable success allows delivery.

## Validation

`internal/daemon/external_poll_health_test.go` covers multiple failures, partial
and unconfirmed recovery, fresh/unknown observations, concurrent results,
restart, disable/enable, sleep/resume, unavailable delivery, and idempotent
upgrade of delivered/pending legacy warnings and queued recoveries, unconfirmed
legacy errors, and changes in health between enqueue and recovery delivery.

Run focused health tests, targeted race tests, `go test ./...`,
`go build -buildvcs=false ./...`, diff/secret checks, and fresh-context review.
Candidate Telegram readback requires an isolated live validation contour;
the production daemon is restarted only when deployment is requested.
