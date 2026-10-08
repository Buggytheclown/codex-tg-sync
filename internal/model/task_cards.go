package model

// TaskCardDTO is the board projection. Details and frozen launch payloads belong
// to GetTask, never to board refreshes or invalidation polling.
type TaskCardDTO struct {
	Priority     string             `json:"priority"`
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	Description  string             `json:"description"`
	Column       string             `json:"column"`
	Position     float64            `json:"position"`
	ThreadID     string             `json:"thread_id"`
	SourceURL    string             `json:"source_url"`
	SourceKey    string             `json:"source_key"`
	SourceTaskID string             `json:"source_task_id"`
	SourceAuthor string             `json:"source_author,omitempty"`
	Version      int64              `json:"version"`
	CreatedAt    TimeString         `json:"created_at"`
	UpdatedAt    TimeString         `json:"updated_at"`
	Execution    *TaskCardExecution `json:"execution,omitempty"`
	Polling      *TaskCardPolling   `json:"polling,omitempty"`
	Requests     []TaskCardRequest  `json:"requests"`
}
type TaskCardExecution struct {
	LastSeenTurnStatus   string     `json:"last_seen_turn_status"`
	LastSeenThreadStatus string     `json:"last_seen_thread_status"`
	LastPollAt           TimeString `json:"last_poll_at"`
}
type TaskCardRequest struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}
type TaskCardPollingAction struct {
	Type string `json:"type"`
}
type TaskCardPolling struct {
	Type          string                `json:"type"`
	Mode          string                `json:"mode"`
	Action        TaskCardPollingAction `json:"action"`
	NextCheckAt   TimeString            `json:"next_check_at"`
	LastAttemptAt TimeString            `json:"last_attempt_at"`
	LastSuccessAt TimeString            `json:"last_success_at"`
	LastError     string                `json:"last_error"`
	Problem       string                `json:"problem"`
}
