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
	CompleteOnMerge *bool    `json:"complete_on_merge,omitempty"`
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
	if raw := bytes.TrimSpace(p.Config); len(raw) == 0 || raw[0] != '{' {
		return c, errors.New("polling config requires JSON object")
	}
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
	if p.Action.Type == model.PollingCreateTask {
		return validateSourceChildWait(p, c)
	}
	return nil
}

// Validate child waiting before saving a source. Only PR sources supply the
// PR ID/reviewer/author substitutions used by their default wait settings.
func validateSourceChildWait(source model.TaskPolling, sourceConfig Config) error {
	if source.Action.ChildDefinition == nil || source.Action.ChildDefinition.Polling == nil {
		return nil
	}
	wait := *source.Action.ChildDefinition.Polling
	if wait.Type != TypePRWait {
		return errors.New("child waiting must be a once PR wait")
	}
	c, err := DecodeConfig(wait)
	if err != nil {
		return fmt.Errorf("child waiting: %w", err)
	}
	if source.Type == TypeReview || source.Type == TypeChanges {
		replace := strings.NewReplacer("{{pr_id}}", "1", "{{login}}", sourceConfig.Login, "{{author}}", "pr-author")
		c.PRID = replace.Replace(c.PRID)
		c.Login = replace.Replace(c.Login)
		c.Author = replace.Replace(c.Author)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if bytes.Contains(raw, []byte("{{")) {
		if source.Type == TypeCron {
			return errors.New("cron child waiting needs explicit PR ID, reviewer and author values; remove PR placeholders or disable Repeat work")
		}
		return errors.New("child waiting contains an unsupported source variable; use {{pr_id}}, {{login}} or {{author}}")
	}
	wait.Config = raw
	if err := Validate(wait); err != nil {
		return fmt.Errorf("child waiting: %w", err)
	}
	return nil
}

// WithLogin copies polling settings and supplies the configured account at runtime.
// Stored source and child settings cannot select another account.
func WithLogin(p model.TaskPolling, login string) (model.TaskPolling, error) {
	if p.Type == TypeReview || p.Type == TypeChanges || p.Type == TypePRWait {
		cfg, err := DecodeConfig(p)
		if err != nil {
			return p, err
		}
		required := p.Type == TypeReview || p.Type == TypeChanges
		for _, condition := range cfg.Conditions {
			required = required || condition == ConditionAnswered
		}
		if required && !loginPattern.MatchString(login) {
			return p, errors.New("configure CTR_GO_ARCANUM_REVIEW_LOGIN with a valid account")
		}
		cfg.Login = login
		p.Config, err = json.Marshal(cfg)
		if err != nil {
			return p, err
		}
	}
	if p.Action.ChildDefinition != nil {
		child := *p.Action.ChildDefinition
		p.Action.ChildDefinition = &child
		if child.Polling != nil {
			wait, err := WithLogin(*child.Polling, login)
			if err != nil {
				return p, fmt.Errorf("child waiting: %w", err)
			}
			child.Polling = &wait
		}
	}
	return p, nil
}
