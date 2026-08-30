package model

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

const (
	ExternalLaunchPendingApproval    = "pending_approval"
	ExternalLaunchStarting           = "starting"
	ExternalLaunchSessionStarted     = "session_started"
	ExternalLaunchSessionCompleted   = "session_completed"
	ExternalLaunchSessionInterrupted = "session_interrupted"
	ExternalLaunchSessionFailed      = "session_failed"
	ExternalLaunchDismissed          = "dismissed"
	ExternalLaunchFailed             = "failed"
	ExternalLaunchOutcomeUnknown     = "outcome_unknown"
	ExternalLaunchRejectedSender     = "rejected_sender"
	ExternalReplyPending             = "pending"
	ExternalReplySending             = "sending"
	ExternalReplySent                = "sent"
	ExternalReplyDead                = "dead"

	AFCStateOff        = "off"
	AFCStateActivating = "activating"
	AFCStateActive     = "active"
	AFCStateDraining   = "draining"

	AFCSecurityValid   = "valid"
	AFCSecurityUnknown = "unknown"

	AFCTopicConnected = "connected"
	AFCTopicCleanup   = "cleanup"

	AFCDraftReady    = "ready"
	AFCDraftStarting = "starting"
	AFCDraftUnknown  = "unknown"
	AFCDraftCleanup  = "cleanup"

	AFCTurnStarting = "starting"
	AFCTurnActive   = "active"
	AFCTurnTerminal = "terminal"
	AFCTurnUnknown  = "unknown"

	AFCReceiptAccepted   = "accepted"
	AFCReceiptDispatched = "dispatched"
	AFCReceiptRejected   = "rejected"
	AFCReceiptUnknown    = "unknown"

	TurnOriginTelegram = "telegram_input"

	PromptSourceServerRequest = "server_request"
	PromptSourceSyntheticPoll = "synthetic_poll"

	DetailItemUser       = "user"
	DetailItemCommentary = "commentary"
	DetailItemPlan       = "plan"
	DetailItemTool       = "tool"
	DetailItemOutput     = "output"
	DetailItemFinal      = "final"

	DeliveryStatusPending    = "pending"
	DeliveryStatusProcessing = "processing"
	DeliveryStatusDelivered  = "delivered"
	DeliveryStatusRetry      = "retry"
	DeliveryStatusDead       = "dead"
	DeliveryStatusSuperseded = "superseded"

	CallbackStatusActive  = "active"
	CallbackStatusExpired = "expired"

	DeliveryModeSendMessage  = "send_message"
	DeliveryModeEditMessage  = "edit_message"
	DeliveryModeSendDocument = "send_document"
)

type AFCState struct {
	SessionID             string
	ChatID                int64
	State                 string
	SecurityState         string
	SnapshotAt            TimeString
	ActivationSummaryJSON string
	CreatedAt             TimeString
	EndedAt               TimeString
}

type AFCTopic struct {
	SessionID             string
	ChatID                int64
	TopicID               int64
	ThreadID              string
	Rank                  int
	Title                 string
	TelegramState         string
	StatusMessageID       int64
	StatusTurnID          string
	LastRenderFP          string
	LastFinalFP           string
	LastUserFP            string
	PendingTelegramUserFP string
	PendingTelegramTurnID string
	ActiveTurnID          string
	ActiveTurnState       string
	WriterGeneration      uint64
	CreatedAt             TimeString
	UpdatedAt             TimeString
}

type AFCTopicDraft struct {
	SessionID       string
	ChatID          int64
	TopicID         int64
	Rank            int
	Title           string
	CWD             string
	ProjectName     string
	DirectoryName   string
	State           string
	SourceMessageID int64
	CreatedAt       TimeString
	UpdatedAt       TimeString
}

type AFCMessageReceipt struct {
	ChatID    int64
	TopicID   int64
	MessageID int64
	SessionID string
	ThreadID  string
	State     string
	CreatedAt TimeString
	UpdatedAt TimeString
}

type TimeString string

type ExternalLaunchRequest struct {
	ID                     string
	Source                 string
	ExternalID             string
	Sender                 string
	Title                  string
	SafePreview            string
	SourceURL              string
	Prompt                 string
	CWD                    string
	Model                  string
	ReasoningEffort        string
	Status                 string
	TelegramTopicID        int64
	TelegramMessageID      int64
	TelegramRenderedStatus string
	ThreadID               string
	TurnID                 string
	AutoStart              bool
	SourceChatID           string
	SourceMessageID        int64
	SourceThreadID         int64
	AckStatus              string
	AckText                string
	AckMessageID           int64
	AckAttempts            int
	AckAvailableAt         TimeString
	AckError               string
	ReplyStatus            string
	ReplyText              string
	ReplyMessageID         int64
	ReplyAttempts          int
	ReplyAvailableAt       TimeString
	ReplyError             string
	ErrorType              string
	ErrorSummary           string
	CreatedAt              TimeString
	UpdatedAt              TimeString
}

type SendOptions struct {
	Silent bool
}

func NowString() TimeString {
	return TimeString(time.Now().UTC().Format(time.RFC3339Nano))
}

type Thread struct {
	ID              string          `json:"id"`
	Title           string          `json:"title"`
	CWD             string          `json:"cwd,omitempty"`
	ProjectName     string          `json:"project_name"`
	DirectoryName   string          `json:"directory_name,omitempty"`
	CreatedAt       int64           `json:"created_at,omitempty"`
	UpdatedAt       int64           `json:"updated_at"`
	Status          string          `json:"status,omitempty"`
	LastPreview     string          `json:"last_preview,omitempty"`
	ActiveTurnID    string          `json:"active_turn_id,omitempty"`
	PreferredModel  string          `json:"preferred_model,omitempty"`
	PermissionsMode string          `json:"permissions_mode,omitempty"`
	Archived        bool            `json:"archived"`
	Raw             json.RawMessage `json:"raw"`
}

func (t Thread) IsInternal() bool {
	return rawThreadLooksInternal(t.Raw)
}

func (t Thread) ShortID() string {
	if len(t.ID) <= 8 {
		return t.ID
	}
	return t.ID[:8]
}

func (t Thread) Label() string {
	title := strings.TrimSpace(t.Title)
	if title == "" {
		title = t.ShortID()
	}
	project := strings.TrimSpace(t.ProjectName)
	if project == "" {
		return title
	}
	return fmt.Sprintf("[%s] %s", project, title)
}

func rawThreadLooksInternal(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false
	}
	return payloadLooksInternal(payload) || payloadLooksInternal(mapValue(payload["thread"]))
}

func payloadLooksInternal(payload map[string]any) bool {
	if len(payload) == 0 {
		return false
	}
	if truthy(payload["ephemeral"]) {
		return true
	}
	source := mapValue(payload["source"])
	return stringFromAny(source["subAgent"]) != ""
}

func mapValue(value any) map[string]any {
	typed, _ := value.(map[string]any)
	return typed
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		normalized := strings.TrimSpace(strings.ToLower(typed))
		return normalized == "true" || normalized == "1" || normalized == "yes"
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return false
	}
}

func stringFromAny(value any) string {
	if typed, ok := value.(string); ok {
		return strings.TrimSpace(typed)
	}
	return ""
}

type ThreadSnapshotState struct {
	ThreadUpdatedAt      int64           `json:"thread_updated_at"`
	LastSeenThreadStatus string          `json:"last_seen_thread_status,omitempty"`
	LastSeenTurnID       string          `json:"last_seen_turn_id,omitempty"`
	LastSeenTurnStatus   string          `json:"last_seen_turn_status,omitempty"`
	LastProgressFP       string          `json:"last_progress_fp,omitempty"`
	LastProgressSentAt   TimeString      `json:"last_progress_sent_at,omitempty"`
	LastFinalFP          string          `json:"last_final_fp,omitempty"`
	LastCompletionFP     string          `json:"last_completion_fp,omitempty"`
	LastApprovalFP       string          `json:"last_approval_fp,omitempty"`
	LastReplyFP          string          `json:"last_reply_fp,omitempty"`
	LastFinalNoticeFP    string          `json:"last_final_notice_fp,omitempty"`
	LastToolDocumentFP   string          `json:"last_tool_document_fp,omitempty"`
	LastRichLiveEventAt  TimeString      `json:"last_rich_live_event_at,omitempty"`
	LastPollAt           TimeString      `json:"last_poll_at,omitempty"`
	NextPollAfter        TimeString      `json:"next_poll_after,omitempty"`
	CompactJSON          json.RawMessage `json:"compact_json,omitempty"`
}

type CallbackRoute struct {
	Token             string
	Action            string
	ThreadID          string
	TurnID            string
	RequestID         string
	TelegramMessageID int64
	Status            string
	ExpiresAt         string
	PayloadJSON       string
	CreatedAt         TimeString
}

type PendingApproval struct {
	RequestID         string
	ThreadID          string
	TurnID            string
	ItemID            string
	PromptKind        string
	Question          string
	Status            string
	TelegramMessageID int64
	PayloadJSON       string
	UpdatedAt         TimeString
}

type PlanPrompt struct {
	PromptID    string   `json:"prompt_id"`
	Source      string   `json:"source"`
	ThreadID    string   `json:"thread_id"`
	TurnID      string   `json:"turn_id,omitempty"`
	ItemID      string   `json:"item_id,omitempty"`
	RequestID   string   `json:"request_id,omitempty"`
	Question    string   `json:"question"`
	Options     []string `json:"options,omitempty"`
	Fingerprint string   `json:"fingerprint"`
	Status      string   `json:"status"`
}

type DeliveryPayload struct {
	Text            string         `json:"text,omitempty"`
	ThreadID        string         `json:"thread_id,omitempty"`
	TurnID          string         `json:"turn_id,omitempty"`
	ItemID          string         `json:"item_id,omitempty"`
	EventID         string         `json:"event_id,omitempty"`
	Buttons         [][]ButtonSpec `json:"buttons,omitempty"`
	HealthKey       string         `json:"health_key,omitempty"`
	HealthEpisodeID string         `json:"health_episode_id,omitempty"`
	HealthState     string         `json:"health_state,omitempty"`
}

type DeliveryQueueItem struct {
	ID          int64
	EventID     string
	ChatKey     string
	ChatID      int64
	TopicID     int64
	ThreadID    string
	Kind        string
	Status      string
	RetryCount  int
	AvailableAt TimeString
	LastError   string
	PayloadJSON string
	CreatedAt   TimeString
	UpdatedAt   TimeString
}

type DeliveryAttempt struct {
	ID        int64
	QueueID   int64
	AttemptNo int
	Status    string
	ErrorText string
	CreatedAt TimeString
}

type ButtonSpec struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

type MessageEntity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
}

type RenderedMessage struct {
	Text     string          `json:"text"`
	Entities []MessageEntity `json:"entities,omitempty"`
}

type DetailItem struct {
	ID              string     `json:"id,omitempty"`
	Kind            string     `json:"kind"`
	StartedAt       TimeString `json:"started_at,omitempty"`
	Phase           string     `json:"phase,omitempty"`
	Text            string     `json:"text,omitempty"`
	Label           string     `json:"label,omitempty"`
	Status          string     `json:"status,omitempty"`
	Output          string     `json:"output,omitempty"`
	FP              string     `json:"fp,omitempty"`
	CommentaryIndex int        `json:"commentary_index,omitempty"`
}

type ObserverEvent struct {
	EventID       string `json:"event_id"`
	Kind          string `json:"kind"`
	ThreadID      string `json:"thread_id"`
	ProjectName   string `json:"project_name"`
	ThreadTitle   string `json:"thread_title"`
	Text          string `json:"text"`
	Status        string `json:"status,omitempty"`
	TurnID        string `json:"turn_id,omitempty"`
	ItemID        string `json:"item_id,omitempty"`
	RequestID     string `json:"request_id,omitempty"`
	NeedsReply    bool   `json:"needs_reply,omitempty"`
	NeedsApproval bool   `json:"needs_approval,omitempty"`
}

type RouteSource string

const (
	RouteSourceExplicit RouteSource = "explicit"
	RouteSourceReply    RouteSource = "reply"
	RouteSourceSteer    RouteSource = "steer"
	RouteSourceBinding  RouteSource = "binding"
	RouteSourceNone     RouteSource = "none"
)

type RouteDecision struct {
	ThreadID  string
	TurnID    string
	RequestID string
	Source    RouteSource
}

func ChatKey(chatID, topicID int64) string {
	return fmt.Sprintf("%d:%d", chatID, topicID)
}

func NormalizePath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	// Codex threads may originate from Windows, macOS, or Linux while tests and
	// CI run on any OS. Normalize both separator styles without using host-OS
	// path semantics.
	normalized := strings.ReplaceAll(trimmed, "\\", "/")
	cleaned := path.Clean(normalized)
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func ProjectNameFromCWD(cwd string) (projectName string, directoryName string) {
	normalized := NormalizePath(cwd)
	if normalized == "" {
		return "Shared/General", ""
	}
	slashed := strings.ToLower(normalized)
	if slashed == "c:/users/you/documents/codex" {
		return "Shared/General", "General"
	}
	dir := path.Base(normalized)
	if dir == "." || dir == "/" || dir == "" {
		return "Shared/General", ""
	}
	return dir, dir
}

func MustJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(payload)
}
