# Sync Status stability fix

Date: 2026-10-02. Status: implemented and reviewed; deployment not performed.
Execution evidence: [validation report](../testing/2026-10-02-sync-status-stability-validation.md).

## Goal and evidence

Keep a turn's Status commentary and plan blocks in their authoritative order across repeated reads, tool-heavy histories, text updates, incomplete observations, and process restarts. Preserve stable block identities, numbers, inferred start times, tool counts, and the existing Status message anchor. A fresh complete read must recover a projection already damaged by the old compaction/merge interaction.

The inspected code establishes the failure mechanism:

1. `boundCompactSnapshot` keeps the last 64 non-output details, including tools and commentary in the same budget. Enough tools remove earlier commentary and its inferred timing.
2. `mergeSyncDetailItems` starts with that saved tail and appends current-only details. A subsequent full read restores an omitted prefix at the end, rather than in the source order.
3. `applyStatusBlockTiming` sees restored blocks without durable start times and estimates them again. The compact tail changes again on the next poll.
4. `renderSyncStatusBlocks` numbers the surviving slice using `index+1`, making history loss visible as changing block numbers as well as order and duration.

The supplied live observation is consistent with this mechanism: non-monotonic commentary indices within the same turn and changing starts for the same block. The observed message was below Telegram's 4096 UTF-16-unit limit. Egress queue waits are a separate transport observation, not evidence that the governor caused this corruption.

Relevant contracts: `AGENTS.md`, `docs/testing/regression-map.md`, ADR-032, ADR-034, ADR-033, and the still-applicable timing contract in ADR-022. ADR-034 supersedes historical live overlays: streaming events update ephemeral activity, while authoritative latest-turn reads produce persisted projections.

## Scope and non-goals

Expected implementation files: `internal/appserver/normalize.go`, its tests, `internal/daemon/sync.go`, its tests, and focused updates to ADR-034 and the regression map. Keep existing snapshot JSON and database schema unless a demonstrated blocker requires a separate decision.

No governor or polling-interval changes, new dependencies, generic event framework, unbounded full-thread history reads, new Telegram messages, output/command rendering, routing changes, or broad cleanup. Do not restart the main daemon or change Sync activation as part of this fix without deployment authorization. No commit or push is part of this plan.

## Chosen design

### 1. Retain status blocks independently of the detail tail

Keep all commentary/plan details in the existing `DetailItems` field. Retain at most 64 other non-output details, selecting their newest tail while preserving the full sequence's relative order. Continue removing output details and output payloads and applying the existing per-field text bound.

This is a separate retention budget, not a second persisted copy of status blocks. It avoids a schema/format migration and prevents tools from evicting status identity and timing. Total `DetailItems` may exceed 64; the old test's total-length assertion must change to assert the non-status cap and retained commentary explicitly. Include a case with more than 64 status blocks, not only more than 64 tools.

The compact projection grows with the number of commentary/plan blocks in the latest turn, while tool metadata remains bounded. That is an intentional tradeoff needed to retain block history. Telegram's independent rendering bound remains unchanged. Avoid claiming total compact JSON size is fixed.

### 2. Merge using current source order and durable identity

For the same turn, use current details as the ordered base; upsert current values by identity and insert previous-only details relative to the nearest shared anchors. Do not start with the old compact slice and append a restored prefix.

Concrete required examples, with letters denoting stable item identities:

- Previous `C,D,E`, current `A,B,C,D,E` -> `A,B,C,D,E`.
- Previous `C,A,B`, current `A,B,C` -> `A,B,C` (repair existing corruption).
- Previous `A,B,C,D,E`, current `A,C,E,F` -> `A,B,C,D,E,F`.
- Previous `A,B,C`, current `B` -> `A,B,C`.
- Empty current retains previous; empty previous accepts current.
- No shared identity: preserve both runs deterministically, previous before current. Do not invent chronology from fingerprints or timestamps when no common anchor exists.

Preserve the previous-only run's internal order. Insert a missing run before its nearest following shared anchor; if none exists, after its nearest preceding shared anchor, or before current when there is no shared anchor. Current relative order wins when legacy previous anchors conflict. Tests must freeze the chosen tie behavior rather than depending on map iteration.

Use `Kind + ID` where ID exists. For ID-less commentary/plan details with a positive `CommentaryIndex`, use kind plus that ordinal, without the mixed detail slice offset or text fingerprint. The existing positional fallback may remain only for otherwise unidentifiable non-status details. Do not collapse multiple index-zero blocks under one key; keep a deterministic fallback for legacy/test data without reliable identity.

Current text, fingerprint, and status replace matched values; missing `StartedAt` can reuse the prior start unless the recovery rule below invalidates it. Do not deduplicate blocks merely because their text matches. Avoid mutating inputs through reused slice backing arrays.

The merge must be idempotent. The existing process path merges once before persistence and again inside persistence; repeated application must not reorder, duplicate, or change values. Removing the duplicate call is optional only if all direct persistence callers retain the same protections; it is not necessary for this fix.

### 3. Preserve healthy timing and recover damaged timing once

For healthy same-turn data, preserve a block's known start across full polls, text/fingerprint updates, missing details, tools, and a JSON reload. Estimate times only for genuinely new or previously lost blocks, using the existing interpolation between known neighboring starts. A restored missing prefix belongs before the first retained anchor, not after the last poll.

Inspect the stored previous projection before canonicalization. Detect the legacy corruption signal of decreasing positive commentary indices in stored status order. Also check reused starts in the repaired canonical order for decreases or times outside the turn interval. A source-order correction alone must not leave an impossible time sequence.

When these checks show corruption, rebuild the affected inferred status timeline deterministically from the repaired block order and the existing turn start/end, then persist it. For this recovery pass, disregard both the old timing lookup and copied `StartedAt` values inherited by the merge; clearing only the lookup is insufficient because merge already carries starts into current items. Prefer a small explicit helper/condition over a new persistent version or state machine.

Use the existing initial-distribution behavior for a fully untrusted timeline. It is acceptable for recovery to correct displayed historical durations once. Exact lost historical start times cannot be reconstructed; describe recovered values as estimates. The next identical read must preserve the newly saved starts byte-for-byte. Preserve healthy known starts wherever no corruption is established.

Terminal recovery uses the frozen terminal endpoint, so it cannot incorporate later wall-clock time. The existing terminal fence and turn-time rules remain intact. New turns do not inherit blocks, counts, or starts. Legacy stable terminal projections may be skipped by reconciliation; recovery is guaranteed on the next successful full read, not by a new global migration sweep.

### 4. Render stable authoritative block numbers

Use a positive `CommentaryIndex` as the displayed block number; retain `index+1` only for legacy details without an ordinal. Tool counts already use the same ordinal and must remain associated with that block. Do not derive a number from the subset that survives Telegram text trimming.

Source ordering is determined by the ordered merge, not by sorting IDs, text, or inferred timestamps. Ordinals are presentation/association metadata, not a substitute for authoritative current item order.

### 5. Keep the existing incomplete-read and streaming boundary explicit

Empty/incomplete observations cannot erase already observed details, User/Final content, counts, or terminal state. Partial snapshot tests should omit details while retaining known identities and turn-global commentary indices. Per-block tool counts remain monotonic maxima computed before tool-tail truncation; do not recount only retained tools.

Production Sync requests the latest turn with full items. Streaming events remain read-free and do not merge partial tool overlays into durable state. Preserve those tests and do not change normalization of streaming wire events.

Limitation: a hypothetical partial wire payload that removes the leading commentary and then renumbers the remaining blocks from one does not carry the same global-ordinal contract. Supporting that protocol would require explicit provenance or identity-based count remapping and is outside this patch. Do not claim arbitrary locally renumbered partial histories are supported, or add a broad heuristic to infer completeness from item count. If live evidence establishes this different failure, stop and extend the design before implementing that support.

## Implementation and test sequence

1. Add a failing cross-layer regression before changing code. Build a synthetic authoritative turn containing several commentary/plan blocks and more than 64 tools; normalize, monotonic-merge, compact, JSON-reload, and render it over multiple fixed-time polls. Assert exact block identity/order, numbers, unchanged starts, all pre-truncation tool counts, and the same Status anchor. Compare stable body components separately from intentional elapsed/freshness changes.
2. Add compaction tests proving independent retention budgets, output removal, UTF-8-safe field truncation, preservation of more than 64 status blocks, and isolation from caller slice mutation. Update `TestCompactSnapshotKeepsToolCountBeyondDetailLimit` to assert the new cap contract.
3. Add table-driven merge tests for the examples above, changed text, tools between comments, ID-less positive-ordinal blocks, duplicate text with different identities, zero-ordinal fallback, and double-merge idempotency.
4. Add a legacy recovery fixture constructed directly as old compact JSON: scrambled commentary order, truncated prefix, and stale/reversed inferred starts. A fresh full read repairs it; a second poll and a JSON reload keep the repaired order and starts. Include a healthy sparse previous snapshot that only needs its prefix restored and must preserve valid anchors.
5. Implement the retention, ordered merge, recovery, and numbering changes. Prefer the existing normalization/rendering boundaries and small helpers.
6. Exercise lifecycle transitions: append a block, update its text, omit then restore an interior block, append tools, freeze terminal time, observe a late active snapshot, and start a different turn. Assert that only the open block grows with the existing ten-second timer bucket and that closed durations remain stable after the next block is known. Confirm restart from persisted JSON needs no in-memory history.
7. Update ADR-034 and the regression map to state independent status retention, ordered recovery, stable numbering, and the precise incomplete-read contract. Keep this plan as a dated execution artifact, not the permanent specification.

## Nearby regression checks

- Status: active and terminal renderings below and above 4096 UTF-16 units, including astral Unicode characters; exact terminal expandable-blockquote entity offsets/lengths; header and latest tail survive trimming; retained block headers keep original numbers. Include a body below the limit with many tools so the original bug cannot hide behind legitimate text trimming.
- Counts: more than 64 tools under one commentary, tools spread over several blocks, orphan tools before commentary, plan blocks, repeated polls without double counting, and a lagging snapshot with fewer tools. Labels/output never appear in Status.
- Delivery: late-bound persisted projection, one Status message per turn, title change and direct delivery preserving the anchor, and no extra edit for unchanged content inside the same timer/freshness bucket.
- Finals: sticky final/user content, terminal-to-active rejection, Final delivery proceeding when terminal Status editing fails, retry idempotency, long numbered Final chunks, and new-turn User-before-Status/Final presentation.
- Streaming: important events still invalidate; streaming activity still changes only ephemeral event freshness without reads or SQLite writes. No new event handler producer is introduced.
- Governor: run the existing suite as part of normal validation. Do not modify limits in response to expected background queue waits.

Useful existing tests include `TestSyncStatusAggregatesCommentaryBlocksInOneMessage`, `TestSyncStatusUpdatesSameBlockAndCountsTools`, `TestSyncEventReadCountsToolAndMonotonicProjectionPreservesIt`, `TestSyncStatusBlockDurationsPartitionOverallDuration`, `TestSyncStatusOpenBlockTimerUsesSameTenSecondBucketAsHeader`, `TestSyncStatusTrimsOldLinesAndPreservesLatestTail`, `TestSyncDeliveryLoadsLatestPersistedProjectionAtExecution`, `TestSyncTerminalStatusFailureDoesNotDeferFinal`, and the compact timing tests.

## Validation and live QA

Run focused tests first, then the repository's required checks:

```sh
go test ./internal/appserver ./internal/daemon
go test ./...
go test -race ./internal/daemon ./internal/storage ./internal/telegram
go build -buildvcs=false ./...
git diff --check
```

Run the secret/local-data scan specified in `AGENTS.md`, inspect the actual diff, and perform a fresh-context review with this brief, tests, relevant ADRs, and validation output. No private identifiers, payload text, local credentials, or live snapshots belong in committed fixtures or documentation.

Before any deployment, live evidence can be gathered without touching the main daemon: use an isolated candidate harness with the real normalization/merge/compaction/render path and a temporary store; consume a read-only authoritative sample from an available active tool-heavy turn. Repeat polls and restart the harness from its compact JSON. Compare block identity/order, stable starts/numbers, counts, and formatted body to authoritative source order. Read back the existing Telegram message to distinguish current production behavior from candidate rendering. Do not publish candidate text into user topics merely to obtain a Bot API success.

This isolated check validates candidate projection against real data; it is not deployed Telegram E2E proof. Record that distinction explicitly. If deployment is later authorized, rebuild/restart according to project instructions, account for restart resetting Sync off, then verify through actual Sync-topic readback: the same Status anchor across polls, correct blocks through tool growth, legitimate elapsed updates only, terminal collapse and one Final, and a clean new turn. Exercise the standard command-scope, Control, topic, callback, and log/readback checklist where the live contour permits. Log queue waits separately from rendering correctness.

Acceptance requires deterministic automated recovery and stability tests, successful mandatory checks, fresh-context review, and a precise statement of candidate/live/deployed validation actually performed. A missing live contour or missing deployment authorization must be reported as the exact remaining validation boundary, not as successful E2E.
