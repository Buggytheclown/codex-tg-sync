# External Terminal Notifications And Arcanum Auto-start Brief

## Problem

An external launch card eventually reflects terminal state, but Telegram edits
are silent and the operator can miss completion. Arcanum review requests also
require the same manual launch approval even for explicitly trusted PR authors.

## Goal

- Keep the original `[Launch request]` card authoritative through terminal
  `Completed`, `Interrupted`, and `Run failed` states.
- Deliver one short durable Telegram notification for each newly observed
  terminal transition.
- Allow future Arcanum review requests from configured exact author logins to
  auto-start without launch approval.
- Require a visible Requests card before a configured-topic auto-start can
  claim or dispatch the request.

## Non-goals

- Copying the full Codex Final into the Requests topic.
- Changing App Server approval, reviewer, or sandbox permissions.
- Auto-starting already persisted pending PR requests after a config change.
- Adding a new delivery table or exactly-once remote Telegram delivery.

## UX / Operator Flow

- A trusted-author PR first renders a buttonless `[Launch request]` card with
  `Queued for automatic start`, then starts on a later delivery cycle.
- A non-trusted PR keeps `Dismiss` and `Start` approval actions.
- Terminal state edits the same card and removes actions.
- A short audible terminal notice identifies the outcome and request preview;
  the full Final remains in the managed session topic.

## Domain Model

- `CTR_GO_ARCANUM_REVIEW_AUTO_START_AUTHORS` is a comma-separated exact-login
  allowlist. Matching is case-insensitive after trimming whitespace and `@`.
- `ExternalLaunchRequest.AutoStart` remains the generic durable launch policy.
- The existing `delivery_queue` deduplicates terminal notices by stable event
  id and target. No external request schema changes are required.

## Architecture

- `internal/arcanumreview` owns author-policy matching and sets `AutoStart` on
  newly normalized PR requests.
- `internal/storage` gates configured-topic auto-start claims on a persisted
  Telegram message id.
- `internal/daemon` renders terminal cards, enqueues terminal notices before
  committing the terminal render marker, and delivers those notices audibly.

## Testing

- Config and Arcanum poller tests cover exact normalized allowlist matching.
- Storage tests prove configured-topic auto-start waits for card visibility.
- Daemon tests prove buttonless queued cards, terminal edits, durable notice
  deduplication, and audible delivery.
- Run targeted tests, `go test ./...`, `go build -buildvcs=false ./...`, secret
  scan, service restart, SQLite verification, and live Telegram readback.

## Acceptance Criteria

- [x] Trusted configured PR authors do not require Telegram launch approval.
- [x] Other PR authors still require explicit approval.
- [x] A configured-topic auto-start never dispatches before its card is saved.
- [x] Every new terminal transition edits the original card and queues one
      short audible notification.
- [x] Restart/re-render cannot duplicate a terminal notification.
- [x] No new database table or external-request schema migration is added.
