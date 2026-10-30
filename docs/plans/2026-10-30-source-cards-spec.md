# Source When/Then cards

Status: implemented and validated. The operator selected the Linear-card
prototype and requested separate PR source and event blocks.

## Goal and behavior

Replace the numbered Workflow steps form with compact source cards, using the
same palette and controls as task polling. The ordinary view shows the rule;
detailed settings open from their corresponding block.

- When has a clickable Arcanum PR or Cron source. PR identity scope and check
  frequency belong to source settings. Authored PRs map to the existing feedback
  source; assigned reviews map to the existing review source.
- PR feedback shows Request changes and/or Merge checks failed explicitly.
  AND within groups and OR between groups use the task-polling editor. Legacy
  check_merge_failures settings produce the equivalent summary without silently
  enabling checks. Assigned reviews show Assigned as reviewer. Cron shows its
  scheduled event. These retain the existing server contracts.
- Then contains Create task (title/description template), destination status,
  and launch mode in that order. Prompt, variables, trusted authors and shared
  execution fields belong to launch settings. Permission controls stay omitted.
- Prompt modes require TODO; Inbox/Done means no launch. Switching these modes
  retains prompt, template and execution drafts. Trusted-author launch remains
  available for PRs; switching to Cron uses manual approval instead.
- Copied child waiting rules remain separate under In new tasks, with their
  existing source placeholders, independent events and manual activation.
- Source enable/pause, explicit atomic save, Cmd/Ctrl+Enter, error recovery,
  stale-version guards and typing during saves retain their behavior. Escape
  closes a popup first, then its settings panel, without dismissing the source.

## Scope and acceptance

UI and documentation only. No discovery, request, deduplication, database or
Telegram execution changes; no combination of authored and assigned-review
candidate sets. Existing tasks and frozen requests are unaffected by source
template edits.

Acceptance: load legacy PR review/feedback and Cron sources; inspect independent
source/events/template/launch panels; save and read back exact source and child
definitions; switch modes without losing drafts; handle failed/stale saves; create
a source with valid title/prompt; preserve source selection on nested Escape.
Verify desktop and 320/375/736/1024 px layouts without horizontal overflow. Run
UI integration tests, production build and repository Go checks. Install checked
static assets and verify live byte readback; no daemon restart is required.
