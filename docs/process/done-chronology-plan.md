# Done chronology: implementation plan

Status: implemented and locally validated under the requested make-plan → make-diff workflow.
Scope: [brief](done-chronology-brief.md). The proposed browser timezone, English day labels,
chronological placement and conservative historical backfill are the implementation defaults.
Commit and local deployment were authorized by a subsequent delivery request.

## Design

Store `tasks.done_at` as an optional UTC RFC3339 instant, separate from `updated_at`.
Existing storage transactions own it: insert into Done uses creation time; a successful
manual or accepted polling transition uses the transition's current time; leaving Done
clears it. Same-column operations and metadata writes preserve it. Poller observation
`CheckedAt` remains diagnostics and cannot backdate the accepted transition.
Expose the field in full tasks and compact board cards; no history query on refresh.

A persisted field is preferable to deriving every refresh from events: old polling
history lacks destinations, and edits must never change chronology. Explicit writes
in the three existing transition sites are simpler to audit than SQLite triggers.
An additive transactional migration adds the column and backfills current Done tasks
once. Inspect the latest history event capable of changing column: `moved`, `created`,
`pr_merged`, or a non-child `polling_event`. Only an explicit last entry to Done supplies
a date. Ambiguous polling entries, missing history or invalid dates remain unknown;
never fall back to an earlier Done event or `updated_at`. Migration preserves history,
versions and all other state; reopening cannot backfill again.

UI sorts Done by parsed instant descending, then ID for stable ties; unknowns last.
Other columns retain manual ordering. Add a small semantic date heading before each
local calendar day: Today, Yesterday, a localized short date (year when different),
Unknown date. Use calendar arithmetic rather than 24-hour subtraction for yesterday.
The existing 30-second reconcile and focus refresh update the local day without reload.
Filter first, then render headings, omitting empty groups.

Expanded Done is a whole-column target, with a subtle outline and no insertion gap.
Cross-column optimistic placement sets a provisional entry instant, replaced by the
server response; rollback restores the prior instant/group. Same-Done drag is a no-op;
leaving Done retains existing insertion and motion behavior. Collapsed Done stays the
same width and does not reveal the column during drag.

## Delivery and evidence

1. Storage/model: first add tests for entry, edits/reorder, re-entry, stale/conflicting
   commands, direct/source creation, both polling paths and old-database migration.
   Implement atomic writes and compact projection. Confirm reopen persistence and
   unknown/ambiguous backfill; use old CheckedAt in polling tests to detect backdating.
2. UI: first add pure ordering/day tests and board integration/drag tests for dividers,
   search, day rollover, whole-column Done, no intra-Done reorder, and failed-drop
   rollback. Implement minimal helper/rendering/styles using the current palette.
3. Review tests and combined diff against the brief, update ADR-035/regression map,
   run targeted tests, all Go/UI tests, UI build, Go build, diff/changed-line private-data
   scan. No new dependencies or unrelated dirty-file cleanup.
4. If browser QA is available, inspect populated/empty/narrow Done, keyboard labels,
   pointer drag in/out and rollback. State any unavailable visual validation explicitly.

No changes to polling conditions, launch/Telegram behavior, other-column ordering,
card contents, inspector controls or search semantics.

## Validation result

- Full Go suite passed; the storage suite was rerun after final migration refinements.
- 204 UI tests passed, including all existing drag tests; UI and Go builds passed.
- Calendar/drag tests also passed under Europe/Berlin and America/New_York.
- Backfill failure rolls back the schema change; retry preserves existing task state.
- Creation history crossing midnight uses the stored creation instant.
- Self-review checked the brief, tests, combined dirty diff and ADR. Same-Done no-op
  release resumes queued refresh; no polling/launch ownership semantics changed.
- Diff whitespace and changed/new-line private-data scans passed.
- Browser visual/pointer QA remains unverified: the browser access host was unavailable.
  The implementation phase did not mutate the live database or restart the daemon.
  Delivery follows the separate commit/update request; pushing is outside that request.
