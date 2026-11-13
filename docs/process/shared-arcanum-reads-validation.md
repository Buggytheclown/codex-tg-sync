# Shared Arcanum reads — implementation validation

Date: 2026-11-13. Scope: shared-arcanum-reads-plan.md and
shared-arcanum-reads-spec.md. Implementation, local rollout and live read-only
validation are complete. The initial development checks below preceded
installation; the subsequent operational checks are described in the follow-up.

## Delivered behavior

- One SharedReader owns search, slow reads and status publication. Consumers
  register compact active demand and compare observations without network IO.
- Search has a persisted 30-second limit per exact query and shared projection.
  Comments/checks share a needed-only round with five-minute per-PR limits.
  Exceptional get has a ten-minute per-PR limit. A new diff, rearm, temporary
  demand removal and restart cannot reset an existing budget.
- Explicit pagination retains positive rows while distinguishing incomplete
  discovery. Each logical read is bounded; CLI internal retries are disabled.
  Longer exposed Retry-After delays extend the persisted deadline.
- SQLite publication preserves the latest successful status and latest attempt,
  with local retry. Terminal Merged is durable without requiring a PR card and
  wins over late open/error observations and older request start timestamps.
- Rules retain per-group watermarks and deferred state. Ready OR branches can
  act while optional resources wait or fail. AND groups require coherent facts.
  Waiting alone does not fabricate attempt/success timestamps. Runner evaluates
  new PR observations despite old task/rule deadlines; generation guards and
  transactional actions remain in storage.
- Regular compact API/SSE reads exclude Done before DTO/request projection.
  Explicit archive reads, deep links, linked references and version fences are
  preserved. Background UI signals do not reload Done lists/details. Done creates
  no dedicated Arcanum demand. Health registration uses compact active rows and
  disables prior archived registrations, including after daemon restart.
- PR editors display the actual read-only schedule and preserve old interval
  fields. Poller health uses resource deadlines, bounded slow queue estimates
  and terminal state. Browser-presence worker, endpoint and hook are removed.

## Evidence

Passed:

- `go test ./...`
- `go test -race ./internal/arcanumreview ./internal/polling ./internal/storage ./internal/daemon`
- `go build -buildvcs=false ./...`
- UI `npm test` — 223 tests in 14 files
- UI `npm run build`
- `git diff --check`, formatting and changed-file privacy inspection
- Fresh-context independent review, followed by fixes and focused rechecks

Counting fixtures and fake time prove five same-author status cards use 120
search commands over a one-hour steady run and zero get/comments/checks. Another
fixture proves comments starting at 00:00 and checks at 00:40 defer the next round
until 05:40 after restart. Reservation errors cannot start CLI work. SQLite
integration sends one search to both footer projection and future-deadline rules,
accepting one event per task while preserving the waiting OR branch.

Regression cases include terminal precedence, rearm, delayed slow facts after a
newer status observation, status changes away and back, fallback failures,
archive point reads and linked PR references, explicit Done loading, no background
archive/detail reload, failed deep-link retry, external reopen after an owned Done response and
explicit reload after a Done save conflict with the draft preserved. See docs/testing/regression-map.md for the test locations.

## Limits of validation

Native browser QA was attempted against an isolated fixture board, but the
browser access host was unavailable. The fixture server was stopped. Visual
layout and native window behavior remain unverified; UI behavior above is covered
by jsdom tests.

Arcanum API calls and production data were not used for the initial development
validation. Command counts are fixture measurements, not HTTP traces or measured
production load/latency. Multiple queries and pagination increase command count;
startup and slow-round bursts remain possible. The subsequent live read-only
traffic observation is documented below; an HTTP trace was not collected.

## Live rollout follow-up

The Go binary and UI assets were rebuilt and installed, then the configured
service was restarted with a private offline database/binary/UI backup. Build
hash and served asset readback matched. Sync session identity survived restart.
Live API reads confirmed default non-Done selection, explicit archive and point
reads without additional Arcanum demand. Operational data and full measurement
artifacts remain with the private service backup, outside the public checkout.

Start/finish diagnostic events now record physical CLI calls using method,
opaque resource key and original start time. They omit query text, PR IDs,
credentials and response content and follow the existing diagnostic-log switch.
This remains CLI-call evidence rather than an HTTP trace.

The live run exposed an unnecessary author query near a discovery query's
30-second boundary. A usable positive row from a healthy active discovery query
now owns its next refresh, avoiding this duplicate. An actual missing row or
failed discovery still permits author fallback. Regression tests cover both the
jitter case and a missing open-list row resolved to Merged by author search.
After installing that correction, live remeasurement retained only the needed
shared queries and respected the existing slow-resource deadline across restart.
The correction passed full Go tests, targeted race tests and independent review.
Native browser access remained unavailable; no visual readback is claimed.
