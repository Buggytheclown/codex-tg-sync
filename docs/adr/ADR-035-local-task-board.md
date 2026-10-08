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

The cursor retains the last accepted instruction's head, rather than claiming which revision an LLM reviewed. Explicit rearming includes issues created during the preceding pass without overwriting the old head before comparison. Some redundant checks are preferable to dropping changes between a pass and rearming. Merged/closed PRs are not automatically hidden in Done.

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

Cmd+Enter saves the focused detail form using normal validation and busy/IME/repeat guards. Polling has separate execution and waiting forms; saving one keeps the other's draft. A newer full save response cannot grant the old submitted settings the external edit's version; editors preserve conflict protection or display authoritative returned fields.

## Inline card controls amendment

Ordinary kanban cards edit priority and always show chat linkage. Linked Codex controls open the chat; unbound controls reveal an inline attachment form without navigating away. Priority and chat controls stay outside the draggable selection button. Priority has a dedicated versioned compact command which changes only priority, version, updated time and history; it does not load full chat details, rearm polling, cancel requests or enter Sync/launch gates. A compact response may advance a loaded detail's priority/version only when that detail matches the submitted version and the response is exactly the next version with the requested priority; all other cases retain conflict protection and refetch full details.

Overview may adopt a newer priority while retaining title/description drafts only when server title/description still match the baseline and local priority is unedited. A locally edited priority keeps conflict protection. The inline chat form supports cancellation and retains its input on rejected attachment.
