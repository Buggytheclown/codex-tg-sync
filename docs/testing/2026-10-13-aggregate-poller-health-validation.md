# Aggregate Poller Health Validation — 2026-10-13

## Candidate

Working-tree change on `feat/local-task-board`, based on
`797d99bc295b1aed6493987efca754971621d175`. No commit, push, installation, or
production daemon restart was performed.

Contract and implementation scope:
[notification brief](../process/poller-health-notification-brief.md),
`internal/daemon/health.go`, startup/delivery guards in
`internal/daemon/service.go`, and
`internal/daemon/external_poll_health_test.go`.

## Automated evidence

After the final implementation and removal of temporary QA instrumentation:

- `go test ./...` — passed.
- `go test -race ./internal/daemon -run
  'TestExternalPollHealth|TestHealth|TestPollers' -count=1` — passed.
- `go build -buildvcs=false ./...` — passed.
- `git diff --check` — passed.
- Repository secret-pattern scan was inspected; changed-file private-path,
  credential, and live-identifier scan passed.

Tests cover additional failures, partial recovery, new unconfirmed errors,
stale/unknown sources, parallel observations, restart, disable/re-enable, wake
grace, suppressed undelivered incidents, and legacy upgrade. Queued recovery
tests prove temporary health blockage retains a retryable receipt and stable
success eventually delivers exactly one recovery. Legacy tests retain old
unconfirmed errors, reuse delivered warning receipts, suppress pending source
notices, and prevent registration from opening incidents before configuration.

An independent reviewer inspected the brief, tests, diff, ADR-032, and product
contracts. Findings about queued recovery, sequential startup registration, and
unconfirmed legacy errors were fixed and re-reviewed. No substantive blockers
remained.

## Live evidence

Candidate health transitions used an isolated temporary database and synthetic
poller observations, with the actual Telegram Bot sender/egress path targeting
one newly created QA topic inside the configured private Sync forum. Telegram
ingress and App Server sessions were not started.

Observed sequence: first failure -> second failure -> first recovery -> final
recovery. Exactly one warning and one recovery were delivered; additional
failure and partial recovery produced no messages.

Readback through the Telegram user-session skill confirmed both messages,
their text, routing to the QA topic, and absence of duplicate health messages.
Bot API success alone was not used as the acceptance evidence.

The temporary topic, database, metadata, harness, and QA instrumentation were
removed. No live identifiers, tokens, sessions, private paths, or raw payloads
are stored in this report. The production daemon still runs its installed
version; deployment remains separate.
