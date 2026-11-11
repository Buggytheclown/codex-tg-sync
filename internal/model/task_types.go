package model

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const (
	CardTask     = "task"
	CardPR       = "pr"
	CardSchedule = "schedule"
	TaskReview   = "review"
)

type SetTaskParentInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	ParentTaskID    string `json:"parent_task_id"`
}

// PRStatus is passive display data. It grants no right to run task rules.
type PRStatus struct {
	Lifecycle     string     `json:"lifecycle"`
	CommonStatus  string     `json:"common_status"`
	Label         string     `json:"label"`
	LastAttemptAt TimeString `json:"last_attempt_at"`
	LastSuccessAt TimeString `json:"last_success_at"`
	Stale         bool       `json:"stale"`
	Unavailable   bool       `json:"unavailable"`
}

var positivePR = regexp.MustCompile(`^[1-9][0-9]*$`)
var reviewPath = regexp.MustCompile(`^/review/([1-9][0-9]*)(?:/details)?/?$`)

// ParseArcanumPR canonicalizes only recognized IDs/URLs; arbitrary URLs are data.
func ParseArcanumPR(value string) (string, error) {
	value = strings.TrimSpace(value)
	candidate := strings.TrimPrefix(value, "#")
	if !positivePR.MatchString(candidate) {
		u, err := url.Parse(value)
		if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || (u.Host != "a.yandex-team.ru" && u.Host != "arcanum.yandex-team.ru") {
			return "", fmt.Errorf("enter a positive PR ID or an Arcanum review URL")
		}
		match := reviewPath.FindStringSubmatch(u.Path)
		if match == nil {
			return "", fmt.Errorf("invalid Arcanum review URL")
		}
		candidate = match[1]
	}
	if _, err := strconv.ParseInt(candidate, 10, 64); err != nil {
		return "", fmt.Errorf("invalid PR ID")
	}
	return candidate, nil
}

func PRSourceType(kind string) bool { return kind == "pr_review_source" || kind == "pr_changes_source" }
func SourceChildCardType(kind string) string {
	if PRSourceType(kind) {
		return CardPR
	}
	if kind == "cron" {
		return CardSchedule
	}
	return CardTask
}

func (t Task) IsSource() bool {
	return t.Polling != nil && t.Polling.Action.Type == PollingCreateTask || strings.HasPrefix(t.SourceKey, "source:")
}
func (t Task) PRSourceOwned() bool {
	return strings.HasPrefix(t.SourceKey, "arcanum_review:") || strings.HasPrefix(t.SourceKey, "arcanum_changes:")
}
