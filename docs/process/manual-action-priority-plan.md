# Implementation plan and verification

1. Keep the existing Telegram governor and all its limits. Classify first cards,
   changed execution outcomes and explicit recovery results as foreground;
   unchanged-status repaints are background. Select foreground before a historical
   batch. Transport plain card edit options explicitly through Sender.
2. Keep SQLite as the only due-work journal. The existing foreground render loop
   can progress alongside one bounded background worker. One in-flight owner per
   request prevents conflicting edits; promotion cancels that request's parked
   background attempt. Durable manual/rendered priority versions prevent an early
   or stale render from consuming a newer action. A topic delivery request marker
   uses the existing delivery revision to promote the actual manual Retry result.
3. Release syncMu during background read/list/resume/unsubscribe and transport
   close. Compare the exact before/after session, source/poll generation, route,
   writer reservation, continuation turn and observed terminal state. Discard stale
   results. Reload routes after list/read boundaries. Serialize only same-target
   connection mutations; revalidate admission after such a cancellable wait.
4. Preserve execution journals, callback guards, terminal notice deduplication,
   snapshot monotonicity and cleanup. Extend existing old-schema and governor tests.
5. Run focused regressions, full tests on a clean staged release, targeted race,
   build, diff/secret scan and fresh-context review. The opt-in live probe confirms
   a real turn while unrelated background IO remains blocked, Telegram recovery,
   and authenticated readback, cleaning only its own artifacts.
6. Commit only this change. Before installation inspect accepted starting work
   and let its sending boundary settle. Back up the consistent DB/binary, install
   the checked binary, restart, then verify Sync, existing execution identities,
   priority classification, command scope, history and no historical notice replay.

The spec check is recorded in manual-action-priority-spec-review.md. The user
explicitly authorized implementation, tests, commit and installation together.
