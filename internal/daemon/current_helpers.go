package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) markTelegramOriginTurn(ctx context.Context, threadID, turnID string) error {
	key := telegramOriginTurnKey(threadID, turnID)
	if key == "" {
		return nil
	}
	return s.store.SetState(ctx, key, model.TurnOriginTelegram)
}

func (s *Service) markTelegramOriginTurnFromTelegram(ctx context.Context, threadID, turnID string, chatID, topicID int64) error {
	err := s.markTelegramOriginTurn(ctx, threadID, turnID)
	s.logLifecycle("telegram_origin_turn_marked", lifecycleFields{
		"chat_key": model.ChatKey(chatID, topicID), "thread_id": threadID, "turn_id": turnID, "error": err,
	})
	return err
}

func (s *Service) isTelegramOriginTurn(ctx context.Context, threadID, turnID string) bool {
	key := telegramOriginTurnKey(threadID, turnID)
	if key == "" {
		return false
	}
	value, err := s.store.GetState(ctx, key)
	return err == nil && strings.TrimSpace(value) == model.TurnOriginTelegram
}

func telegramOriginTurnKey(threadID, turnID string) string {
	threadID, turnID = strings.TrimSpace(threadID), strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return ""
	}
	return "turn_origin.telegram." + threadID + "." + turnID
}

func runTimingValue(snapshot *appserver.ThreadReadSnapshot, now time.Time) (string, bool) {
	if snapshot == nil {
		return "", false
	}
	startedAt := parseTime(model.TimeString(snapshot.LatestTurnStartedAt))
	if startedAt.IsZero() {
		return "", false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if isTerminalStatus(snapshot.LatestTurnStatus) {
		endedAt := parseTime(model.TimeString(snapshot.LatestTurnUpdatedAt))
		if endedAt.IsZero() {
			endedAt = now
		}
		return formatToolDuration(endedAt.Sub(startedAt)), true
	}
	return formatToolDuration(now.Sub(startedAt)), false
}

func formatToolDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	seconds := int(duration.Truncate(time.Second).Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes, seconds := seconds/60, seconds%60
	if minutes < 60 {
		if seconds == 0 {
			return fmt.Sprintf("%dm", minutes)
		}
		return fmt.Sprintf("%dm %02ds", minutes, seconds)
	}
	hours, minutes := minutes/60, minutes%60
	if hours < 48 {
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	}
	days, hours := hours/24, hours%24
	return fmt.Sprintf("%dd %02dh", days, hours)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := cleanPayloadString(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func isTerminalStatus(status string) bool {
	switch strings.TrimSpace(strings.ToLower(status)) {
	case "completed", "interrupted", "failed", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func isThreadNotLoadedError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "thread not loaded")
}

func threadLooksActiveForPolling(thread model.Thread) bool {
	if strings.TrimSpace(thread.ActiveTurnID) != "" {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(thread.Status))
	return status == "active" || strings.HasPrefix(status, "active[") || strings.Contains(status, "waitingon") || strings.Contains(status, "inprogress") || strings.Contains(status, "running")
}
