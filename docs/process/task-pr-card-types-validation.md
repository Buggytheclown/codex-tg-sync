# Task / PR / Schedule validation

Date: 2026-11-11. Implements the agreed card-type specification and plan.

## Automated checks

- `go test ./...`.
- Targeted race suites: storage, controlapi, polling, daemon, arcanumreview.
- `go build -buildvcs=false ./...` and production daemon build.
- Clean `npm ci`, UI tests (215 tests), TypeScript/Vite production build.
- Whitespace and public-diff checks; private migration artifacts stay outside
  the repository. Dependency manifests and lockfile are unchanged.

One broad race run encountered a queued-delivery assertion in the unchanged
external poll-health test. Ten isolated repetitions and a full daemon race rerun
passed. Neither the health implementation nor that test was changed.

## Behavior checks

Storage/API tests cover explicit types, Task-only Review, manual parent links,
canonical PR reuse across source families without another launch, frozen-request
and authoritative-chat protection on PR replacement, and passive status reads.
Existing polling completion, approval, source, ordering and Done suites remain.

UI tests distinguish the two chats, navigate from a filtered Task's PR row,
propagate a PR move to that row, preserve relationship drafts on conflict and
support both Arc URL host forms, including successful same-ID edits.

An isolated HTTP/SQLite fixture additionally exercised compact reads, status
projection, unlink/relink, invalid PR → Review rejection, a PR move with unchanged
parent/chat and serving the production static bundle. It did not launch chats or
modify user PRs.

Migration was rehearsed on a consistent copy of the live database. All card IDs,
unchanged legacy fields, old events with their rowids, frozen requests, polling
and other existing tables were preserved. Only approved reference archival and
new type/schema fields changed. Repeated apply, foreign_key_check and
integrity_check passed. Regression fixtures additionally cover failed History
rollback, old/empty schemas, missing Done/source columns and changed legacy
template definitions between preview and apply.

## Independent review

Fresh backend and UI reviewers examined the implementation separately. Their
findings were corrected and rechecked: earlier source/Done upgrades and empty
schema detection, legacy import identity, referenced-template digest, canonical
URL save and search. No material findings remained in those reviewed areas.

## Limits

The browser access host was unavailable. Real browser visual acceptance at narrow
widths, focus/drag geometry and motion is therefore unverified; DOM tests and the
HTTP fixture do not substitute for that check. No Telegram messages were sent
as QA, and no new live Codex run was requested. Live post-update checks use
read-only daemon/API observations and database preservation comparisons.
