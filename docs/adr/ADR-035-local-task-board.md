# ADR-035: Local task board and database-owned polling

- Status: accepted
- Amends: ADR-030, ADR-031, ADR-034

## Decision

The personal queue lives in the existing daemon and SQLite database. The browser is a local client, not a second scheduler. Four manual columns are Inbox, TODO, Polling and Done. A separate optional running/completed indicator derives from App Server snapshots; completion does not mean success or user acceptance. Opening a card never clears Inbox.

Tasks can have one polling configuration. The Polling column enables it; leaving the column retains its settings and invalidates in-flight checks by generation. A repeat/create_task action makes a permanent source, displayed compactly in the collapsible left sidebar. An ordinary once wait returns the same task to Inbox. The center is kanban, and the right panel edits details/settings. Ship/completion never automatically rearms a task.

Each source owns its child task definition directly: title/description, initial and continuation prompts, execution settings and optional inactive child waiting settings. A new task copies this definition and is self-contained. Later source edits affect future tasks only; there is no separate Templates entity or shared definition relation. The launch request independently freezes its final prompt and execution settings. The browser edits a source’s explicit initial_launch policy (manual, trusted_authors for PR sources, or auto), independently of Codex execution approvals. Missing policy means manual. Each new automatic initial request has a visible Telegram card before claim. Continuations always require Telegram Start. Existing requests preserve their frozen policies. The once-only imported review-source upgrade restores the configured legacy author allowlist only when no explicit board policy exists; custom names/policies survive. Feedback and previously imported cron sources remain manual; fresh cron imports retain configured manual/auto policy.

`tasks`, `task_polling`, and `task_events` extend the current schema. Event deduplication, cursor acceptance, child definition copying, task changes and launch-request creation share one transaction. Events are a compact history and deduplication barrier, not an event-sourcing architecture. The request stores task/event/target-thread linkage. A source key identifies a PR cycle; the continuation request is keyed by the distinct event, not just PR ID.

TODO/Done cancels definitely unstarted requests under the same daemon gate used by launch claim/dispatch. Already started or ambiguous work is neither interrupted nor replayed. Continuation reads authority, resumes only the original thread, and starts one turn. It never steers a busy chat or creates a replacement thread. Busy requests remain pending for a later explicit Start.

One in-process Go runner performs bounded, non-overlapping checks. Adapters return facts and cursors, never launch models or send Telegram messages. PR conditions combine with ANY: new published source head, replies to all tracked own issues, all tracked issues resolved, or a transition to Request changes. Draft/deleted/unknown comments, missing data and empty issue sets do not prove completion. Nested replies follow parent IDs. A reply's resolution does not resolve its parent issue. PR observations are not transactional across API calls; ordinary polling consistency is acceptable.

The cursor retains the last accepted instruction's head, rather than claiming which revision an LLM reviewed. Explicit rearming includes issues created during the preceding pass without overwriting the old head before comparison. Some redundant checks are preferable to dropping changes between a pass and rearming. PR waits with complete_on_merge enabled move to Done on an authoritative merged observation (see amendments below); closed-but-unmerged PRs keep their existing behavior.

PR and cron configuration is imported once into database-owned sources. Durable legacy request identities become task/event barriers. The old loops do not run alongside the board runner. Cron keeps the existing current-day/current-week catch-up, lateness bound, local scheduled-date identity, and startup/resume stabilization. Configuration file edits after import do not override UI edits.

The existing shared App Server projection observes the union of Telegram topics and linked board chats. A board chat remains observed with Sync off or after topic expiry. Passive board-only observation reads snapshots without `ThreadResume`, so adding a card cannot make historical chats active. For unbound linked board chats, a known old terminal observation prevents metadata-only activity bumps from rediscovering a topic. Active/waiting chats bypass this filter; newly completed work becomes discoverable after projection catch-up. Missing observation history keeps the normal discovery behavior. Telegram delivery remains conditional on its routing state. The browser consumes HTTP commands and SSE invalidations, refetching on connection/reconnection/focus and periodically. Correctness does not depend on delivery of every SSE frame.

React/TypeScript/Vite sources live in `ui/`; Go serves a configured static directory beside `/v1` routes. Static assets are not embedded in the binary. Exact loopback Host and same-origin JSON mutation checks protect the local surface. Origin-less JSON requests support the explicit add-chat skill. There is no wildcard CORS, API fallback to the SPA, arbitrary shell poller, Python plugin, or additional App Server process.

## Consequences

Changing Go checks requires the normal fast build/restart, preserving Sync state. UI edits use Vite independently; settings edits require no rebuild. The first version supports PRs, cron and chats; deployment monitoring is deferred. The task remains the persistent entry point while Telegram handles launch approval and Codex handles discussion/result review.

The source detail panel combines “When create task” (trigger, schedule, on/off and diagnostics) with “Task to create” (title, prompt, CWD, compact model/reasoning and optional repeat-work defaults). Trigger and child definition save atomically through polling settings. A provenance link identifies the source without making an existing child inherit edits. Upgrade atomically copies legacy source references from the referenced template definitions, never from unused source task config. Historical task configs and frozen requests remain unchanged. The old template table and columns may remain inert; fresh databases and runtime APIs do not create or expose templates.

## Launch metadata and task titles amendment

New launch cards use the source name for Source and the actual current PR snapshot author for From. Child tasks persist source_author independently of their optional waiting configuration; continuations fall back to this durable provenance if the event omits the author. Request prompts are exactly their configured text after one-pass rendering. Observation facts and author remain in task_events and are not appended to prompts.

Each new source child title is centrally composed from the current source name and configured child title. Cron inserts the scheduled occurrence date/time in its configured timezone immediately after the source prefix, including catch-up occurrences; legacy Scheduled task filler is omitted. The default Assigned PRs source is renamed PR review once by exact imported-source identity/name. Custom source names, historical task names and frozen request titles/prompts/settings are preserved. Source edits affect future tasks.

## Task ordering amendment

Drag moves use an optional before-card neighbor on the existing version-checked MoveTask command. The storage transaction rebalances destination ordering coordinates without changing other cards' editable versions or state. Equal historical positions use ID as the stable tie-breaker. Same-column reordering is an ordering operation: it records a reordered event, preserves polling generation/readiness and requests. The reorder-only command bypasses the launch gate; column transitions use it. Column transitions retain readiness checks and request cancellation. Browser insertion previews and optimistic saving defer snapshot replacement until the gesture/save ends; failure rolls back and reconciles.

## Board performance amendment

Ordering has a dedicated `reorder` command. Its transaction checks both expected version and unchanged column; a stale command cannot become a column transition or cancel a launch. It returns a compact card and never takes `syncMu` or `taskLaunchMu`. Creation also does not wait for Sync. Column transitions, request claims/retries and polling outcomes share `taskLaunchMu`. Dispatch acquires `syncMu` first, then `taskLaunchMu`; no path takes them in reverse order. A column transition can wait for an active dispatch to finish because cancellation must distinguish definitely unstarted requests from possible App Server side effects. Unrelated background Sync does not block board creation or column transitions.

The board and SSE reconciler use the compact `TaskCardDTO` projection from `GET /v1/task-cards`: identity/order/version, display text, polling health, execution status and launch-request status. This projection reads neither full chat snapshots nor frozen launch payloads; polling configuration is read internally for pure readiness diagnostics, but is not returned. Details stay in `GET /v1/tasks/{id}` and are loaded when a task/source is selected. Legacy `GET /v1/tasks` remains available. Separate card/detail types prevent summary data from entering settings editors as empty defaults. Selection/version guards reject stale detail responses; failed detail loading exposes retry and preserves an existing editor/draft. A compact reorder advances a loaded detail version only when that detail matches the command's expected version and the response advances it by exactly one; otherwise it retains conflict protection and refetches full settings.

## Task detail tabs amendment

Ordinary task details use Overview, Polling and History. Overview edits title and multiline description. Polling groups the task's source link, prompts/execution settings and waiting configuration; source links are provenance, while the PR wait target is its explicit PR ID. SourceEditor keeps the source/child-definition flow. History retains task events and frozen launch-request history. Each editor preserves fields it does not own and keeps version conflict protection; full detail refreshes may advance a draft's baseline only when its owned settings are unchanged.

## Task priority and chat shortcuts amendment

Tasks persist low/normal/high priority, defaulting to normal through an additive schema upgrade. Priority is displayed as an accessible card icon and edited in Overview; manual ordering remains independent. Legacy updates which omit priority preserve it. Source children default to normal rather than inheriting source priority.

Overview shows Open in Codex for linked tasks, or permits attaching an existing chat by ID/deep link. The dashboard exposes a separate Codex link without nesting interactive controls in the selection button. Linking passively reads App Server authority and atomically checks task version, unique chat ownership and external launch/history conflicts under the launch gate; it never starts/resumes a chat. Pending or uncertain launches require explicit operator handling before linking. Settings, column and polling cursor survive; polling generation fences in-flight outcomes against the new identity.

Cmd+Enter saves the focused detail form using normal validation and busy/IME/repeat guards. The workflow inspector amendment below combines ordinary execution and waiting settings in one form. A newer full save response cannot grant the old submitted settings the external edit's version; editors preserve conflict protection or display authoritative returned fields.

## Inline card controls amendment

Ordinary kanban cards edit priority and always show chat linkage. Linked Codex controls open the chat; unbound controls reveal an inline attachment form without navigating away. Priority and chat controls stay outside the draggable selection button. Priority has a dedicated versioned compact command which changes only priority, version, updated time and history; it does not load full chat details, rearm polling, cancel requests or enter Sync/launch gates. A compact response may advance a loaded detail's priority/version only when that detail matches the submitted version and the response is exactly the next version with the requested priority; all other cases retain conflict protection and refetch full details.

Overview may adopt a newer priority while retaining title/description drafts only when server title/description still match the baseline and local priority is unedited. A locally edited priority keeps conflict protection. The inline chat form supports cancellation and retains its input on rejected attachment.

## External ticket link amendment

Tasks and compact cards store an optional ticket_url through an additive empty-default schema column. It is separate from source_url, which remains PR/source provenance. New source children default to no ticket link. Legacy task PATCH preserves ticket_url when omitted; a dedicated compact metadata command adds, replaces or clears it without entering Sync/launch gates or changing polling/runtime/order/settings. HTTP(S) absolute URLs without credentials are accepted; URLs are never fetched by the daemon. Cards and Overview expose the external link plus an editor; empty state permits adding the link immediately. Ticket editors retain drafts on failed/conflicting writes. Compact response guards advance full detail versions only for exactly known metadata changes, preserving unowned editor drafts.

## Ticket keys and completed PR reviews amendment

Ticket links display the final nonempty path segment while preserving the full href. A short tracker key is normalized to its full HTTPS tracker URL in create, optional PATCH and dedicated ticket commands. Ticket labels and shorthand do not add tracker integration.

An authoritative merged observation ends an active PR wait with complete_on_merge enabled: atomically move Polling to Done, increment task version/polling generation, record history and cancel only definitely unstarted launch requests. No continuation event or LLM launch is generated. Active/ambiguous work is not interrupted. Existing generation/column ownership rejects stale outcomes. Generated review and feedback cycles default to enabled independently of display names; sources remain active and other tasks require an explicit enabled option. Closed without merge does not complete the task. Terminal merged reads need no comment/change comparison.

## Configured identity and merge option amendment

Arcanum login is daemon configuration, rather than an editable board field. Pure normalization applies the configured identity during typed validation and checker execution, including source-owned child waits. Persisted legacy logins do not override configuration. Malformed configs and missing required configured identity fail validation instead of silently selecting another account.

PR wait config supports optional complete_on_merge. Explicit true/false controls completion independently of source names. Missing values default to true for generated review and feedback tasks, based on durable provenance; other legacy tasks default to false. PR source child waits default to true and cron child waits to false. Full detail reads expose effective values without rewriting stored settings or versions. Source edits continue to affect future children only. Merge completion retains the atomic transition, generation fences, history and safe cancellation described above.

## Workflow inspector amendment

The source inspector shows numbered When, Create task and optional Waiting settings for new tasks steps. Future waiting settings are visually distinct and explicitly copied into future tasks only, for later manual activation. The ordinary Polling inspector shows Wait, After event, Continuation prompt and Approval, plus a separate PR merged → Done terminal option. A concrete monitored PR ID links directly to that PR; placeholder IDs in source child settings are not links. Missing chat readiness is visible, with an attachment flow which preserves the workflow draft. Return to Inbox requests no model; continuation still goes through Telegram Start into the same chat. Initial prompts, execution settings and provenance remain available in collapsed sections. The sidebar, kanban, Overview and History remain unchanged in purpose.

One ordinary workflow save sends task fields and optional polling through the existing PATCH command. SQLite validates the resulting state and writes both atomically, advancing version/generation once with the existing cursor identity rules. Source-owned settings remain on the source polling command. Omitted/null polling preserves old client behavior; no schema or new route is added. Activation is an explicit guarded move after saving, not a model launch. Frozen requests, chat uniqueness and Telegram authorization do not change.

## Shared visual design amendment

The board uses an Arcanum-inspired dark visual system: gray-purple surfaces, blue links/selection, purple primary actions, and compact outlined controls. Shared semantic color tokens cover forms, diagnostics, History, workflow steps and transient/error states. Native controls use the dark color scheme. YS Text loads from Yandex's font CDN with a system-font fallback. Compact cards integrate their ticket/Codex footer inside a common visual boundary and expose one editable priority control. Selection/drag and priority/link/edit targets remain separate DOM controls; insertion geometry still measures the complete card wrapper. Description line breaks, independent polling/execution indicators, empty connection actions and transient drag/save states remain available. Completed is neutral and never a success verdict.

Overview is a readable editable task brief: native title/description fields, priority, chat/ticket controls and provenance/source links. It no longer renders polling diagnostics/conditions, metadata dates, launch requests or frozen prompts. Stored data, Polling and History are retained; this is a presentation change only. Task moves remain independent of Overview edits; keyboard/version/draft protection remains intact. Numbered source/task polling workflows, History and create/attach/error/loading states share the visual system without new commands, package dependencies, schema or API changes. Static deployment needs no daemon restart.

Compact footers use a distinct darker surface and bundled Arcanum, Tracker and Codex icons. Recognized Arcanum source URLs expose the PR number as a separate link without loading full task details; other source links retain their generic Overview representation. Ticket links keep their existing explicit editor and version guards. Its pencil overlays the service-icon slot only on hover or keyboard focus, without widening the footer; touch pointers keep an explicit edit action. Codex controls retain accessible names and opening/attachment behavior while showing only the icon, a plus for an unlinked chat and an animated ring for an active run. Reduced-motion mode keeps the ring static. Completed/failed/interrupted runs show no card execution badge or extra icon marker; completion remains neutral. Polling badges, launch-approval hints and inspector status badges retain their separate meanings.

## Overview autosave amendment

Overview title, description and priority save on field blur without an ordinary Save overview action. A small per-task pending draft survives editor navigation, serializes blurred changes and retains failed/stale edits for explicit retry/reload. Unchanged drafts and invalid titles do not write. Saving feedback is local to the editor; destination clicks and same-task commands retain their expected-version protection. Ticket/Codex attachment and Polling commands remain explicit. Cmd/Ctrl+Enter remains an optional flush. This change does not alter storage/API/launch behavior or require restarting Sync.

The task title occupies the same inspector heading above badges and tabs in Overview, Polling and History. Overview edits it through a native textarea associated with the existing Overview form by its form attribute; the other tabs display that same draft text. Shared typography and an invisible sizing mirror give editable/read-only titles the same wrapping and height. Tabs sit directly below the status badges; only the title, badges and tabs remain shared across views. Priority, Move, ticket/Codex actions and Description are Overview content below the tabs. That content stays mounted and hidden outside Overview to retain drafts and form association. Save/retry/conflict feedback remains visible on every tab. Blur, validation, composition and Enter/Cmd/Ctrl+Enter keep the same protection.

Ticket/Codex actions sit below Priority and Move without a visible Connections heading or autosave instruction. Optional Open source and Created by source links form a semantic list below Description, leaving room for further task references. The card footer and inspector share the Codex action renderer. Unlinked chats open an explicit inline attachment form with Cancel; its draft survives tab switches and inspector dismissal. Overview settings use flat surface-filled selectors, and the active tab uses a blue underline. The static empty inspector follows this same arrangement without active controls or animation.

## Source identity and editable connections

PR review sources and their children use a neutral eye icon; PR feedback uses a comment icon. Classification uses typed source settings, durable event-key namespaces and source_task_id relationships, never display titles. Overview's connection row now includes the PR link alongside ticket and Codex controls; only generic source URLs and Created by source remain in the reference list. Missing PR is a passive muted placeholder; missing ticket keeps a muted icon/text attachment action. Linked Codex controls expose a hover/focus pencil and explicit replacement form with Cancel. Replacement drafts survive tabs/dismissal and unrelated versions; changed bindings require reload, and input typed while saving is retained. Backend replacement observes both chat authorities, rejects unfinished old turns and pending/uncertain task launches, preserves frozen terminal requests and advances the existing version/generation fences. It never starts or interrupts a turn.

This binding amendment requires a normal daemon rebuild/restart with Sync state preserved.

The PR placeholder is now a muted Add PR action in both footer and Overview. An existing PR has a hover/focus pencil, with a separate touch control. The explicit editor accepts a positive PR ID or Arcanum review URL; empty clears the link. It retains drafts across tabs, failed/conflicting saves and typing during a save. PR changes use the existing version-checked task PATCH after settling owned Overview saves and reading that exact full task version, preserving fields omitted by compact cards. Only the displayed source_url changes; durable source identity, chat/ticket links, frozen requests and polling target/settings remain intact. The existing PATCH generation fence still advances. This UI amendment needs only static asset installation.

## Dismissed inspector amendment

With no selected card, the inspector shows a muted, static outline of Overview rather than loading feedback. The decorative shape has no animation, form controls or accessibility-tree content; a readable caption invites selection. Escape or a primary click outside the inspector clears the visible selection and removes only the task query parameter. Clicks on another card/source or an editor action still perform that destination's action. Escape during a drag cancels the drag first; composing input and unrelated creation/linking editors are not dismissed.

Dismissal blurs the active Overview field so its existing autosave rules run. The last editor remains mounted and hidden to preserve explicit-save drafts; reopening that same card restores its tab. Detail reads pause while dismissed and late replies cannot reopen the inspector. Failed Overview drafts remain reachable from the board. Dismissal itself adds no storage command or task-state transition.

## Compact task inspector amendment

Ordinary tasks no longer have a Task details strip or an Overview connection row.
The shared header puts the editable column badge, independent execution projection
and priority on one row above the tabs. Column selection still uses the existing
MoveTask command and preserves autosave/version guards; priority retains its
Overview form association and blur/keyboard saving on every tab. PR, ticket and
Codex controls remain on kanban cards. Generic source/provenance references remain
below Description. The static empty inspector follows the compact layout.

Description grows to its full editable content height without a preview cap or
expand/collapse actions. Content longer than the window scrolls with the inspector,
so available vertical space is used without nested description scrolling.
Source-derived eye/comment icons are inline
prefixes of card titles, rather than a separate flex column, and also prefix the
inspector title. Existing typed source/provenance classification is unchanged;
ordinary tasks acquire no icon. This is a static UI update without daemon changes.

## Continuation execution inheritance

Task continuations use native `turn/start` model/effort overrides. Empty execution
fields are omitted, so App Server keeps the original chat's current settings;
explicit model and reasoning values override independently. Continuations do not
install a default collaboration preset, which could clear an unspecified effort.
Initial launches use the same native override path. Ordinary Sync input omits
model/effort rather than reasserting cached thread metadata. Explicit collaboration
mode requests retain their existing preset behavior. Frozen requests, prompts,
permissions, thread identity, writer ownership and no-replay fences are unchanged.

## Native selector dismissal

Inspector dismissal observes a primary pointer press outside the panel rather than
an outside click. Native selector popups may retarget their final click to the
page; that click must not clear the selected task. Escape on a native select is
reserved for its popup. Outside pointer presses still blur/autosave the editor
before the destination action, and Escape during a drag still cancels it first.

## Chip selectors and hidden Done drop target

The inspector column and priority use compact chips with an anchored listbox,
current-value checkmark and keyboard selection. Escape closes the open list before
it can dismiss details; focus stays on the trigger. Priority keeps its Overview
form ownership and blur/Cmd+Enter autosave, including on other inspector tabs.
Creation forms and card priority controls retain native selectors. These controls
reuse the current color tokens without changing the page palette.

During an internal card drag with Done hidden, the existing Show Done button becomes
a taller Done drop target at the same position and width. An in-flow sizing slot
preserves the original header footprint; the target grows over the board without
resizing columns or revealing Done. Drop uses the existing version-guarded move
command. Cancel, success and failure restore the toggle, retaining the user's Done
visibility preference and existing rollback/conflict behavior. Expanded Done keeps
its ordinary column target. Only static UI assets change.

## Failed merge checks

PR feedback sources may opt into `check_merge_failures`. Discovery then scans the
account's open published PRs and accepts Request changes OR a failed required
merge check. A bounded source pass remembers its next candidate so healthy PRs
do not continually occupy the first five slots. Existing PR task identity and
source deduplication remain unchanged.

PR waits may independently select `merge_checks_failed` among their ANY conditions.
Both paths use the same predicate: required and status=failure, excluding the
Arcanum approved and comment_issues_closed checks. Pending, skipped and unknown
are not failures. Checks are read for a specific published diff with complete
pagination; a draft head preserves the latest published diff for checks without
exposing that draft as a new commit. Unavailable/incomplete pages are errors and
must not consume the wait cursor or queue a partial result.

The existing JSON cursor remembers the published diff and previously observed
failed check keys. Initial failures, newly failed checks, a new published revision
and observed recovery/retry followed by failure may wake a task. Rearming or
restarting with unchanged failures does not. A retry entirely between polls with
identical end state is indistinguishable without additional run identity.
Accepted events retain failed check names, descriptions and available URIs. The
existing atomic event/request path, permissions, initial launch policies and
manual continuation approval remain authoritative; no schema or queue is added.

Both options default off. Source discovery and child waiting are independent;
source edits affect future children. Standard bootstrap prompts mention merge
checks, but saved custom prompts and existing tasks are not rewritten. The UI
explains the exclusions and asks the operator to include check inspection in the
prompt. No CI logs are collected by the poller.

## Independent polling rules amendment

Ordinary tasks now configure `type: rules`, with 1–20 named rules in config.
Each leaf selects one PR or cron source, and an explicit action destination.
PR event groups are ORs of AND terms evaluated against the same current snapshot;
no term is latched across passes. A per-check PR cache makes same-PR rules see
one snapshot and one published check response. Accepted fingerprints and existing
head/reply/check baselines prevent unchanged evidence from firing after rearming.
Legacy flat conditions retain their transition semantics until explicitly edited.

The existing single task_polling row owns the entire list. Rule cursors and check
deadlines are stored by stable rule ID in the owner cursor. No new SQL table,
scheduler, event bus or launch queue is introduced. Done rules are checked first,
then configured order; the first matching due rule supplies the one action.
A failed read fails the pass without accepting partial cursors/events.

Actions are Inbox/Done without launch, or TODO plus a same-chat continuation with
Telegram Start or automatic start. All rules stop when that action leaves Polling;
only explicit re-entry rearms them. PR merged → Done is a normal PR rule, replacing
the special checkbox in the editor. No merge watcher survives a prompt rule.
The accepted leaf is resolved from persisted settings under the generation fence.
Cancel older definitely unstarted requests before inserting the new request, in
the same transaction as the destination, event and cursor. TODO requests may be
claimed; manual moves to TODO/Done still cancel definitely unstarted requests.
Already started or ambiguous work is never interrupted or replayed.

Automatic continuations use the existing queue and dispatcher, remain visible in
Telegram before claim, preserve frozen execution settings, wait for an idle target
and use only the attached chat. Source launch policies additionally support `none`;
the explicit destination applies to the child, while the source keeps discovering.
The source editor uses the same Wait/events/After structure and copies multiple
inactive child rules. Existing trusted-author policies remain supported.

Upgrade is lazy and non-destructive: legacy active waits remain runnable; opening
the editor projects their merge choice into a separate rule, and saving converts
the definition. `legacy-wait` retains the old accepted cursor for the same PR.
Stable rule IDs retain matching identity state across edits; changed identities
reset only that rule. Copied source evidence seeds matching child-rule cursors,
including the accepted fingerprint. Frozen launch requests are never rewritten.

## Compact rule cards amendment

Ordinary and copied child rules use compact When/Then cards. The source chip
opens PR identity/check frequency or cron settings. Event groups keep AND within
and OR between groups, with editing available from the displayed expression.
Then has two separate blocks: destination status, followed by launch mode and
its prompt. Prompt text belongs to that launch block; it has no separate card
control. Detailed fields, rule naming/removal and diagnostics open on demand.

The editor maps these two blocks to the existing action contract: TODO requires
manual or automatic continuation; Inbox/Done means no launch. Choosing a prompt
sets TODO, and choosing no launch from TODO sets Inbox. Switching modes retains
the draft prompt, while stale-version and explicit save/activation guards remain.
Panel Escape restores focus to its trigger and leaves the task selected; an open
chip menu consumes its own Escape first. Colors, server configuration, polling
execution and frozen requests are unchanged.

Execution fields (working directory, model and reasoning) live inside the prompt
launch panel, including copied child rules. They still edit the existing shared
task configuration, also used by initial launch; the UI states this scope.
Without waiting, initial-launch settings retain these fields. Permission controls
are omitted from all board forms; existing permission values and server defaults
are preserved when editing visible execution fields.

## Source cards amendment

Sources use the same compact When/Then grammar as task rules. Arcanum PR is the
source; authored versus assigned-review scope and frequency live in its settings.
Authored PRs retain `pr_changes_source` and expose Request changes / required
failed-check groups separately. Assigned reviews retain `pr_review_source` and
its Assigned as reviewer event. Cron retains its scheduled event. Feedback
summaries translate legacy check flags without modifying their configuration
until an explicit event edit. The event summary/editor is shared with task rules.

Then opens the child template, destination and initial-launch settings separately.
Prompt modes require TODO; no-launch sources create Inbox or Done tasks. Prompt,
variables, trusted authors and execution settings live in the launch panel; the
configuration stays shared with child continuation prompts. Child waiting rules
remain an independent copied definition and require manual activation. The source
keeps discovering events after creating a task. No backend contracts change.

## Friendly visual style amendment

The board uses rounded column outlines and title capsules, softer task-card
borders with a purple selection ring, tinted source-icon backings, capsule
selectors and primary buttons, and segmented inspector tabs. Existing palette
tokens, service branding, footer links, data, controls and behaviors are retained.
Task details keep their provenance without duplicating the card footer.

Cards lift slightly on hover and selection and depress on pointer press. Motion
for hover and press pauses during drag/save while placement animation owns the
card position; the collapsed Done drop target retains its fixed slot and width.
Focus indicators remain visible, and reduced motion disables movement as well as
transitions.

Explanatory workspace, sidebar, column and workflow captions are omitted. Empty
columns use a small decorative card stack with an accessible empty/loading label;
the inspector keeps its noninteractive outline without a visible caption.
Feedback uses a simple speech bubble and schedules a larger outlined clock.
Collapsed Sources retain every source button, its active/paused status dot and a
name/status tooltip and accessible label; opening and editing a source is unchanged.

## Smooth card dragging amendment

Pointer dragging uses a 6 px activation threshold and a noninteractive full-card
preview that retains the footer and live form values. The original card stays
mounted outside flow and a single, text-free slot starts at its original position.
Moving the slot does not duplicate the original footprint. Neighbors animate to
new layout positions; hit testing excludes their visual translation. Native drag
handlers retain the same ordering/validation path as a fallback.

Release commits the existing optimistic move immediately while the preview lands
with a small settling motion. It retargets to the real card position if validation
or an asynchronous save failure restores the original location. A hidden Done
accepts a shrinking preview without revealing the column or changing its slot.
A same-position release, outside release, Escape, blur or pointer cancellation
performs no write. The gesture's synthetic click cannot open a task afterward.

Touch scrolling remains available on card bodies; the existing grip starts touch
dragging. Edge scrolling, focus controls and reduced-motion settings are retained.
The rendering layer cleans up previews, listeners and animations on unmount.
Version fencing, draft settlement, queued refreshes and rollback stay in the
existing move operation; neither endpoints nor persistence contracts change.
