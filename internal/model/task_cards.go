package model

// TaskCardDTO is the board projection. Details and frozen launch payloads belong
// to GetTask, never to board refreshes or invalidation polling.
type TaskCardDTO struct {
	RelatedCards []TaskReference    `json:"related_cards,omitempty"`
	CardType     string             `json:"card_type"`
	ArcanumPRID  string             `json:"arcanum_pr_id"`
	ParentTaskID string             `json:"parent_task_id"`
	PRStatus     *PRStatus          `json:"pr_status,omitempty"`
	TicketURL    string             `json:"ticket_url"`
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
	DoneAt       TimeString         `json:"done_at"`
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
	Rules         []TaskCardPollingRule `json:"rules,omitempty"`
}

type TaskCardPollingRule struct {
	Type            string     `json:"type"`
	Title           string     `json:"title,omitempty"`
	PRID            string     `json:"pr_id,omitempty"`
	ConditionGroups [][]string `json:"condition_groups,omitempty"`
	NextScheduledAt TimeString `json:"next_scheduled_at,omitempty"`
}

// TaskReference is enough for navigation and linking, without archive payloads.
type TaskReference struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CardType     string `json:"card_type"`
	ArcanumPRID  string `json:"arcanum_pr_id"`
	ParentTaskID string `json:"parent_task_id"`
	Column       string `json:"column"`
	Version      int64  `json:"version"`
	ThreadID     string `json:"thread_id"`
	TicketURL    string `json:"ticket_url"`
}
