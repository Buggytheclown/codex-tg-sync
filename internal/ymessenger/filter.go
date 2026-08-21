package ymessenger

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mideco-tech/codex-tg/internal/model"
)

const Source = "yandex_messenger"

type FilterConfig struct {
	RobotLogin      string
	AllowedSenders  []string
	DefaultCWD      string
	TelegramTopicID int64
}

func RequestsFromUpdates(updates []Update, cfg FilterConfig) []model.ExternalLaunchRequest {
	allowed := make(map[string]struct{}, len(cfg.AllowedSenders))
	for _, sender := range cfg.AllowedSenders {
		if normalized := normalizeLogin(sender); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	robotLogin := normalizeLogin(cfg.RobotLogin)
	requests := make([]model.ExternalLaunchRequest, 0, len(updates))
	for _, update := range updates {
		sender := normalizeLogin(update.From.Login)
		text := strings.TrimSpace(update.Text)
		if update.UpdateID < 0 || update.MessageID == 0 || strings.TrimSpace(update.Chat.ID) == "" || sender == "" || update.From.Robot || text == "" {
			continue
		}
		if _, ok := allowed[sender]; !ok || !mentionsLogin(update.MentionedUsers, robotLogin) {
			continue
		}
		externalID := fmt.Sprintf("%s:%d", strings.TrimSpace(update.Chat.ID), update.MessageID)
		now := model.NowString()
		requests = append(requests, model.ExternalLaunchRequest{
			ID: Source + ":" + externalID, Source: Source, ExternalID: externalID, Sender: sender,
			Title: "Yandex Messenger request from " + sender, SafePreview: preview(text, 240), Prompt: text,
			CWD: strings.TrimSpace(cfg.DefaultCWD), Status: model.ExternalLaunchPendingApproval,
			TelegramTopicID: cfg.TelegramTopicID, CreatedAt: now, UpdatedAt: now,
		})
	}
	return requests
}

func normalizeLogin(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(value, "@")))
}

func mentionsLogin(users []User, login string) bool {
	if login == "" {
		return false
	}
	for _, user := range users {
		if normalizeLogin(user.Login) == login {
			return true
		}
	}
	return false
}

func preview(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
