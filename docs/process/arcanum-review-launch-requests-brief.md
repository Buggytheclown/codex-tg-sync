# Arcanum Review Launch Requests Brief

## Problem

The operator has to notice newly assigned Arcadia pull requests and manually
start the same read-only Codex review command each time.

## Goal

- Poll open published Arcanum pull requests assigned to one configured login.
- Exclude PRs authored by that login from assigned-review requests.
- Poll that login's open published PRs in `Waiting for changes` and automatically
  start one Codex feedback summary per PR, including PRs already in that state
  when polling starts.
- Create one durable Telegram launch request per source and pull request.
- Show the PR author, summary, and URL in the shared Requests topic.
- Send only the exact `$arc-pr-review-stefania <PR URL>` invocation for assigned-review requests.
- Keep Telegram approval as the default while allowing exact configured author
  logins to auto-start future requests after card visibility.
- Report sustained polling failures and recoveries in the same Requests topic.

## Non-goals

- Posting review comments, approving, or otherwise mutating the pull request.
- Creating more than one launch request per source for later PR iterations or reassignment.
- Parsing the Arcanum web UI or introducing a generic plugin framework.
- Returning the final answer to Arcanum.

## UX / Operator Flow

The optional poller reads the current assigned-review list through the official
`gena-arcanum-cli`. A newly observed PR appears in the existing Requests topic
with its author, title, and source link. Normal requests show `Dismiss` /
`Start`; trusted-author requests first show a buttonless `Queued for automatic
start` card and become claimable only after Telegram visibility is persisted.

## Domain Model

- Source identity: `arcanum_review`.
- Feedback-summary source identity: `arcanum_changes`; both sources use the
  configured login and poll interval.
- External identity: decimal PR id.
- `Sender`, `Title`, `SourceURL`, and `SafePreview` are Telegram-only metadata.
- `Prompt` is exactly `$arc-pr-review-stefania [<URL>](<URL>)`.
- Feedback-summary `Prompt` is exactly `<URL> кратко расскажи суть замечаний`.
- `CTR_GO_ARCANUM_REVIEW_AUTO_START_AUTHORS` applies exact normalized login
  matching to future requests only.
- SQLite `(source, external_id)` remains the restart-safe idempotency authority;
  an in-memory seen set avoids repeated writes during one process lifetime.

## Architecture

`internal/arcanumreview` owns CLI execution, JSON parsing, polling, and request
normalization. It depends only on the existing enqueue sink and does not import
Telegram, storage, or App Server packages. The generic external request renderer
owns the Telegram card.

## Testing

- CLI argument and JSON parsing tests.
- LaunchAgent-style child `PATH` normalization test.
- Exact prompt/display metadata and in-memory seen tests.
- Restart-style durable deduplication through the existing store.
- Disabled-by-default and configuration validation tests.
- Generic Telegram card rendering regression test.
- Targeted tests, `go test ./...`, and `go build -buildvcs=false ./...`.

## Acceptance Criteria

- [x] The current assigned PR creates one Requests card on the first poll.
- [x] Repeated polls and daemon restarts do not create duplicate cards.
- [x] The card shows author, title, and source URL.
- [x] Codex receives only the exact arc-pr-review-stefania invocation.
- [x] The adapter is optional and disabled by default.
- [x] Sustained polling failures and recoveries are delivered to Requests.
- [x] Trusted future PR authors may auto-start only after their card is visible.
- [x] The configured reviewer's own PRs are skipped by the assigned-review poller.
- [x] Own PRs in `Waiting for changes` start one feedback-summary task after
      their card is visible, regardless of later diff iterations.
