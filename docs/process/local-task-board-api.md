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
- `ListTaskTemplates(ctx) ([]model.TaskTemplate, error)`
- `CreateTaskTemplate(ctx, model.SaveTaskTemplateInput) (*model.TaskTemplate, error)`
- `UpdateTaskTemplate(ctx, id, model.SaveTaskTemplateInput) (*model.TaskTemplate, error)`
- `DeleteTaskTemplate(ctx, id, expectedVersion) error`
- `ListTaskEvents(ctx, id) ([]model.TaskEvent, error)`

Errors `tasks.ErrConflict`, `tasks.ErrInvalid`, `tasks.ErrNotFound`, `tasks.ErrTemplateInUse` support errors.Is. Mutations require expected_version; templates use it only on update/delete. Task update replaces editable title/description/source_url/config; polling settings use their own operation. Column values: inbox, todo, polling, done. Polling requires type, once/repeat mode, object config and action type return_task/resume_task/create_task (create_task also needs template_id). There is no enabled flag; column activates polling. Sources are tasks with create_task action. Execution is an existing ThreadSnapshotState projection, never separately mutated running state. Requests/history are read-only; pending prompt/settings are frozen.

Suggested routes: GET/POST /v1/tasks, GET/PATCH /v1/tasks/{id}, POST /v1/tasks/{id}/move, PUT /v1/tasks/{id}/polling, GET /v1/tasks/{id}/events, POST /v1/tasks/add-chat (thread_id,title), GET/POST /v1/task-templates, PUT/DELETE /v1/task-templates/{id}. Successful task mutations return full Task. Lists return arrays. Errors use status 409 conflict, 400 invalid, 404 not found. Exact envelope/SSE can be selected by API owner.

Daemon must call MoveTask under its launch mutex, the same mutex spanning claim through definite dispatch. Store cancellation touches pending and starting requests with empty result thread/turn only; an already dispatched or ambiguous request is preserved. Store additionally guards claim/retry of cancelled tasks. Polling runner uses `ListDueTaskPolling(ctx, now, limit)` on Store and `AcceptTaskPollingOutcome(ctx, outcome, approvalTopicID)`; generation captures the due row. Acceptance is atomic with template read/copy, cursor, task event and manual frozen request. No external IO inside the transaction.

## Integration details

`CreateTaskInput.template_id` clones the current template atomically; explicit title/description may override the copied fields. Default manual column is TODO. Source event children always start Inbox. Template waiting JSON contains only type/mode/config/action, without runtime cursor/generation/diagnostics. Template title/description formats take precedence over event labels; supported placeholders use `{{key}}`. Replacement is one pass, and JSON substitution changes string values only. Every source/resume request appends compact event facts plus its source URL, even when the prompt has no placeholders.

Composition calls `Service.SetExecutionDefaults(model.TaskExecutionSettings)` with current launch defaults and `Service.SetPollingValidator(func(model.TaskPolling) error)` with the pure typed poller validator. Defaults are copied when task/template settings are saved; later changes do not change pending requests. Nullable external request `*string` permission fields preserve legacy fallback, while board requests require explicit permissions and always use manual approval. The typed validator also validates resolved child waiting settings inside the acceptance transaction; unresolved placeholders remain allowed in reusable templates.

Moving to Polling validates the runnable action. Resume waiting requires a linked thread and a nonempty continuation prompt. Inactive child waiting can be prepared before its initial request launches. Updates to active settings cannot clear required continuation data. Moving out of Polling, replacing waiting settings, or editing task settings advances generation. Rearming preserves the cursor; replacing PR/login/author/schedule identity resets it. Template edits affect future instances only.

`Store.ListDueTaskPolling(ctx, now time.Time, limit int) ([]model.TaskPolling, error)` returns only due tasks in Polling. `Store.AcceptTaskPollingOutcome(ctx, outcome model.TaskPollingOutcome, approvalTopicID int64) (bool, error)` returns false for stale/disabled checks; true includes duplicate-only successful checks. `PollingEvent.Cursor` initializes a created child’s baseline. Error outcomes update attempt/error/scheduling diagnostics only, without advancing cursor or accepting partial events.

`Store.BootstrapTaskSource(ctx, model.TaskSourceImport) (*model.Task, error)` atomically seeds a source/template and links legacy request IDs to imported cards and event barriers. Import Key is a durable once-only marker; Column defaults Polling and may be TODO for paused imports. LegacyTaskImport carries cycle SourceKey, EventKey, RequestIDs, optional Variables, Polling, Cursor, Title and SourceURL. Adapters select event identities matching their new checker. Existing request prompt/policy/settings stay unchanged. Repeated import preserves local edits. `ListTrackedTaskThreadIDs(ctx) ([]string,error)` supplies the shared snapshot producer's board target set.

Daemon MoveTask runs under the same claim/dispatch mutex. It dismisses pending/starting/definitely failed requests with empty result thread/turn, and expires their callbacks. Started and ambiguous requests remain untouched. Claim/Retry reject board requests in TODO/Done. CompleteExternalLaunchRequest and receipt-confirmed ResolveExternalLaunchOutcome bind task.thread_id in the same transaction as the request result, enforce the target thread, and preserve the card column. Durable Sync receipts now retain their confirmed turn_id for exact initial-request recovery.
