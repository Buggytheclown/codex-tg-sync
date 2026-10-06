# Local task board implementation brief

Build a personal local kanban inside codex-tg. SQLite owns tasks, polling configuration, templates, events and launch requests; App Server owns chat execution. First version supports Arcanum PRs, cron and manually linked chats. Release monitors are deferred.

UI: collapsible compact source list on the left; Inbox, TODO, Polling, Done in the center; selected card details/settings on the right. Sources are ordinary tasks whose repeat polling action creates other tasks. Task execution is a separate optional running/completed projection. Reading a card does not move it. No browser approval or extra notifications.

One task spans one PR cycle. A source atomically creates an Inbox task, copies a template and queues a frozen Telegram launch request. Templates have initial and one continuation prompt, execution settings, and optional inactive child waiting settings. Edits affect future tasks only. Polling is enabled solely by the Polling column plus valid configuration. Once polling returns the same task to Inbox; after completion/ship it never rearms automatically. Conditions use ANY; empty comment sets never trigger.

Use four SQLite tables: tasks, task_polling, task_templates, task_events. Add task/event/target-thread linkage and frozen execution settings to external requests. Version and generation guards reject stale writes/checks. Event acceptance, template copy, task transition and request insertion share one transaction. Preserve old database contents and legacy dedup barriers. Import PR/cron sources once and run only the new owner afterward. New board requests require Telegram approval; preserve existing request policies.

Manual chat additions start in TODO and are idempotent by thread ID. TODO/Done cancel definitely unstarted requests under the same launch gate as claim/dispatch; active or ambiguous work is not replayed or interrupted. Busy continuation stays pending for another explicit Start after completion. Continuation must authoritative-read and start only the same target thread, never steer or replace it. Preserve Sync restart behavior.

PR baseline is the last accepted event/instruction, not a claim that an LLM reviewed a revision. Preserve changes between a pass and rearming. Verify actual Arcanum command/JSON contracts before implementing comments/revisions. Merge/close does not silently hide a task in Done.

Extend the shared snapshot producer to topics union tracked task threads, including Sync off/topic expiry. React/TypeScript/Vite lives in ui; Go serves explicit static directory and existing controlapi routes. HTTP commands and SSE invalidation/refetch; reconnect/focus/periodic reconciliation closes notification gaps. Write routes ship with exact loopback Host, same-origin and JSON guards; no wildcard CORS.

Implementation order: capability spike; storage/domain and published DTO; UI/API and Go pollers; daemon integration and explicit add-chat skill; full validation; fresh Sol review and fixes before commit; separate Sol E2E/regression check. No push. Test meaningful transactions, old DB import, cancellation interleavings, busy/missing continuation, empty/ANY PR conditions, cron occurrences, projection without topics, browser flows and existing Telegram regressions. Use dedicated QA data for live checks.
