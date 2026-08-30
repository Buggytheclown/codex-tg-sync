# ADR-031: Arcanum Review Launch Requests

- Status: accepted
- Amends: ADR-028, ADR-029
- Related: ADR-020, ADR-026, ADR-030

## Context

An operator wants each newly assigned open Arcadia pull request to enter the
existing durable Telegram launch flow and start the installed `$arc-pr-review`
skill. Most authors require explicit approval, while a small operator-owned
allowlist may auto-start without making the launch invisible. Parsing the
Arcanum web UI is fragile, while owning another OAuth implementation or a
second daemon would duplicate local authentication and lifecycle
responsibilities.

## Decision

- `internal/arcanumreview` is an optional in-process source adapter.
- The adapter invokes the official `gena-arcanum-cli` through a configured `ya`
  binary and searches with
  `open(true);published(true);assignee(<configured-login>)`.
- When the configured `ya` path is absolute, its directory is prepended to the
  child process `PATH`. This supports `ya` token helpers under macOS
  LaunchAgents, whose default `PATH` omits `/usr/local/bin`.
- The configured executable itself must be readable by the service. In
  particular, an otherwise valid symlink into a macOS privacy-protected
  `Documents` tree may require copying the bootstrap to a service-owned path.
- Polling is sequential and defaults to once per minute. The adapter is
  disabled by default and requires an absolute working directory, the shared
  external Requests topic, and Sync group configuration when enabled.
- PR identity is its decimal id under source `arcanum_review`. The existing
  unique `(source, external_id)` constraint is the restart-safe idempotency
  authority for every request state.
- An in-memory monotonic seen set suppresses repeated SQLite writes while the
  process is running. Ids enter that set only after the enqueue transaction
  succeeds. After restart, the first current snapshot is offered to SQLite
  once; existing ids conflict harmlessly and then populate the new seen set.
- Telegram display metadata and executable prompt remain separate. `Sender`
  contains the PR author, `Title` the PR summary, `SourceURL` the canonical PR
  URL, and `SafePreview` a short review action. `Prompt` contains only the exact
  `$arc-pr-review [<URL>](<URL>)` invocation.
- The generic external request card renders `Source`, `From`, `Title`, `Status`,
  `Link`, and `Request`. Source adapters do not render Telegram text or own
  callback/status behavior.
- Telegram approval is the default. An optional comma-separated exact-login
  allowlist marks future PR requests for auto-start; author matching is
  case-insensitive after trimming whitespace and a leading `@`. Existing
  persisted pending requests are not reclassified after config changes.
- An auto-start with a configured Requests topic is not claimable until the
  initial buttonless `[Launch request]` card has a persisted Telegram message
  id. The card says `Queued for automatic start`; a later delivery cycle claims
  and dispatches it through the same durable Sync path.
- No final answer is posted back to Arcanum. Terminal state edits the original
  card and the generic external delivery contract emits one short durable
  Requests-topic notification without copying the full Final.
- External polling health messages are source-neutral, use the shared Requests
  topic, and retain the existing resume warm-up, failure delay, recovery delay,
  durable delivery, and General-topic fallback when the Requests topic is gone.
  The observation-gap threshold exceeds one 30-second CLI timeout plus the
  one-minute poll interval, so sustained slow failures cannot be mistaken for
  repeated laptop resumes.

## Consequences

The daemon performs a small snapshot query rather than maintaining an Arcanum
cursor. This is necessary because an older PR may be assigned after newer PR
ids already exist. Repeated snapshots are cheap and safe, and the official CLI
continues to own internal authentication.

One PR creates at most one durable launch request. A later iteration,
unassignment/reassignment, title edit, or re-publication does not create a new
request or mutate the original card. Supporting per-iteration review requests
would require a different external identity contract.

The configured working directory must allow the `arc-pr-review` skill to create
its isolated temporary Arc mount when one is necessary. The skill remains
read-only with respect to the reviewed PR and the user's primary Arc checkout.

## Non-goals

- Posting comments, approving, shipping, or changing the pull request.
- Polling review iterations, issue replies, or CI transitions independently.
- A generic polling plugin protocol or arbitrary Telegram card renderer.
- A native Arcanum OAuth client, HTML scraping, or webhook service.
- Wildcard, team, or display-name author trust policies.
