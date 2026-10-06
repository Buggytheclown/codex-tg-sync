package model

import "encoding/json"

const (
	TaskInbox         = "inbox"
	TaskTODO          = "todo"
	TaskPollingColumn = "polling"
	TaskDone          = "done"
	PollingOnce       = "once"
	PollingRepeat     = "repeat"
	PollingReturnTask = "return_task"
	PollingResumeTask = "resume_task"
	PollingCreateTask = "create_task"
)

type TaskExecutionSettings struct {
	CWD               string `json:"cwd"`
	Model             string `json:"model"`
	ReasoningEffort   string `json:"reasoning_effort"`
	ApprovalPolicy    string `json:"approval_policy"`
	ApprovalsReviewer string `json:"approvals_reviewer"`
	SandboxMode       string `json:"sandbox_mode"`
}
type TaskConfig struct {
	InitialPrompt      string                `json:"initial_prompt"`
	ContinuationPrompt string                `json:"continuation_prompt"`
	Execution          TaskExecutionSettings `json:"execution"`
}
type TaskPollingAction struct {
	Type       string `json:"type"`
	TemplateID string `json:"template_id,omitempty"`
}
type TaskPolling struct {
	TaskID        string            `json:"task_id"`
	Type          string            `json:"type"`
	Mode          string            `json:"mode"`
	Config        json.RawMessage   `json:"config"`
	Action        TaskPollingAction `json:"action"`
	Cursor        json.RawMessage   `json:"cursor"`
	Generation    int64             `json:"generation"`
	NextCheckAt   TimeString        `json:"next_check_at"`
	LastAttemptAt TimeString        `json:"last_attempt_at"`
	LastSuccessAt TimeString        `json:"last_success_at"`
	LastError     string            `json:"last_error"`
}
type Task struct {
	ID              string               `json:"id"`
	Title           string               `json:"title"`
	Description     string               `json:"description"`
	Column          string               `json:"column"`
	Position        float64              `json:"position"`
	ThreadID        string               `json:"thread_id"`
	SourceURL       string               `json:"source_url"`
	SourceKey       string               `json:"source_key"`
	TemplateID      string               `json:"template_id"`
	TemplateVersion int64                `json:"template_version"`
	Config          TaskConfig           `json:"config"`
	Version         int64                `json:"version"`
	CreatedAt       TimeString           `json:"created_at"`
	UpdatedAt       TimeString           `json:"updated_at"`
	Polling         *TaskPolling         `json:"polling,omitempty"`
	Execution       *ThreadSnapshotState `json:"execution,omitempty"`
	Requests        []TaskRequestInfo    `json:"requests"`
}
type TaskRequestInfo struct {
	ID             string     `json:"id"`
	TriggerEventID string     `json:"trigger_event_id"`
	TargetThreadID string     `json:"target_thread_id"`
	ThreadID       string     `json:"thread_id"`
	TurnID         string     `json:"turn_id"`
	Status         string     `json:"status"`
	Prompt         string     `json:"prompt"`
	ErrorType      string     `json:"error_type"`
	ErrorSummary   string     `json:"error_summary"`
	CreatedAt      TimeString `json:"created_at"`
}
type TaskTemplateDefinition struct {
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Config      TaskConfig   `json:"config"`
	Polling     *TaskPolling `json:"polling,omitempty"`
}
type TaskTemplate struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Version    int64                  `json:"version"`
	Definition TaskTemplateDefinition `json:"definition"`
	CreatedAt  TimeString             `json:"created_at"`
	UpdatedAt  TimeString             `json:"updated_at"`
}
type TaskEvent struct {
	ID           string          `json:"id"`
	TaskID       string          `json:"task_id"`
	Kind         string          `json:"kind"`
	EventKey     string          `json:"event_key"`
	ResultTaskID string          `json:"result_task_id"`
	Payload      json.RawMessage `json:"payload"`
	CreatedAt    TimeString      `json:"created_at"`
}
type CreateTaskInput struct {
	TemplateID  string       `json:"template_id,omitempty"`
	Title       string       `json:"title"`
	Description string       `json:"description"`
	Column      string       `json:"column"`
	Position    float64      `json:"position"`
	ThreadID    string       `json:"thread_id"`
	SourceURL   string       `json:"source_url"`
	SourceKey   string       `json:"source_key"`
	Config      TaskConfig   `json:"config"`
	Polling     *TaskPolling `json:"polling,omitempty"`
}
type UpdateTaskInput struct {
	ExpectedVersion int64      `json:"expected_version"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	SourceURL       string     `json:"source_url"`
	Config          TaskConfig `json:"config"`
}
type MoveTaskInput struct {
	ExpectedVersion int64   `json:"expected_version"`
	Column          string  `json:"column"`
	Position        float64 `json:"position"`
}
type SetTaskPollingInput struct {
	ExpectedVersion int64       `json:"expected_version"`
	Polling         TaskPolling `json:"polling"`
}
type SaveTaskTemplateInput struct {
	ExpectedVersion int64                  `json:"expected_version"`
	Name            string                 `json:"name"`
	Definition      TaskTemplateDefinition `json:"definition"`
}
type PollingEvent struct {
	Cursor      json.RawMessage   `json:"cursor,omitempty"`
	Key         string            `json:"key"`
	SourceKey   string            `json:"source_key"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	SourceURL   string            `json:"source_url"`
	Payload     json.RawMessage   `json:"payload"`
	Variables   map[string]string `json:"variables"`
}
type TaskPollingOutcome struct {
	TaskID      string
	Generation  int64
	Cursor      json.RawMessage
	NextCheckAt TimeString
	CheckedAt   TimeString
	Error       string
	Events      []PollingEvent
}

// TaskSourceImport is a once-only bridge from a retired source owner. Keys are
// durable source-cycle/occurrence identities selected by the source adapter.
type TaskSourceImport struct {
	Column       string
	Key          string
	Title        string
	Polling      TaskPolling
	TemplateName string
	Definition   TaskTemplateDefinition
	Legacy       []LegacyTaskImport
}
type LegacyTaskImport struct {
	Variables  map[string]string
	SourceKey  string
	EventKey   string
	RequestIDs []string
	Title      string
	SourceURL  string
	Polling    *TaskPolling
	Cursor     json.RawMessage
}

// Template waiting settings exclude runtime state, even if callers supplied it.
func (d TaskTemplateDefinition) MarshalJSON() ([]byte, error) {
	type settings struct {
		Type   string            `json:"type"`
		Mode   string            `json:"mode"`
		Config json.RawMessage   `json:"config"`
		Action TaskPollingAction `json:"action"`
	}
	var waiting *settings
	if d.Polling != nil {
		waiting = &settings{Type: d.Polling.Type, Mode: d.Polling.Mode, Config: d.Polling.Config, Action: d.Polling.Action}
	}
	return json.Marshal(struct {
		Title       string     `json:"title"`
		Description string     `json:"description"`
		Config      TaskConfig `json:"config"`
		Polling     *settings  `json:"polling,omitempty"`
	}{d.Title, d.Description, d.Config, waiting})
}
