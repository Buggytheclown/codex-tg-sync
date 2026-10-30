# Polling rules: specification review and implementation plan

## Reviewed contract

A task has independent rules. Each rule selects one source (Arcanum PR or a
schedule), an OR of AND event groups, and one action. AND uses facts from the
same check, never accumulated observations. Actions always leave Polling:
Inbox/Done without a model, or TODO plus a continuation request with Telegram
Start or automatic start. Leaving Polling stops every rule. Explicit re-entry
rearms them. PR merged → Done is an ordinary rule.

Sources keep discovering new tasks. Their Wait and After event use the same
fields, with actions applied to the new child; copied child rules remain dormant
until that child enters Polling. Existing trusted-author policies remain valid.

## Review findings resolved

- Simultaneous matches need arbitration: Done first, then configured order;
  accept one action. A Done result never interrupts an already started turn.
- Moving to TODO must not cancel the request it just created: cancel old,
  definitely unstarted requests before inserting the new request, in one transaction.
- A condition observed on separate checks must not satisfy AND. Repeated unchanged
  facts must not launch repeatedly after rearming; retain accepted fingerprints.
- Migration must preserve prompts, cursor baselines, merge defaults, inactive
  settings and frozen requests. Legacy edge waits retain their event semantics
  until the user edits their condition expression.
- Required merge failures exclude Approved and Closed issues; pending/unknown
  are not failures. Existing published-diff API is sufficient; no new API needed.

Specification is ready after these resolutions. The design intentionally does not
keep a merge watcher alive after another rule moves a task to TODO.

## Design and alternatives

Keep the existing task_polling owner, generation, runner and outcome transaction.
Store rule settings in its JSON config and per-rule cursors in its JSON cursor.
This is smaller than a new per-rule SQL table and avoids coordinating multiple
owners that must stop together. Leaf PR and cron adapters stay in use. Cache PR
reads for one owner check so same-PR rules see the same response. Resolve the
accepted rule from stored configuration under the generation guard.

Reuse the existing launch request and dispatcher for automatic continuations;
keep original-thread authority checks, explicit permissions, busy handling and
no replay guarantees. No new scheduler, launch queue or generic workflow engine.

## Delivery and validation

1. Add rule model, strict validation, same-check boolean groups and deterministic
   selection. Test AND across different checks, Done priority, errors and cron.
2. Accept one selected action transactionally, with TODO request survival,
   automatic continuation and all-rule generation fencing. Test stale outcomes,
   deduplication, persistence and rollback.
3. Preserve old settings/cursors through upgrade and future source-child copying.
4. Replace single-wait UI with reusable rule cards and the three After actions.
   Remove Approval and special merged checkbox. Preserve drafts/version guards,
   source policies, keyboard access and current palette.
5. Run targeted and full Go/UI suites, lifecycle race tests, builds and diff/local
   data checks. Review final diff against this contract in a separate pass.
   Browser QA uses isolated fixtures; model/Telegram launches require a safe live
   fixture and must not dispatch real work merely for testing.

## Implemented compatibility choice

Use lazy conversion on editor save instead of a startup rewrite. Old active rows
continue through the tested legacy adapter. The editor projects effective merge
completion as a separate rule; storage lifts its accepted cursor to `legacy-wait`.
This avoids a migration that could rearm every active wait on daemon restart.
