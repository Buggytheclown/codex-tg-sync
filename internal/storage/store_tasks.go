package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/mideco-tech/codex-tg/internal/model"
)

var (
	ErrTaskConflict = errors.New("task version conflict")
	ErrTaskInvalid  = errors.New("invalid task settings")
	ErrTaskNotFound = errors.New("task not found")
)

func (s *Store) initializeTasks(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
 CREATE TABLE IF NOT EXISTS tasks (
 id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', column TEXT NOT NULL CHECK(column IN ('inbox','todo','polling','done')),
 position REAL NOT NULL DEFAULT 0, thread_id TEXT, source_url TEXT, source_key TEXT, source_task_id TEXT,
 task_config_json TEXT NOT NULL, version INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_thread ON tasks(thread_id) WHERE thread_id IS NOT NULL AND thread_id<>'';
 CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_source ON tasks(source_key) WHERE source_key IS NOT NULL AND source_key<>'';
 CREATE TABLE IF NOT EXISTS task_polling (
 task_id TEXT PRIMARY KEY REFERENCES tasks(id), type TEXT NOT NULL, mode TEXT NOT NULL CHECK(mode IN ('once','repeat')),
 config_json TEXT NOT NULL, action_json TEXT NOT NULL, cursor_json TEXT NOT NULL DEFAULT '{}', generation INTEGER NOT NULL,
 next_check_at TEXT NOT NULL DEFAULT '', last_attempt_at TEXT NOT NULL DEFAULT '', last_success_at TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS task_events (
 id TEXT PRIMARY KEY, task_id TEXT NOT NULL REFERENCES tasks(id), kind TEXT NOT NULL, event_key TEXT,
 result_task_id TEXT REFERENCES tasks(id), payload_json TEXT NOT NULL, created_at TEXT NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS idx_task_events_key ON task_events(task_id,event_key) WHERE event_key IS NOT NULL AND event_key<>'';
 CREATE INDEX IF NOT EXISTS idx_tasks_column ON tasks(column,position);
 CREATE INDEX IF NOT EXISTS idx_task_polling_due ON task_polling(next_check_at);
 CREATE INDEX IF NOT EXISTS idx_external_launch_task ON external_launch_requests(task_id,created_at);
 CREATE UNIQUE INDEX IF NOT EXISTS idx_external_launch_task_event ON external_launch_requests(trigger_event_id) WHERE trigger_event_id IS NOT NULL AND trigger_event_id<>'';
 `)
	if err != nil {
		return err
	}
	return s.migrateOwnedSourceDefinitions(ctx)
}

func validTaskColumn(c string) bool {
	return c == model.TaskInbox || c == model.TaskTODO || c == model.TaskPollingColumn || c == model.TaskDone
}
func jsonObject(raw json.RawMessage) bool {
	var value map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != nil
}
func normalizeConfig(c model.TaskConfig) model.TaskConfig {
	if c.Execution.ApprovalPolicy == "" {
		c.Execution.ApprovalPolicy = "on-request"
	}
	if c.Execution.ApprovalsReviewer == "" {
		c.Execution.ApprovalsReviewer = "auto_review"
	}
	if c.Execution.SandboxMode == "" {
		c.Execution.SandboxMode = "workspace-write"
	}
	return c
}
func validatePolling(p model.TaskPolling) error {
	if strings.TrimSpace(p.Type) == "" || (p.Mode != model.PollingOnce && p.Mode != model.PollingRepeat) || !jsonObject(p.Config) {
		return fmt.Errorf("%w: polling type, mode and object config required", ErrTaskInvalid)
	}
	switch p.Action.Type {
	case model.PollingReturnTask, model.PollingResumeTask:
		if p.Action.ChildDefinition != nil {
			return fmt.Errorf("%w: only sources own child definitions", ErrTaskInvalid)
		}
		if p.Mode != model.PollingOnce {
			return fmt.Errorf("%w: working task waiting must be once", ErrTaskInvalid)
		}
	case model.PollingCreateTask:
		if p.Mode != model.PollingRepeat || p.Action.ChildDefinition == nil {
			return fmt.Errorf("%w: sources require repeat and child definition", ErrTaskInvalid)
		}
		if err := validateChildDefinition(*p.Action.ChildDefinition); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unknown polling action", ErrTaskInvalid)
	}
	if len(p.Cursor) > 0 && !jsonObject(p.Cursor) {
		return fmt.Errorf("%w: cursor must be object", ErrTaskInvalid)
	}
	return nil
}

const taskColumns = `id,title,description,column,position,coalesce(thread_id,''),coalesce(source_url,''),coalesce(source_key,''),coalesce(source_task_id,''),task_config_json,version,created_at,updated_at`

func scanTask(row rowScanner) (*model.Task, error) {
	var t model.Task
	var raw string
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Column, &t.Position, &t.ThreadID, &t.SourceURL, &t.SourceKey, &t.SourceTaskID, &raw, &t.Version, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(raw), &t.Config); err != nil {
		return nil, err
	}
	t.Requests = []model.TaskRequestInfo{}
	return &t, nil
}

const pollingColumns = `task_id,type,mode,config_json,action_json,cursor_json,generation,next_check_at,last_attempt_at,last_success_at,last_error`

func scanPolling(row rowScanner) (*model.TaskPolling, error) {
	var p model.TaskPolling
	var cfg, action, cursor string
	err := row.Scan(&p.TaskID, &p.Type, &p.Mode, &cfg, &action, &cursor, &p.Generation, &p.NextCheckAt, &p.LastAttemptAt, &p.LastSuccessAt, &p.LastError)
	if err != nil {
		return nil, err
	}
	p.Config = json.RawMessage(cfg)
	p.Cursor = json.RawMessage(cursor)
	err = json.Unmarshal([]byte(action), &p.Action)
	return &p, err
}
func getTaskTx(ctx context.Context, tx *sql.Tx, id string) (*model.Task, error) {
	t, err := scanTask(tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTaskNotFound
	}
	if err != nil {
		return nil, err
	}
	p, err := scanPolling(tx.QueryRowContext(ctx, `SELECT `+pollingColumns+` FROM task_polling WHERE task_id=?`, id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	t.Polling = p
	return t, nil
}

func (s *Store) GetTask(ctx context.Context, id string) (*model.Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p, err := scanPolling(s.db.QueryRowContext(ctx, `SELECT `+pollingColumns+` FROM task_polling WHERE task_id=?`, id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	t.Polling = p
	if t.ThreadID != "" {
		t.Execution, err = s.GetSnapshot(ctx, t.ThreadID)
		if err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,coalesce(trigger_event_id,''),coalesce(target_thread_id,''),coalesce(thread_id,''),coalesce(turn_id,''),status,prompt,coalesce(error_type,''),coalesce(error_summary,''),created_at FROM external_launch_requests WHERE task_id=? ORDER BY created_at,id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r model.TaskRequestInfo
		if err := rows.Scan(&r.ID, &r.TriggerEventID, &r.TargetThreadID, &r.ThreadID, &r.TurnID, &r.Status, &r.Prompt, &r.ErrorType, &r.ErrorSummary, &r.CreatedAt); err != nil {
			return nil, err
		}
		t.Requests = append(t.Requests, r)
	}
	return t, rows.Err()
}
func (s *Store) ListTasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks ORDER BY column,position,created_at,id`)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := make([]model.Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		if t != nil {
			out = append(out, *t)
		}
	}
	return out, nil
}
func (s *Store) ListTrackedTaskThreadIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT thread_id FROM tasks WHERE thread_id IS NOT NULL AND thread_id<>'' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func insertTaskTx(ctx context.Context, tx *sql.Tx, t model.Task) error {
	raw, err := json.Marshal(t.Config)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tasks(id,title,description,column,position,thread_id,source_url,source_key,source_task_id,task_config_json,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.Title, t.Description, t.Column, t.Position, nullable(t.ThreadID), nullable(t.SourceURL), nullable(t.SourceKey), nullable(t.SourceTaskID), string(raw), t.Version, t.CreatedAt, t.UpdatedAt)
	return err
}
func setPollingTx(ctx context.Context, tx *sql.Tx, p model.TaskPolling) error {
	if len(p.Cursor) == 0 {
		p.Cursor = json.RawMessage(`{}`)
	}
	action, err := json.Marshal(p.Action)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_polling(task_id,type,mode,config_json,action_json,cursor_json,generation,next_check_at,last_attempt_at,last_success_at,last_error) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(task_id) DO UPDATE SET type=excluded.type,mode=excluded.mode,config_json=excluded.config_json,action_json=excluded.action_json,cursor_json=excluded.cursor_json,generation=excluded.generation,next_check_at=excluded.next_check_at,last_attempt_at=excluded.last_attempt_at,last_success_at=excluded.last_success_at,last_error=excluded.last_error`, p.TaskID, p.Type, p.Mode, string(p.Config), string(action), string(p.Cursor), p.Generation, p.NextCheckAt, p.LastAttemptAt, p.LastSuccessAt, p.LastError)
	return err
}
func addTaskEventTx(ctx context.Context, tx *sql.Tx, taskID, kind string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO task_events(id,task_id,kind,payload_json,created_at) VALUES(?,?,?,?,?)`, uuid.NewString(), taskID, kind, string(raw), model.NowString())
	return err
}
func (s *Store) CreateTask(ctx context.Context, in model.CreateTaskInput) (*model.Task, error) {
	if in.Polling != nil {
		p := s.normalizeSourcePolling(*in.Polling)
		in.Polling = &p
	}
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	in.SourceKey = strings.TrimSpace(in.SourceKey)
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: title required", ErrTaskInvalid)
	}
	if in.Column == "" {
		in.Column = model.TaskTODO
	}
	if !validTaskColumn(in.Column) {
		return nil, ErrTaskInvalid
	}
	if in.Polling != nil {
		if err := s.validateResolvedTaskPolling(*in.Polling); err != nil {
			return nil, err
		}
		if err := validatePolling(*in.Polling); err != nil {
			return nil, err
		}
	}
	if in.Column == model.TaskPollingColumn && in.Polling == nil {
		return nil, fmt.Errorf("%w: configure polling first", ErrTaskInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if in.ThreadID != "" || in.SourceKey != "" {
		var id string
		err := findTaskIdentityTx(ctx, tx, in.SourceKey, in.ThreadID, &id)
		if err == nil {
			tx.Rollback()
			return s.GetTask(ctx, id)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	now := model.NowString()
	t := model.Task{ID: uuid.NewString(), Title: in.Title, Description: in.Description, Column: in.Column, Position: in.Position, ThreadID: in.ThreadID, SourceURL: in.SourceURL, SourceKey: in.SourceKey, Config: s.normalizeTaskConfig(in.Config), Version: 1, CreatedAt: now, UpdatedAt: now}
	if t.Column == model.TaskPollingColumn {
		if in.Polling == nil {
			return nil, ErrTaskInvalid
		}
		if err := validateTaskPollingReadiness(&t, *in.Polling); err != nil {
			return nil, err
		}
	}
	if err := insertTaskTx(ctx, tx, t); err != nil {
		return nil, err
	}
	if in.Polling != nil {
		p := *in.Polling
		if err := s.validateResolvedTaskPolling(p); err != nil {
			return nil, err
		}
		p.TaskID = t.ID
		p.Generation = 1
		p.Cursor = json.RawMessage(`{}`)
		p.LastAttemptAt = ""
		p.LastSuccessAt = ""
		p.LastError = ""
		if err := validatePolling(p); err != nil {
			return nil, err
		}
		if err := setPollingTx(ctx, tx, p); err != nil {
			return nil, err
		}
	}
	if err := addTaskEventTx(ctx, tx, t.ID, "created", map[string]string{"column": t.Column}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, t.ID)
}
func checkTaskVersion(t *model.Task, version int64) error {
	if t.Version != version {
		return ErrTaskConflict
	}
	return nil
}
func (s *Store) UpdateTask(ctx context.Context, id string, in model.UpdateTaskInput) (*model.Task, error) {
	if strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: title required", ErrTaskInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t, err := getTaskTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := checkTaskVersion(t, in.ExpectedVersion); err != nil {
		return nil, err
	}
	t.Config = s.normalizeTaskConfig(in.Config)
	if t.Column == model.TaskPollingColumn && t.Polling != nil {
		if err := validateTaskPollingReadiness(t, *t.Polling); err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(t.Config)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET title=?,description=?,source_url=?,task_config_json=?,version=version+1,updated_at=? WHERE id=?`, in.Title, in.Description, nullable(in.SourceURL), string(raw), model.NowString(), id)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_polling SET generation=generation+1 WHERE task_id=?`, id); err != nil {
		return nil, err
	}
	if err := addTaskEventTx(ctx, tx, id, "edited", map[string]any{}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}

// MoveTask must run under the daemon's claim/dispatch gate. Starting without
// result IDs can be cancelled only while that gate excludes dispatch IO.
func (s *Store) MoveTask(ctx context.Context, id string, in model.MoveTaskInput) (*model.Task, error) {
	if !validTaskColumn(in.Column) {
		return nil, ErrTaskInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t, err := getTaskTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := checkTaskVersion(t, in.ExpectedVersion); err != nil {
		return nil, err
	}
	if in.Column == model.TaskPollingColumn {
		if t.Polling == nil {
			return nil, fmt.Errorf("%w: configure polling first", ErrTaskInvalid)
		}
		if err := s.validateResolvedTaskPolling(*t.Polling); err != nil {
			return nil, err
		}
		if err := validatePolling(*t.Polling); err != nil {
			return nil, err
		}
		if err := validateTaskPollingReadiness(t, *t.Polling); err != nil {
			return nil, err
		}
		if err := validatePolling(*t.Polling); err != nil {
			return nil, err
		}
	}
	now := model.NowString()
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET column=?,position=?,version=version+1,updated_at=? WHERE id=?`, in.Column, in.Position, now, id)
	if err != nil {
		return nil, err
	}
	if t.Column != in.Column {
		_, err = tx.ExecContext(ctx, `UPDATE task_polling SET generation=generation+1,next_check_at=CASE WHEN ?='polling' THEN '' ELSE next_check_at END WHERE task_id=?`, in.Column, id)
		if err != nil {
			return nil, err
		}
	}
	if in.Column == model.TaskTODO || in.Column == model.TaskDone {
		if err := cancelUnstartedTaskRequestsTx(ctx, tx, id, now); err != nil {
			return nil, err
		}
	}
	if err := addTaskEventTx(ctx, tx, id, "moved", map[string]string{"from": t.Column, "to": in.Column}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}
func cancelUnstartedTaskRequestsTx(ctx context.Context, tx *sql.Tx, id string, now model.TimeString) error {
	_, err := tx.ExecContext(ctx, `UPDATE callback_routes SET status=? WHERE action LIKE 'external_launch_%' AND request_id IN (SELECT id FROM external_launch_requests WHERE task_id=? AND status IN (?,?,?) AND coalesce(thread_id,'')='' AND coalesce(turn_id,'')='')`, model.CallbackStatusExpired, id, model.ExternalLaunchPendingApproval, model.ExternalLaunchStarting, model.ExternalLaunchFailed)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE external_launch_requests SET status=?,error_type='task_cancelled',error_summary='Task moved to TODO or Done before dispatch.',updated_at=? WHERE task_id=? AND status IN (?,?,?) AND coalesce(thread_id,'')='' AND coalesce(turn_id,'')=''`, model.ExternalLaunchDismissed, now, id, model.ExternalLaunchPendingApproval, model.ExternalLaunchStarting, model.ExternalLaunchFailed)
	return err
}
func (s *Store) SetTaskPolling(ctx context.Context, id string, in model.SetTaskPollingInput) (*model.Task, error) {
	in.Polling = s.normalizeSourcePolling(in.Polling)
	if err := s.validateResolvedTaskPolling(in.Polling); err != nil {
		return nil, err
	}
	if err := validatePolling(in.Polling); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	t, err := getTaskTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := checkTaskVersion(t, in.ExpectedVersion); err != nil {
		return nil, err
	}
	p := in.Polling
	p.TaskID = id
	p.Generation = 1
	p.Cursor = json.RawMessage(`{}`)
	if t.Polling != nil {
		p.Generation = t.Polling.Generation + 1
		if samePollingIdentity(*t.Polling, p) {
			p.Cursor = t.Polling.Cursor
		} else {
			p.Cursor = json.RawMessage(`{}`)
		}
	}
	p.LastAttemptAt = ""
	p.LastSuccessAt = ""
	p.LastError = ""
	p.NextCheckAt = ""
	if t.Column == model.TaskPollingColumn {
		if err := validateTaskPollingReadiness(t, p); err != nil {
			return nil, err
		}
	}
	if err := validatePolling(p); err != nil {
		return nil, err
	}
	if err := setPollingTx(ctx, tx, p); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE tasks SET version=version+1,updated_at=? WHERE id=?`, model.NowString(), id)
	if err != nil {
		return nil, err
	}
	if err := addTaskEventTx(ctx, tx, id, "polling_configured", map[string]any{"generation": p.Generation}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) ListTaskEvents(ctx context.Context, id string) ([]model.TaskEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,kind,coalesce(event_key,''),coalesce(result_task_id,''),payload_json,created_at FROM task_events WHERE task_id=? OR result_task_id=? ORDER BY created_at,id`, id, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TaskEvent{}
	for rows.Next() {
		var e model.TaskEvent
		var raw string
		if err := rows.Scan(&e.ID, &e.TaskID, &e.Kind, &e.EventKey, &e.ResultTaskID, &raw, &e.CreatedAt); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(raw)
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Store) ListDueTaskPolling(ctx context.Context, now time.Time, limit int) ([]model.TaskPolling, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+strings.ReplaceAll(pollingColumns, "task_id,", "p.task_id,")+` FROM task_polling p JOIN tasks t ON t.id=p.task_id WHERE t.column='polling' AND (p.next_check_at='' OR p.next_check_at<=?) ORDER BY p.next_check_at,p.task_id LIMIT ?`, now.UTC().Format(time.RFC3339Nano), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TaskPolling{}
	for rows.Next() {
		p, err := scanPolling(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func samePollingIdentity(a, b model.TaskPolling) bool {
	if a.Type != b.Type {
		return false
	}
	var x, y map[string]json.RawMessage
	if json.Unmarshal(a.Config, &x) != nil || json.Unmarshal(b.Config, &y) != nil {
		return false
	}
	for _, key := range []string{"pr_id", "login", "author", "cron", "timezone"} {
		if string(x[key]) != string(y[key]) {
			return false
		}
	}
	return true
}

// SetTaskPollingValidator installs the pure typed adapter validator used inside
// atomic child creation. Source child definitions may still contain placeholders.
func (s *Store) SetTaskPollingValidator(fn func(model.TaskPolling) error) {
	s.taskValidatorMu.Lock()
	s.taskValidator = fn
	s.taskValidatorMu.Unlock()
}
func (s *Store) validateResolvedTaskPolling(p model.TaskPolling) error {
	s.taskValidatorMu.RLock()
	fn := s.taskValidator
	s.taskValidatorMu.RUnlock()
	if fn != nil {
		if err := fn(p); err != nil {
			return fmt.Errorf("%w: %v", ErrTaskInvalid, err)
		}
	}
	return nil
}

func (s *Store) SetTaskExecutionDefaults(settings model.TaskExecutionSettings) {
	s.taskValidatorMu.Lock()
	s.taskExecutionDefaults = settings
	s.taskValidatorMu.Unlock()
}
func (s *Store) normalizeTaskConfig(c model.TaskConfig) model.TaskConfig {
	s.taskValidatorMu.RLock()
	defaults := s.taskExecutionDefaults
	s.taskValidatorMu.RUnlock()
	if c.Execution.CWD == "" {
		c.Execution.CWD = defaults.CWD
	}
	if c.Execution.Model == "" {
		c.Execution.Model = defaults.Model
	}
	if c.Execution.ReasoningEffort == "" {
		c.Execution.ReasoningEffort = defaults.ReasoningEffort
	}
	if c.Execution.ApprovalPolicy == "" {
		c.Execution.ApprovalPolicy = defaults.ApprovalPolicy
	}
	if c.Execution.ApprovalsReviewer == "" {
		c.Execution.ApprovalsReviewer = defaults.ApprovalsReviewer
	}
	if c.Execution.SandboxMode == "" {
		c.Execution.SandboxMode = defaults.SandboxMode
	}
	return normalizeConfig(c)
}

func validateTaskPollingReadiness(t *model.Task, p model.TaskPolling) error {
	if p.Action.Type == model.PollingResumeTask {
		if strings.TrimSpace(t.ThreadID) == "" || strings.TrimSpace(t.Config.ContinuationPrompt) == "" {
			return fmt.Errorf("%w: continuation waiting requires a linked chat and continuation prompt", ErrTaskInvalid)
		}
	}
	return nil
}

func findTaskIdentityTx(ctx context.Context, tx *sql.Tx, sourceKey, threadID string, id *string) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM tasks WHERE (thread_id=? AND thread_id<>'') OR (source_key=? AND source_key<>'')`, threadID, sourceKey)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		if err := rows.Scan(id); err != nil {
			return err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	if count > 1 {
		return fmt.Errorf("%w: source and chat belong to different tasks", ErrTaskConflict)
	}
	return nil
}
