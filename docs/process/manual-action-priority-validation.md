# Validation: manual-action priority

## Specification and implementation review

The specification and its check are recorded beside this report. The user
explicitly requested verification, commit and installation. Independent fresh-
context review found stale cached poll-source acceptance, early manual delivery
priority acknowledgement, subscription source races and admission-session reuse
across a same-target cleanup wait. Corrections validate the same before/after
owner, fence poll subscription sources, preserve the admission session, and record
manual outcome provenance transactionally in existing topic/delivery journals.

## Regressions

- The blocked unrelated board read regression fails against the pre-change
  source: manual dispatch waits until the reader is released. The corrected
  implementation confirms the turn before background IO is released.
- Manual starts pass blocked read, thread list, subscribe and unsubscribe.
- Late read results cannot complete a new manual turn or cross a changed Sync
  session/poll connection. A later ID cannot reuse a cached pre-reconnect reader.
- Same-target pending writer unsubscribe precedes its new resume while an
  unrelated launch proceeds. Session/off/security changes during that cancellable
  target wait reject stale unsent admission.
- More than one historical selection batch cannot hide a new foreground card.
  A parked background card does not block another card, and same-card promotion
  cancels its background wait without backfilling a terminal notice.
- Explicit priority survives reopening and cannot be consumed by a late older
  render. Physical legacy-column removal/reopen preserves historical status.
- Early known-topic recovery cards cannot downgrade the actual success/failure;
  stale delivery completion cannot acknowledge a newer revision. Manual new-topic
  failure, interrupted creation and confirmation/materialization retain priority
  across process-reopen boundaries.
- Bot/HTTP/fake-clock integration exhausts the existing background budget with
  actual EditMessage calls; a manual SendMessage reaches HTTP before the parked
  next historical edit. Existing shared limit, critical acknowledgements, fairness
  and cooldown tests remain in use.

## Live QA

The opt-in live probe uses an isolated database and harmless ephemeral Codex turn.
It holds a passive background observation while the real shared App Server starts
the turn, then tests a deliberately hidden successful Telegram create response,
manual Telegram recovery and authenticated history readback of the connected
launch card and expected Final. It does not consume production getUpdates, replay
real work or restart the production daemon. Its own test topics/card are deleted.

## Release checks

Full tests, targeted race tests, build, secret/local scan and diff checks are run
on the isolated staged release before installation. Deployment evidence is kept
locally with the consistent database/binary backup; runtime IDs and credentials
are not included in this public repository.

The final clean staged release passed `go test -p 1 ./... -count=1`, targeted
`go test -race`, `go build -buildvcs=false ./...`, the CLI binary build,
`git diff --check` and the staged private-material scan. The required broad
keyword scan was inspected as code/config handling, not as a secret detector.
The live probe passed again on that exact release source, including authenticated
history readback and cleanup. Independent review and correction follow-up found
no remaining blockers. The later visible-button correction has a red/green
regression and is included in the final full/race/review snapshot.
