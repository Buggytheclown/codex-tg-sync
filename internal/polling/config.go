package polling

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/cronpoller"
	"github.com/mideco-tech/codex-tg/internal/model"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	TypeReview        = "pr_review_source"
	TypeChanges       = "pr_changes_source"
	TypePRWait        = "pr_wait"
	TypeCron          = "cron"
	ConditionCommit   = "new_commit"
	ConditionAnswered = "all_answered"
	ConditionResolved = "all_resolved"
	ConditionChanges  = "request_changes"
)

type Config struct {
	Login           string   `json:"login,omitempty"`
	PRID            string   `json:"pr_id,omitempty"`
	Author          string   `json:"author,omitempty"`
	Conditions      []string `json:"conditions,omitempty"`
	IntervalSeconds int      `json:"interval_seconds,omitempty"`
	Cron            string   `json:"cron,omitempty"`
	Timezone        string   `json:"timezone,omitempty"`
	MaxLateness     string   `json:"max_lateness,omitempty"`
	LegacyID        string   `json:"legacy_id,omitempty"`
}

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func DecodeConfig(p model.TaskPolling) (Config, error) {
	var c Config
	decoder := json.NewDecoder(bytes.NewReader(p.Config))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&c); err != nil {
		return c, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return c, errors.New("polling config requires one JSON object")
	}
	if c.IntervalSeconds == 0 {
		c.IntervalSeconds = 60
	}
	if c.IntervalSeconds < 10 || c.IntervalSeconds > 86400 {
		return c, errors.New("interval_seconds must be 10..86400")
	}
	return c, nil
}
func Validate(p model.TaskPolling) error {
	c, err := DecodeConfig(p)
	if err != nil {
		return err
	}
	switch p.Type {
	case TypeReview, TypeChanges:
		if !loginPattern.MatchString(c.Login) {
			return errors.New("valid reviewer login required")
		}
		if p.Mode != model.PollingRepeat || p.Action.Type != model.PollingCreateTask {
			return errors.New("PR source must repeat and create tasks")
		}
	case TypePRWait:
		id, err := strconv.ParseInt(c.PRID, 10, 64)
		if err != nil || id <= 0 {
			return errors.New("positive PR id required")
		}
		if p.Mode != model.PollingOnce || (p.Action.Type != model.PollingResumeTask && p.Action.Type != model.PollingReturnTask) {
			return errors.New("PR wait must return or resume this task once")
		}
		if len(c.Conditions) == 0 {
			return errors.New("select at least one condition")
		}
		for _, condition := range c.Conditions {
			switch condition {
			case ConditionCommit, ConditionResolved, ConditionChanges:
			case ConditionAnswered:
				if !loginPattern.MatchString(c.Login) {
					return errors.New("reviewer login required for replies")
				}
			default:
				return fmt.Errorf("unsupported condition %q", condition)
			}
		}
	case TypeCron:
		if p.Mode != model.PollingRepeat || p.Action.Type != model.PollingCreateTask {
			return errors.New("cron must repeat and create tasks")
		}
		if strings.TrimSpace(c.Timezone) == "" {
			return errors.New("timezone required")
		}
		if _, _, err := cronpoller.DueOccurrence(c.Cron, c.Timezone, c.MaxLateness, time.Now()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown poller type %q", p.Type)
	}
	return nil
}
