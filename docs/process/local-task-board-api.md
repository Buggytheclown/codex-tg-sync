# Local task board API contract

Go DTOs live in internal/model/tasks.go. JSON names are snake_case. `internal/tasks.New(store)` returns `*tasks.Service`.

Service methods (context first):
- `ListTasks(ctx) ([]model.Task, error)`
- `GetTask(ctx, id) (*model.Task, error)`; nil means not found
- `CreateTask(ctx, model.CreateTaskInput) (*model.Task, error)`
- `UpdateTask(ctx, id, model.UpdateTaskInput) (*model.Task, error)`
- `MoveTask(ctx, id, model.MoveTaskInput) (*model.Task, error)`
- `SetTaskPolling(ctx, id, model.SetTaskPollingInput) (*model.Task, error)`
- `AddChat(ctx, threadID, title) (*model.Task, error)`; idempotent, starts TODO
- `ListTaskEvents(ctx, id) ([]model.TaskEvent, error)`

Errors `tasks.ErrConflict`, `tasks.ErrInvalid`, `tasks.ErrNotFound` support errors.Is. Mutations require expected_version. Task update replaces editable title/description/source_url/config; polling settings use their own operation. Column values: inbox, todo, polling, done. Polling requires type, once/repeat mode, object config and action type return_task/resume_task/create_task (create_task also needs an owned child_definition). There is no enabled flag; column activates polling. Sources are tasks with create_task action. Execution is an existing ThreadSnapshotState projection, never separately mutated running state. Requests/history are read-only; pending prompt/settings are frozen.

Suggested routes: GET/POST /v1/tasks, GET/PATCH /v1/tasks/{id}, POST /v1/tasks/{id}/move, PUT /v1/tasks/{id}/polling, GET /v1/tasks/{id}/events, POST /v1/tasks/add-chat (thread_id,title). Successful task mutations return full Task. Lists return arrays. Errors use status 409 conflict, 400 invalid, 404 not found. Exact envelope/SSE can be selected by API owner.

Daemon must call MoveTask under its launch mutex, the same mutex spanning claim through definite dispatch. Store cancellation touches pending and starting requests with empty result thread/turn only; an already dispatched or ambiguous request is preserved. Store additionally guards claim/retry of cancelled tasks. Polling runner uses `ListDueTaskPolling(ctx, now, limit)` on Store and `AcceptTaskPollingOutcome(ctx, outcome, approvalTopicID)`; generation captures the due row. Acceptance is atomic with owned child definition copy, cursor, task event and manual frozen request. No external IO inside the transaction.

## Integration details

Default manual column is TODO. Source event children always start Inbox. A source's `polling.action` is `{type:"create_task", child_definition:{title,description,config,polling?}}`. The definition embeds child settings; no template identity or runtime template routes exist. Definition waiting JSON contains only type/mode/config/action, without runtime cursor/generation/diagnostics. It must be a once return/resume wait; nested create_task actions are rejected. Children copy the definition at event acceptance, retain `source_task_id` as provenance, and do not inherit subsequent edits. Trigger and child settings use one version-checked SetTaskPolling transaction. Source on/off uses the existing move operation.

Child title/description formats take precedence over event labels; supported placeholders use `{{key}}`. Replacement is one pass, and JSON substitution changes string values only. Every source/resume request appends compact event facts plus its source URL, even when the prompt has no placeholders.

Composition calls `Service.SetExecutionDefaults(model.TaskExecutionSettings)` with current launch defaults and `Service.SetPollingValidator(func(model.TaskPolling) error)` with the pure typed poller validator. Defaults are copied when task/source child settings are saved; later changes do not change pending requests. Nullable external request `*string` permission fields preserve legacy fallback, while board requests require explicit permissions and always use manual approval. The typed validator also validates resolved child waiting settings inside the acceptance transaction; PR source child waits may use {{pr_id}}, {{login}} and {{author}} because those sources supply them. Cron child waits require explicit positive PR IDs and reviewer/author values when applicable; PR-only placeholders are rejected on save/create. Child waits are validated before persistence and again after event resolution.

Moving to Polling validates the runnable action. Resume waiting requires a linked thread and a nonempty continuation prompt. Inactive child waiting can be prepared before its initial request launches. Updates to active settings cannot clear required continuation data. Moving out of Polling, replacing waiting settings, or editing task settings advances generation. Rearming preserves the cursor; replacing PR/login/author/schedule identity resets it. Source definition edits affect future instances only.

`Store.ListDueTaskPolling(ctx, now time.Time, limit int) ([]model.TaskPolling, error)` returns only due tasks in Polling. `Store.AcceptTaskPollingOutcome(ctx, outcome model.TaskPollingOutcome, approvalTopicID int64) (bool, error)` returns false for stale/disabled checks; true includes duplicate-only successful checks. `PollingEvent.Cursor` initializes a created child’s baseline. Error outcomes update attempt/error/scheduling diagnostics only, without advancing cursor or accepting partial events.

`Store.BootstrapTaskSource(ctx, model.TaskSourceImport) (*model.Task, error)` atomically seeds a source with its owned child definition and links legacy request IDs to imported cards and event barriers. Import Key is a durable once-only marker; Column defaults Polling and may be TODO for paused imports. LegacyTaskImport carries cycle SourceKey, EventKey, RequestIDs, optional Variables, Polling, Cursor, Title and SourceURL. Adapters select event identities matching their new checker. Existing request prompt/policy/settings stay unchanged. Repeated import preserves local edits. `ListTrackedTaskThreadIDs(ctx) ([]string,error)` supplies the shared snapshot producer's board target set.

Daemon MoveTask runs under the same claim/dispatch mutex. It dismisses pending/starting/definitely failed requests with empty result thread/turn, and expires their callbacks. Started and ambiguous requests remain untouched. Claim/Retry reject board requests in TODO/Done. CompleteExternalLaunchRequest and receipt-confirmed ResolveExternalLaunchOutcome bind task.thread_id in the same transaction as the request result, enforce the target thread, and preserve the card column. Durable Sync receipts now retain their confirmed turn_id for exact initial-request recovery.

Startup upgrades legacy create_task action references by copying the referenced template definition into action.child_definition and removing template_id, in one transaction. It never derives child settings from source.config or rewrites ordinary task configs/requests. Reopening preserves edited owned definitions. Existing source events supply source_task_id provenance. Legacy template storage remains inert; /v1/task-templates routes return 404.
