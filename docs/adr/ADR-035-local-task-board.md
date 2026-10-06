# ADR-035: Local task board and database-owned polling

- Status: accepted
- Amends: ADR-030, ADR-031, ADR-034

## Decision

The personal queue lives in the existing daemon and SQLite database. The browser is a local client, not a second scheduler. Four manual columns are Inbox, TODO, Polling and Done. A separate optional running/completed indicator derives from App Server snapshots; completion does not mean success or user acceptance. Opening a card never clears Inbox.

Tasks can have one polling configuration. The Polling column enables it; leaving the column retains its settings and invalidates in-flight checks by generation. A repeat/create_task action makes a permanent source, displayed compactly in the collapsible left sidebar. An ordinary once wait returns the same task to Inbox. The center is kanban, and the right panel edits details/settings. Ship/completion never automatically rearms a task.

Each source owns its child task definition directly: title/description, initial and continuation prompts, execution settings and optional inactive child waiting settings. A new task copies this definition and is self-contained. Later source edits affect future tasks only; there is no separate Templates entity or shared definition relation. The launch request independently freezes its final prompt and execution settings. The browser cannot approve a launch: new board requests require Telegram Start. Existing legacy request policies are preserved.

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
