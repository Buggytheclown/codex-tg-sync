# Success Metrics

## Safety

- 100% of accepted Telegram inputs come from the configured Sync group and single allowed user.
- 0 out-of-scope messages or callbacks reach SQLite route lookup or App Server mutation.
- 0 duplicate turns from repeated Telegram updates, stale callbacks, reconnect, or ambiguous launch creation.
- 0 secrets or private ids in committed docs, fixtures, logs, or screenshots.

## Reliability

- Topic-to-thread routing remains stable across sync cycles.
- Current health and external terminal deliveries retry durably inside the Sync group.
- Poller status reports enabled state, current work, last attempt/success, and consecutive failures.
- Requests remain retryable/closable after a failed start.
- Shared App Server reconnect never replays an accepted prompt.

## Operator experience

- Command menu exists only in the Sync forum.
- Approval/input actions edit the original card and remove buttons.
- Every external request shows a `[Launch request]` card before manual or automatic start.
- Terminal external work produces a short source notification when replies are supported.
- `/status`, `/pollers`, and `/requests` answer from Control without hidden direct-message dependencies.

## Engineering

- Fresh databases contain no retired Telegram tables; upgrades remain non-destructive.
- Relevant targeted tests, `go test ./...`, race checks, and `go build -buildvcs=false ./...` pass before release.
- Current docs and setup describe only the Sync Telegram surface.
