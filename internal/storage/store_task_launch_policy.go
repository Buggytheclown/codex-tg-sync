package storage

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func validateInitialLaunch(p model.TaskPolling) error {
	policy := p.Action.InitialLaunch
	if policy == nil {
		return nil
	}
	if p.Action.Type != model.PollingCreateTask {
		return fmt.Errorf("%w: initial launch policy belongs to a source", ErrTaskInvalid)
	}
	switch policy.Mode {
	case "manual", "auto":
	case "trusted_authors":
		if p.Type != "pr_review_source" && p.Type != "pr_changes_source" {
			return fmt.Errorf("%w: trusted authors requires a PR source", ErrTaskInvalid)
		}
		for _, author := range policy.Authors {
			if strings.TrimSpace(author) == "" {
				return fmt.Errorf("%w: author must not be empty", ErrTaskInvalid)
			}
		}
	default:
		return fmt.Errorf("%w: invalid initial launch policy", ErrTaskInvalid)
	}
	return nil
}
func normalizeLaunchAuthor(author string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(author), "@"))
}
func initialLaunchAllowed(policy *model.TaskLaunchPolicy, author string) bool {
	if policy == nil {
		return false
	}
	if policy.Mode == "auto" {
		return true
	}
	if policy.Mode != "trusted_authors" || author == "" {
		return false
	}
	for _, trusted := range policy.Authors {
		if normalizeLaunchAuthor(trusted) == normalizeLaunchAuthor(author) {
			return true
		}
	}
	return false
}

// Prefixing is independent of the configured child title and prompt. Schedules
// use the occurrence time, never acceptance time (including late catch-up).
func sourceChildTitle(source model.Task, childTitle string, event model.PollingEvent) (string, error) {
	title := strings.TrimSpace(childTitle)
	sourceTitle := strings.TrimSpace(source.Title)
	if source.Polling != nil && source.Polling.Type == "cron" {
		var cfg struct {
			Timezone string `json:"timezone"`
			LegacyID string `json:"legacy_id"`
		}
		if err := json.Unmarshal(source.Polling.Config, &cfg); err != nil {
			return "", err
		}
		var facts struct {
			ScheduledAt string `json:"scheduled_at"`
		}
		if err := json.Unmarshal(event.Payload, &facts); err != nil {
			return "", err
		}
		slot, err := time.Parse(time.RFC3339, facts.ScheduledAt)
		if err != nil {
			return "", fmt.Errorf("%w: cron event requires scheduled_at", ErrTaskInvalid)
		}
		loc, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			return "", err
		}
		prefix := sourceTitle + ": " + slot.In(loc).Format("2006-01-02 15:04") + " [" + cfg.Timezone + "]"
		if title == "Scheduled task" || title == "Scheduled task: "+cfg.LegacyID || title == "{{scheduled_at}}" || title == facts.ScheduledAt {
			title = ""
		}
		if title != "" {
			prefix += ": " + title
		}
		return prefix, nil
	}
	return sourceTitle + ": " + title, nil
}
