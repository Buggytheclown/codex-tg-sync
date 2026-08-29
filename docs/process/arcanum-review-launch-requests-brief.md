# Arcanum Review Launch Requests Brief

## Problem

The operator has to notice newly assigned Arcadia pull requests and manually
start the same read-only Codex review command each time.

## Goal

- Poll open published Arcanum pull requests assigned to one configured login.
- Create one durable Telegram launch request per pull request.
- Show the PR author, summary, and URL in the shared Requests topic.
- Send only the exact `$arc-pr-review <PR URL>` invocation to Codex.
- Keep Telegram approval mandatory.
- Report sustained polling failures and recoveries in the same Requests topic.

## Non-goals

- Posting review comments, approving, or otherwise mutating the pull request.
- Creating more than one launch request for later PR iterations or reassignment.
- Parsing the Arcanum web UI or introducing a generic plugin framework.
- Returning the final answer to Arcanum.

## UX / Operator Flow

The optional poller reads the current assigned-review list through the official
`gena-arcanum-cli`. A newly observed PR appears in the existing Requests topic
with its author, title, source link, and `Dismiss` / `Start` buttons. `Start`
uses the existing durable AFC dispatch path.

## Domain Model

- Source identity: `arcanum_review`.
- External identity: decimal PR id.
- `Sender`, `Title`, `SourceURL`, and `SafePreview` are Telegram-only metadata.
- `Prompt` is exactly `$arc-pr-review [<URL>](<URL>)`.
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
- [x] Codex receives only the exact arc-pr-review invocation.
- [x] The adapter is optional and disabled by default.
- [x] Sustained polling failures and recoveries are delivered to Requests.
