# Regression Map

Use this map for changes to the active Sync Telegram surface. ADR-032 supersedes legacy direct-message observer contracts in older ADRs.

## Admission, setup, and commands

Primary tests:

- `internal/daemon/retirement_test.go`: out-of-group messages and callbacks are rejected before storage; retired settings are ignored.
- `internal/config/config_test.go`: Sync group and exactly one allowed user, transport modes, paths, and external adapters.
- `cmd/ctr-go/main_test.go`, `cmd/ctr-go/service_test.go`: init/service setup writes the Sync-only configuration.
- `internal/telegram/api_test.go::TestClientScopesCommandsToExactSyncChat`: exact Bot API scope payload.
- `internal/telegram/bot_test.go::TestBotStartScopesCommandsToConfiguredSyncGroup`: startup clears default commands before setting the Sync chat menu.
- `internal/telegram/bot_test.go::TestDefaultCommandsExposeSyncAndOperatorStatusCommands`: exact eight-command menu.

Required invariants:

- Reject before SQLite or App Server.
- No direct-message compatibility router.
- Startup fails closed if command scoping fails.
- `/repair` remains hidden but accepted in Control.

## Sync activation, discovery, and transport

ADRs: ADR-020 through ADR-027, ADR-029, and ADR-032 through ADR-034.

Primary suites:

- `internal/daemon/sync_test.go`
- `internal/storage/store_sync_test.go`
- `internal/appserver/client_test.go`
- `internal/appserver/writer_manager_test.go`
- `internal/telegram/api_test.go`

Coverage includes:

- forum validation and Control preparation;
- paginated, importance-first `/sync on` topic selection (including `notLoaded` threads with running or terminal last turns), initial topic limit, empty active Sync, and continuous discovery of chats created or updated in the last 24 hours;
- bounded discovery batches over every page, with failed-candidate progress;
- `sync_fresh_window_test.go`: 100 chats, expiration/recreation, durable unknown
  create pause, delivery of pre-activation Finals with normal later delivery,
  catch-up Status retry without repeating Final, restart-safe multipart retry,
  and rejection of queued previous-session snapshots;
- one topic per thread and receipt idempotency;
- shared daemon/WebSocket reconnection without prompt replay;
- startup/reconnect preserve session, topics, ready drafts, anchors, activation boundary, and delivery fingerprints; unknown operations are never replayed;
- periodic 24-hour inactivity cleanup by Codex thread activity, including ready drafts,
  existing local writer-state protection, passive-topic expiration and recreation
  on new activity, asynchronous deletion outside `/newchat`, and
  cleanup retry across historical sessions;
- generation-aware writer claims and fail-closed unknown dispatch;
- poll/list/read behavior and non-blocking startup;
- important App Server events coalesce into bounded latest-turn reads while
  streaming updates stay read-free;
- start/status events for unbound threads wake authoritative list discovery,
  while periodic discovery catches missed events and short completed turns;
- periodic summary reconciliation skips only fully delivered stable terminal
  turns and fully reads every other tracked topic;
- typed Telegram topic/retry failures;
- one-second group-write pacing with no more than 20 raw attempts per rolling 60.25 seconds, bounded foreground preference, and shared `retry_after` cooldown.
- a separate 12-attempt background cap that never prevents an eligible foreground write, plus per-operation queue-wait diagnostics.

## Prompt, steer, stop, and lifecycle

Primary suite: `internal/daemon/sync_test.go`.

Required scenarios:

- first topic prompt resumes/starts exactly one turn;
- first draft prompt starts its turn before Telegram topic rename; reconciliation performs the rename asynchronously;
- duplicate message ids do not replay;
- active topic text steers the current turn;
- stale-active rejection re-reads authority before replacement start;
- authoritative newer turns release stale local ownership;
- Stop checks current topic/thread/turn/generation;
- terminal events and terminal poll evidence close writer leases;
- active Status edits only on turn/state/commentary/plan changes; clocks,
  freshness and tool-counter changes alone produce no edits; start/observation
  times are fixed in the displayed card and terminal duration stays exact;
- one status message is created per turn and then edited in place across title
  changes, mirrored user messages, and direct-delivery refreshes;
- Telegram delivery is late-bound to the newest persisted projection, terminal
  state cannot regress to active, and Final does not wait for a Status edit;
- a new Codex-origin turn waits for its authoritative User item before creating
  Status or Final, while Telegram-origin input uses its already-visible original;
- commentary blocks retain their duration and show the complete monotonic count
  of associated tool calls without labels or output;
- tool-heavy turns retain every commentary/plan block independently of the
  64-detail non-status tail; repeated full reads preserve source order, global
  block numbers, and timing, while incomplete reads retain missing items near
  shared identities without locally renumbering their commentary indices;
- legacy scrambled projections recover their order and inferred timing once
  on the next full read, while healthy retained timing anchors survive prefix
  restoration; regression fixtures live in `sync_status_stability_test.go`;
- `sync_status_delivery_test.go`: accumulated tool counts accompany the next
  content update, failed edits retry after reopening the store without new events,
  legacy active cards refresh once and completed cards do not mass-refresh;
- streaming events remain read-free without per-thread heartbeat-clock bookkeeping;
- restart/reconnect does not duplicate user receipts or finals;
- long finals preserve UTF-16 limits and number every Final chunk (`1/N` through `N/N`).

Terminal ambiguity logic lives in `internal/daemon/terminal_gate_test.go`. Preserve defer windows, explicit interrupts, and authoritative terminal evidence.

## Approvals and structured input

Primary tests:

- `internal/daemon/sync_test.go::TestSyncApprovalCallbackIsGuardedByTopicTurnAndGeneration`
- `internal/daemon/sync_test.go::TestSyncApprovalDecisionsResolveSameCard`
- structured input callback tests in `internal/daemon/sync_test.go`

Required invariants:

- only daemon-owned requests are actionable;
- callback coordinates and writer generation are checked before App Server response;
- `acceptForSession` is labeled `Allow command prefix`;
- accept, acceptForSession, decline, and cancel edit the original card to a terminal status;
- the edit path has no inline keyboard;
- duplicate/stale callbacks fail closed.

App Server request normalization remains covered in `internal/appserver/*_test.go`; removing a Telegram presentation must not remove wire compatibility.

## Storage and upgrade safety

Primary tests:

- `internal/storage/store_test.go::TestLegacyTelegramTablesAreNotCreatedOrDropped`
- delivery retirement matrix in `internal/storage/store_test.go`
- Sync state tests in `internal/storage/store_sync_test.go`
- callback, receipt, request, delivery, and snapshot repository tests across `internal/storage/*_test.go`

Required invariants:

- fresh DB has no retired Telegram tables;
- old DB tables are left untouched;
- startup supersedes pending/retry/processing deliveries outside the Sync group and retired observer delivery kinds;
- historical delivered/dead rows remain historical;
- current health, external terminal, and Sync activation deliveries to the Sync group remain eligible;
- startup returns supported in-group deliveries interrupted in `processing` to `retry`;
- `/sync on` stops after the first ambiguous topic-creation outcome, marks later candidates skipped, and queues its Control summary durably with the activation state.

## Launch requests

ADRs: ADR-028 through ADR-031.

Primary suites:

- `internal/daemon/external_requests_test.go`
- `internal/storage/store_external_requests_test.go`
- `internal/cronpoller/*_test.go`
- `internal/ymessenger/*_test.go`
- `internal/arcanumreview/*_test.go`

Required scenarios:

- ingestion and cursor advancement are atomic/idempotent;
- a `[Launch request]` card exists before manual or automatic start;
- manual Start/Dismiss and Retry/Close edit the same durable card;
- callback acknowledgement does not wait for launch-card Telegram I/O;
- only admission-valid callbacks are pre-acknowledged, before business handling and regardless of group cooldown;
- `createForumTopic` `429` performs one launch-owned attempt and leaves a failed card with Retry/Close;
- auto-start author policy applies only to new Arcanum requests;
- Arcanum assigned-review polling skips PRs authored by the configured login;
- own PRs in `Waiting for changes` create one automatic feedback summary per PR,
  including after a daemon restart;
- start claims once and ambiguous thread creation is not replayed;
- terminal state closes the request and external reply delivery is retryable/idempotent;
- `/requests` enters through public Sync Control routing and lists active requests;
- unauthorized YMessenger messages cannot create or start Codex work.

## Poller and health status

Primary tests:

- poller status tests in `internal/daemon/external_requests_test.go`
- health tests in `internal/daemon/health_test.go`
- aggregate incident tests in `internal/daemon/external_poll_health_test.go`
- source-specific poller tests under `internal/cronpoller`, `internal/ymessenger`, and `internal/arcanumreview`

Required invariants:

- `/pollers` enters through public Sync Control routing;
- output distinguishes disabled, polling, healthy, degraded, and failed sources;
- last attempt/success and consecutive failures are truthful;
- sleep/resume gaps do not cause false flapping;
- external pollers share one warning/recovery episode; additional failures and
  partial recoveries stay silent, and unconfirmed errors or missing/stale
  observations block recovery;
- restart and legacy upgrade retain incidents and suppress per-source queued
  notices without repeating already delivered warnings;
- queued aggregate recovery waits for current health without exhausting send
  attempts when a new error or unknown/stale source temporarily blocks it;
- warning/recovery order is preserved;
- health deliveries fall back to General only inside the configured Sync group.

## Configuration and secrets

Primary tests:

- `internal/config/config_test.go`
- `cmd/ctr-go/main_test.go`
- `cmd/ctr-go/service_test.go`
- secret-redaction tests in daemon, config, storage, and Telegram packages

Configuration must not reintroduce retired chat allowlists, observer intervals, notification switches, panel modes, or Telegram model/settings state. External tokens and Telegram Bot API URLs must remain redacted in config JSON, logs, SQLite errors, and snapshots.

## Local task board

ADR-035 adds database-owned PR/cron sources, owned child definitions and the local browser UI.

- `internal/storage/store_task_source_migration_test.go` covers atomic/idempotent legacy template-reference upgrade, preservation of four definitions, 82 historical configs, frozen requests and provenance.
- `internal/storage/store_task_launch_policy_test.go` covers exact configured prompts and event facts, durable author independent of waiting, human source labels, manual/trusted/automatic initial policies, normalized author matching, visible-before-auto claim, manual continuations, central source/cron title composition, and one-time upgrades preserving custom source names/policies/frozen requests.
- `internal/polling/checker_test.go` additionally checks initial/current continuation snapshot authors. `cmd/ctr-go/board_test.go` checks configured permissions and fresh cron/review policy preservation; UI policy editing is covered in `ui/src/App.test.tsx`.
- `internal/storage/store_tasks_test.go` and task import tests cover fresh/old databases, source definition snapshots, optimistic versions, generation fencing, atomic events/requests, legacy barriers, cancellation and chat binding.
- `internal/polling/*_test.go` covers ANY conditions, empty/draft/self/nested comments, all-resolved and status edges, published heads, cron occurrence identity, lateness, resume stabilization and partial read failures.
- `internal/daemon/board_launch_test.go` covers same-thread continuation, busy/missing chats, move versus launch, and projection without Telegram topics or enabled Sync.
- `internal/controlapi/board_test.go` covers exact Host/Origin and JSON boundaries, conflict responses, static/API separation and SSE invalidation.
- `cmd/ctr-go/board_test.go` covers one-time legacy import and sole runner ownership; UI tests/build live under `ui/`.
- `internal/storage/store_task_order_test.go`, `internal/controlapi/board_order_test.go` and `ui/src/drag.test.tsx` cover equal-position neighbor insertion, persistent ordering, API conflicts/backward compatibility, same-column polling/request preservation, midpoint/height placeholders, grabbed/cancel states, optimistic saves, old snapshots, rollback and queued refresh.
- `internal/daemon/board_lock_test.go` and `internal/storage/store_task_reorder_test.go` cover independent board progress under Sync/dispatch barriers, reorder-only column/version fences, and preserved launch/cancel ownership. Compact API/storage tests prove summaries exclude large payloads while detail reads retain them; SSE uses compact status/version changes. UI tests cover lazy detail loading, failed retry, stale selection responses and source settings preservation.
- `internal/storage/store_task_priority_chat_test.go` and `internal/daemon/board_chat_link_test.go` cover priority default/preservation/upgrade/reopen, passive authority, chat uniqueness/idempotency, launch/history conflicts and generation fencing. UI acceptance includes Cmd+Enter validation, accessible card priority and non-nested Codex links, linking an existing chat, and preserving drafts on conflicting save responses.
- Task detail acceptance: editable title/multiline description in Overview, prompts/execution/source URL in Polling, History event/request readback, atomic workflow saving with draft/version preservation, and external edits retaining conflict protection.
- Browser acceptance: native drag ghost, grabbed indication, midpoint placeholder while moving upward, same/cross-column insertion, scroll during drag, Escape/cancel, delayed/failed saves; compact collapsible sources, four columns, right details, source versus existing child edits, invalid Polling move, add-chat idempotency, task deep link, execution badge and reconnect.

Run `npm ci`, `npm test` and `npm run build` in `ui/`. Include polling/controlapi in targeted race tests. Live QA uses dedicated personal test tasks/chats; it must not post or change user PRs.

## Required checks (all surfaces)

Run targeted tests first, then:

```powershell
go test ./...
go test -race ./internal/daemon ./internal/storage ./internal/telegram
go build -buildvcs=false ./...
git diff --check
```

For live Telegram QA, verify:

1. command menu exists only in the Sync group;
2. a direct message produces no response or state change;
3. `/status`, `/pollers`, and `/requests` work in Control;
4. `/sync on` creates/updates topics;
   running/waiting chats are created before unknown, failed/interrupted, and completed chats;
5. a topic prompt starts with explicit writable/on-request permissions;
6. an available approval edits the same card and removes buttons;
7. restart preserves Sync and the same topics/Status IDs, catches up a downtime Final once, and never interrupts/replays the authoritative Codex turn.
8. during a Telegram `429`, callbacks are acknowledged while noncritical group writes wait for `retry_after`.

## Restart recovery

- `internal/storage/store_sync_recovery_test.go`: active/off/draining and interrupted activation, receipt/draft uncertainty, preserved delivery/correlation, exact Sync callback expiry, missing database, repeated recovery.
- `internal/daemon/sync_recovery_test.go`: detached terminal/active/superseding turns, downtime Final fingerprint and Status anchor, failed read retention protection, Telegram/Control/group gate, resumed draining without repeated interrupt, old callback with repeated generation.
- `internal/daemon/health_test.go`: transport loss preserves enabled state and reports disconnection without topic cleanup.
- Startup and reconnect never call activation, replay prompts, or create/delete previously bound topics. Unknown creation pauses discovery only. A failed read is isolated to its topic.

## Board passive observation

- `internal/daemon/board_launch_test.go`: shared-transport board reads never resume chats with Sync off, no topic, expired topic, or dirty events; known old terminal observations block metadata-only discovery while genuine active and completed Desktop work remains discoverable.
- `internal/storage/store_external_requests_test.go`: returning a busy or Sync-off request to pending dirties its Telegram rendering marker so Start remains actionable.

## Inline board controls

- `internal/storage/store_task_priority_command_test.go`: priority-only versioned updates preserve order, configuration, polling runtime and requests; invalid/stale/missing commands do not mutate.
- `internal/controlapi/board_priority_chat_test.go` and `internal/daemon/board_lock_test.go`: compact priority responses, command guards and no Sync/launch-gate dependency.
- `ui/src/App.test.tsx` and `ui/src/drag.test.tsx`: direct card priority editing, safe full-detail version advancement, priority/draft conflicts, inline chat attachment/cancellation/errors and separate Codex controls without drag/selection regressions. Native visual/drag QA still requires an enabled browser surface.

## External ticket links

- `internal/storage/store_task_ticket_test.go`: fresh/old database upgrade and reopen, compact projection, legacy omitted-field preservation, URL validation and metadata-only updates preserving polling and requests.
- `internal/controlapi/board_priority_chat_test.go` and `internal/daemon/board_lock_test.go`: compact ticket commands, loopback command guards and gate independence.
- `ui/src/App.test.tsx`: card/Overview add, edit, clear and cancel; failed/conflicting saves retain ticket and unrelated drafts; compact replies cannot certify unseen settings; source URL, Codex and manual ordering remain independent.
- `ui/src/ticket.test.ts`: short-key normalization, invalid keys, decoded final-path labels and full-link preservation. Storage and control API tests cover canonical keys through create, PATCH and the compact ticket command; UI integration covers keyboard saving.

## Merged PR review completion

- `internal/arcanumreview/snapshot_test.go`: authoritative merged metadata with exact PR identity succeeds without comment or author reads.
- `internal/polling/checker_test.go` and `runner_test.go`: merged waits produce no continuation events; failed reads cannot publish a terminal status; merged source entries are remembered so bounded reads still discover later open PRs.
- `internal/storage/store_task_merged_test.go`: review/feedback defaults and explicit merge options complete atomically, record history and stop polling. Column/generation/error fences reject stale outcomes; disabled options, closed PRs, default manual/cron waits and source tasks remain unchanged. Only definitely unstarted launch requests are cancelled; started and uncertain requests remain intact.

## Configured PR identity and merge options

- Polling helper/checker tests cover configured account overriding saved identity, recursive source-child validation, missing required identity, malformed configs and unchanged PR conditions.
- Storage tests cover effective read projections without writes, explicit true/false precedence, durable legacy review/feedback provenance, frozen defaults for copied children and independence from subsequent source changes.
- `ui/src/App.test.tsx` and `model.test.ts` cover login-free source/task saves, merge toggles in task and source child waiting settings, PR versus cron defaults, keyboard saves and conflict draft retention.

## Workflow inspector

- `internal/storage/store_task_workflow_test.go` covers atomic resulting-state prompt/polling saves, stale/invalid/persistence-failure rollback, source ownership, accepted cursor identity, generation/runtime ownership, inactive draft settings and frozen request preservation.
- `internal/controlapi/board_workflow_test.go` covers the optional PATCH polling contract and omitted/null compatibility, version/error boundaries and atomic readback.
- `ui/src/Workflow.test.tsx` and `App.test.tsx` cover the numbered source/task workflow, one save payload, separate readiness-guarded activation, concrete PR links versus placeholders/provenance, no-chat attachment and action selection, ordinary prompt-only tasks, keyboard saves and conflicting drafts.
- Independent browser acceptance uses isolated tasks and the real loopback API/SQLite: source and child independence, frozen prompts, PR/cron configuration, no-model returns and merge completion, same-chat pending continuation, priority/ticket/Codex controls, ordering, Done toggle and responsive inspector. Do not use real PR tasks for mutation QA.

## Shared board visual design

- UI integration covers the task brief's editable title/description/priority and connections, omitted Overview summaries/dates/requests, and retained Polling/History readback. Native fields retain Cmd/Ctrl+Enter and conflict/typing protection.
- Card acceptance includes a single priority control, multiline two-line description, independent polling/execution indicators, visible empty connections, and controls outside the draggable selection button. Integrated footer height remains part of insertion/placeholder measurement.
- Icon-footer acceptance covers branded PR/ticket links without detail reads, exact Arcanum host/path parsing, safe external links, icon-only accessible Codex opening/attachment, running-only activity rings and unmarked terminal states. Browser QA checks hover/focus pencil replacement without geometry shifts, touch edit access, reduced-motion rings, status refresh, ticket/chat drafts and narrow wrapping. The footer remains inside the complete drag wrapper.
- Source identity acceptance covers eye/comment icons on sources and children despite custom titles, typed parent fallback and durable event namespaces. Connection acceptance covers PR in the Overview row, muted Add PR/add-ticket actions and linked-chat replacement. PR tests cover ID/URL add/edit/clear, invalid input, exact-version full-task PATCH composition, preservation of settings and failed/conflicting/pending drafts. Browser QA checks separate PR opening/edit targets, gray empty text, touch controls and narrow wrapping. Storage/daemon tests cover terminal-history preservation, pending/active rejection, optimistic versions, passive old/target authority and polling generation fencing; browser mutations use isolated fixture tasks.
- Independent browser visual/regression QA covers header/search, sources expanded/collapsed, columns/Done/empties, linked/unlinked cards, Overview, Polling, PR/cron SourceEditor, History, create/add/attachment forms and loading/error/conflict/saving states. Check narrow control wrapping, focus/keyboard use, long labels and native drag/drop using isolated fixture data.
- Theme and layout changes preserve schema/configuration/Telegram behavior. Chat replacement also amends the existing chat API and requires a daemon rebuild/restart with Sync state preserved. Install checked static assets and verify live readback.

## Overview autosave

- Test title/description/priority blur, unchanged focus cycles, empty-title validation, exact multiline readback and optional keyboard flush without an ordinary save button.
- Hold saves to prove later blurred edits serialize and survive navigation; preserve failed/stale drafts on return with explicit retry/reload, never certify an unseen newer version or discard typing.
- Verify the first destination click, same-task mutation coordination, separate Ticket/Codex forms and explicit Polling save/activation. Browser tests use isolated tasks, delayed/failing requests and the real local API; live installation readback is GET-only.
- The title stays in one shared heading across Overview/Polling/History. UI integration covers the header textarea's native form association and blur/Enter/Cmd+Enter saves. Browser QA compares title, badges and tab positions for short, multiline and unbroken long titles at desktop/narrow widths, and checks validation plus pending/failed draft text across switches.
- Shared inspector acceptance covers tabs directly below badges and Overview-only Priority above Move, compact ticket/Codex actions and Description below the tabs. Optional provenance links form a list after Description. Connections heading/autosave instruction remain omitted, and priority blur/Cmd+Enter uses the same native form owner. Inline chat attachment opens on demand; Cancel discards only that draft, while tab switches/dismissal retain it. Browser QA checks content switching, native saves, attachment/ticket keyboard actions, visible failures, flat selectors, underline tabs and narrow wrapping with isolated fixture data.

## Dismissed inspector

- UI tests cover the noninteractive empty inspector, Escape/outside dismissal, unrelated URL parameters, inside clicks, card/editor destination actions, IME input, preserved Polling drafts/tab, pending Overview autosave, failed draft recovery and late detail replies. Drag tests prove the first Escape cancels a gesture without closing details and the next Escape clears selection without writes.
- Browser QA checks the static decorative outline, no active/accessible placeholder controls or animations, Escape and outside click on real read-only cards, same-card reopen, another-card selection and narrow-screen layout. Use isolated fixtures for edited drafts; never change real PR tasks for mutation QA.

## Continuation execution inheritance

- `internal/daemon/board_launch_test.go`: Default/Default, model-only, effort-only
  and explicit model/effort continuations preserve unspecified chat settings and
  dispatch only to the linked thread.
- `internal/appserver/client_test.go`: native overrides omit unspecified keys and
  do not emit collaboration presets; explicit mode serialization stays covered.
- `internal/daemon/retirement_test.go`: ordinary input never reasserts cached model
  metadata. Initial external launch coverage checks the same native override path.

## Inspector selector dismissal

- `ui/src/App.test.tsx` covers chip menu open/choose/cancel,
  Escape in column/priority selectors, preserved task URL/selection, and outside
  pointer dismissal including source drafts and destination actions.
- Browser acceptance: open and cancel the column/priority popup without losing
  details; change a column on an isolated task; dismiss with an outside pointer
  press and reopen.

- `ui/src/ChipSelect.test.tsx` covers checked current values, keyboard choice,
  local Escape, focus/form ownership, outside close, blur boundaries and disabled
  controls. Existing inspector tests retain priority autosave/version protection.
- `ui/src/drag.test.tsx` covers hidden Done drop/cancel/conflict rollback and
  expanded Done behavior without changing visibility preferences.
- Browser QA measures Show Done versus its drag target: equal width/position,
  increased height and identical board/column bounds; perform a real mouse drag
  to Done and check the existing move command, hidden column and restored toggle.
  Check chip menus at desktop/narrow widths and that page color tokens stay intact.

## Failed merge checks

- `internal/arcanumreview/checks_test.go` covers authored-open discovery, complete
  check pagination, explicit failure versus pending/unknown, required-only
  filtering, the two review exclusions, links, invalid diff IDs and incomplete or
  repeated-cursor pages. Snapshot tests cover a draft head over published diffs.
- `internal/polling/merge_checks_test.go` covers healthy-candidate rotation,
  discovery before Request changes, unchanged Request changes behavior, opt-in,
  read errors and child baselines. A real isolated SQLite test accepts a same-chat
  continuation, reopens/rearms without duplicates and observes a retry edge.
  `pr_test.go` covers new revision/check identities and unknown observations.
- UI integration covers independent, initially disabled source and child waiting
  options and saving the new ANY condition without changing merge completion.
- Live read-only QA reads published checks through the configured CLI and runs
  discovery/acceptance into an isolated database; it never dispatches its requests
  to Telegram or App Server. Browser QA uses isolated request fixtures for editing
  source and ordinary wait settings. Deployment preserves Sync state.

## Independent polling rules

- `internal/polling/rules_test.go`: same-snapshot AND versus latched facts,
  unconsumed partial evidence, unchanged rearm dedup, Done arbitration, failed
  reads, cron once rules, source expressions and child accepted baselines.
- `internal/storage/store_task_rules_test.go`: atomic TODO continuation/manual or
  auto claim, generation stop, Done without launch, cursor upgrade/identity reset,
  and no-model source creation while discovery stays active.
- `internal/daemon/board_launch_test.go`: automatic same-chat continuation waits
  for visible Telegram rendering and idle authority, then starts once without
  steering, creating a replacement thread or replaying.
- UI workflow, source and rule tests cover reusable rule editing, AND/OR, explicit
  destinations, automatic/manual choices, multiple copied rules, cron validation,
  lazy legacy conversion, stale drafts, keyboard saves and independent source
  versus child conditions. Browser fixtures check source/task editing and readback
  at desktop and 1024/736/375/320 px; page color tokens must stay unchanged.

## Compact polling cards

- `ui/src/PollingRules.test.tsx`: closed When/Then summaries; source frequency and
  prompt settings in separate disclosures; destination/launch invariants; prompt
  retention across mode switches; AND/OR editing; cron add/remove; source-child
  placeholders; local Escape and focus restoration.
- Workflow/App integration retains atomic saves, guarded activation, native
  keyboard validation, task selection, version conflicts and source-child copies
  through the new disclosure controls.
- Browser fixtures check PR/cron source settings, status and launch menus,
  same-form save/readback, nested Escape, source-child copying, error/conflict
  drafts and 1024/736/375/320 px wrapping. OR stays with the following group.
  Install the checked UI assets and verify exact live asset readback; no daemon
  restart is needed for this presentation-only change.

- Workflow and source integration tests cover execution fields inside prompt
  panels, shared values between rules, hiding fields for no-launch actions and
  preserving hidden permissions in the saved task/child configuration. Browser
  acceptance checks nested disclosure layout and narrow-screen wrapping; new-task
  and source forms must not expose permission controls.

## Source When/Then cards

- `ui/src/SourceEditor.test.tsx`: distinct PR source/events, AND/OR editing,
  legacy flags without accidental opt-in, independent template and launch fields,
  destination/launch invariants, retained drafts/permissions, PR scope/Cron
  switching, trusted-author fallback, nested Escape and missing-field recovery.
- App source tests retain creation, pause with unsaved edits, atomic child copies,
  explicit keyboard saves, external refresh failures, stale-version fencing and
  authoritative normalization while preserving typing during a save.
- Browser fixtures check source and child event independence, failed save/retry,
  exact payload/readback, menu/panel Escape without dismissal, source pause and
  narrow wrapping. Verify 320/375/736/1024 px, existing palette, no permissions,
  and separate Cron source / scheduled event. Install assets and verify live
  byte readback without dispatching Telegram or LLM work.

## Friendly visual style

- Existing UI suites retain task selection, source icons, priorities, footer
  actions, full descriptions, explicit workflow saves, nested Escape and drag
  behavior. Quiet empty placeholders keep accessible empty/loading labels.
  Collapsed Sources retain clickable icons, status dots and name/status tooltips;
  the source can be opened and edited while the rail stays collapsed.
- Browser fixtures check cards, inspector tabs, source/prompt panels and polling
  rules at 1440/1024/736/375/320 px without horizontal page overflow. Check original
  service icons, linked/empty footer actions, request hints and source provenance.
- Check hover lift, pointer-press feedback and selected-card lift; pause those
  effects while drag/save placement animations own position. Disable motion with
  reduced motion.
  A mouse drag to collapsed Done must keep the board and toggle slot unchanged;
  only the target height grows from 30 to 88 px. Reduced motion disables style
  transitions and the existing running animation. Install static assets and
  verify exact live byte readback; no daemon restart is required.

## Smooth card dragging

- `ui/src/drag.test.tsx`: a single original slot, no same-position write, full-card
  pointer preview including live priority/footer, activation threshold, suppression
  of the release click, touch grip versus scrolling, mismatched pointers, outside
  release, Escape/blur/pointer cancellation and cleanup on unmount. Existing
  reorder, stale-read, failed-save, Polling validation and hidden Done cases share
  the move path and retain version/draft coverage in App integration.
- Browser fixtures verify full-height cards of different sizes, neighbor movement,
  cross-column landing, a delayed save failure returning the preview, zero leftover
  hidden cards/previews, fixed Done geometry, edge scrolling and reduced motion.
  Check 1440/1024/736/375/320 px wrapping and original footer/control data. Only
  fixture writes are used; install assets and read them back without restarting
  the daemon or dispatching Telegram/LLM work.

## Inspector placeholder and transitions

- `ui/src/useDetailPresence.test.tsx`: keep the same editor/input through exit,
  hide closing controls from assistive technology, reject an obsolete animation
  completion after reopening/switching, and clean up on unmount/reduced motion.
- App integration covers direct card replacement without an intermediate empty
  inspector, together with existing Escape/outside handling, autosave and retained
  Overview/Polling/source drafts.
- Browser QA compares actual versus placeholder tab bounds, inert exit controls,
  rapid close/reopen and switching, long-description scroll during exit, and
  reduced motion. Check 1440/1024/736/375/320 px and source/new-task editor layout.
  Install static assets and verify exact live readback; no daemon restart.

- Directional motion browser QA checks card activation upward, detail entry from
  above and exit upward, and both tab directions while the header stays fixed.
  Confirm text-free placeholders, rapid reversal cleanup, no overflow while moving
  at 320/375/736/1024/1440 px, menus after transition, reduced motion and dragging
  immediately after selection without an extra slot or position jump.
- `ui/src/useTabSlide.test.tsx`: outgoing pictures are inert and stripped of form
  identity/association; real inputs and FormData remain intact; obsolete animation
  completion cannot remove a new transition; reversal, unmount and reduced motion
  clean up without modifying the editor's draft.

- Check that the shared active-tab capsule follows both content directions, stays
  aligned on resize and handles rapid reversal and reduced motion without adding
  focus targets or changing selected-tab semantics.
