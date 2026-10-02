# Sync Status stability validation

Date: 2026-10-02. Branch: `main`, base commit `8972c51`.
At the implementation-validation checkpoint, changes were local and uncommitted;
no commit, push, or daemon restart had been performed.

## Implemented behavior

Following the Astra [plan](../plans/2026-10-02-sync-status-stability-fix.md):

- Preserve all commentary/plan blocks independently of the newest 64 other details.
- Merge in authoritative current order and retain omitted runs near shared IDs.
- Preserve healthy inferred starts, restore legacy corrupted order/timing once,
  and defer timing recovery if an incomplete read still leaves scrambled order.
- Use turn-global block numbers and preserve existing tool-count association.
- Clone mutable slices before compaction; keep tool output excluded.

ADR-034 and the regression map describe the resulting contract. The egress
governor, polling frequency, routing, and database schema did not change.

## Automated evidence

New regression tests failed on the old implementation before the corresponding
fixes. They cover tool-heavy turns, more than 64 commentary/plan blocks, restored
prefixes, omitted interior/suffix items, repeated merges, text updates, ID-less
blocks, shared input isolation, terminal and new-turn transitions, legacy
corruption recovery, repeated incomplete reads, stable Status anchors, timer
buckets, UTF-8 field truncation, and UTF-16 overflow/entity boundaries.

Final checks passed:

```sh
go test ./...
go test -race ./internal/appserver ./internal/daemon ./internal/storage ./internal/telegram
go build -buildvcs=false ./...
git diff --check
```

Focused normalization and status tests also passed. The Go cache was redirected
to a temporary writable directory. Full transport suites ran outside the
sandbox because their temporary Unix/TCP listeners are otherwise blocked.
Formatting and secret/local-data scans were reviewed; no live identifiers,
credentials, or private payloads were added to the changes.

Fresh-context review found a repeated-recovery defect for damaged incomplete
reads. A failing regression reproduced it; the guard was corrected and the
reviewer rechecked the final change with no material findings. The terminal
recovery fixture also verifies a frozen end time.

## Real-data candidate QA and deployment boundary

A temporary overlay harness used the actual candidate normalization, merge,
compaction, JSON reload, and Status renderer. It read the existing shared App
Server and production SQLite in read-only mode. It made no Telegram mutations
and did not write the production database.

For a tool-heavy latest turn, three reads contained 210 authoritative details
and 16 Status blocks. The candidate retained 80 details (16 blocks plus 64
others). All three passes matched authoritative block identity/order/number,
preserved inferred starts and tool counts, and rendered 4060 UTF-16 units within
the limit. After the final review correction, another three reads verified the
task's newer turn: 10 source details, one Status block, 7 retained details, and
173 rendered UTF-16 units, with the same stability checks passing.

Read-only Telegram user-session readback confirmed the existing production
Status was present; at that observation it contained six displayed blocks and
2403 UTF-16 units. This is a production baseline, not candidate delivery proof.
No private snapshot, user-session material, or live identifiers are stored here.

At that checkpoint, the main daemon still ran its previously installed binary.
Deployment had not yet been requested; restart would reset Sync to off. Deployed
Telegram E2E had not been performed. After authorized deployment, re-enable Sync and verify the
same Status anchor across polls, block growth, terminal collapse/Final, and the
standard command/topic/callback readback checklist from the regression map.
