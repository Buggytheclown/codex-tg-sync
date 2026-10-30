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
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	TypeReview           = "pr_review_source"
	TypeChanges          = "pr_changes_source"
	TypePRWait           = "pr_wait"
	TypeCron             = "cron"
	ConditionMerged      = "pr_merged"
	ConditionCommit      = "new_commit"
	ConditionAnswered    = "all_answered"
	ConditionResolved    = "all_resolved"
	ConditionChanges     = "request_changes"
	ConditionMergeChecks = "merge_checks_failed"
)

type Config struct {
	ConditionGroups    [][]string `json:"condition_groups,omitempty"`
	CheckMergeFailures bool       `json:"check_merge_failures,omitempty"`
	CompleteOnMerge    *bool      `json:"complete_on_merge,omitempty"`
	Login              string     `json:"login,omitempty"`
	PRID               string     `json:"pr_id,omitempty"`
	Author             string     `json:"author,omitempty"`
	Conditions         []string   `json:"conditions,omitempty"`
	IntervalSeconds    int        `json:"interval_seconds,omitempty"`
	Cron               string     `json:"cron,omitempty"`
	Timezone           string     `json:"timezone,omitempty"`
	MaxLateness        string     `json:"max_lateness,omitempty"`
	LegacyID           string     `json:"legacy_id,omitempty"`
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
	if c.ConditionGroups != nil && len(c.ConditionGroups) == 0 {
		return c, errors.New("select at least one event group")
	}
	if len(c.ConditionGroups) > 0 && len(c.Conditions) > 0 {
		return c, errors.New("use event groups or legacy conditions, not both")
	}
	if slices.Contains(c.Conditions, ConditionMerged) {
		return c, errors.New("PR merged requires an event group")
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
	if p.Type == model.PollingRules {
		return validateRules(p)
	}
	c, err := DecodeConfig(p)
	if err != nil {
		return err
	}
	if c.CheckMergeFailures && p.Type != TypeChanges {
		return errors.New("check_merge_failures is only supported for PR feedback sources")
	}
	if len(c.ConditionGroups) > 0 {
		for _, group := range c.ConditionGroups {
			if len(group) == 0 {
				return errors.New("empty condition group")
			}
			for _, term := range group {
				if p.Type == TypeChanges && term != ConditionChanges && term != ConditionMergeChecks {
					return errors.New("PR feedback supports Request changes and failed merge checks")
				}
			}
		}
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
		if len(conditions(c)) == 0 {
			return errors.New("select at least one condition")
		}
		for _, condition := range conditions(c) {
			switch condition {
			case ConditionCommit, ConditionResolved, ConditionChanges, ConditionMergeChecks, ConditionMerged:
			case ConditionAnswered:
				if !loginPattern.MatchString(c.Login) {
					return errors.New("reviewer login required for replies")
				}
			default:
				return fmt.Errorf("unsupported condition %q", condition)
			}
		}
	case TypeCron:
		if !((p.Mode == model.PollingRepeat && p.Action.Type == model.PollingCreateTask) || (p.Mode == model.PollingOnce && (p.Action.Type == model.PollingReturnTask || p.Action.Type == model.PollingResumeTask))) {
			return errors.New("cron must create tasks repeatedly or move this task once")
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
	if wait.Type == model.PollingRules {
		var cfg model.PollingRulesConfig
		if err := json.Unmarshal(wait.Config, &cfg); err != nil {
			return err
		}
		for i, rule := range cfg.Rules {
			leaf := rule.Polling(wait)
			if leaf.Type == TypePRWait {
				c, err := resolveChildPRConfig(leaf, source.Type, sourceConfig.Login)
				if err != nil {
					return err
				}
				cfg.Rules[i].Config = jsonBytes(c)
			}
		}
		wait.Config = jsonBytes(cfg)
		return Validate(wait)
	}
	if wait.Type == TypeCron {
		return Validate(wait)
	}
	if wait.Type != TypePRWait {
		return errors.New("child waiting must be a once PR wait")
	}
	c, err := resolveChildPRConfig(wait, source.Type, sourceConfig.Login)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	wait.Config = raw
	if err := Validate(wait); err != nil {
		return fmt.Errorf("child waiting: %w", err)
	}
	return nil
}

func resolveChildPRConfig(wait model.TaskPolling, sourceType, login string) (Config, error) {
	c, err := DecodeConfig(wait)
	if err != nil {
		return c, err
	}
	if sourceType == TypeReview || sourceType == TypeChanges {
		replace := strings.NewReplacer("{{pr_id}}", "1", "{{login}}", login, "{{author}}", "pr-author")
		c.PRID, c.Login, c.Author = replace.Replace(c.PRID), replace.Replace(c.Login), replace.Replace(c.Author)
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if bytes.Contains(raw, []byte("{{")) {
		if sourceType == TypeCron {
			return c, errors.New("cron child waiting needs explicit PR ID, reviewer and author values; remove PR placeholders or disable Repeat work")
		}
		return c, errors.New("child waiting contains an unsupported source variable; use {{pr_id}}, {{login}} or {{author}}")
	}
	return c, nil
}

// WithLogin copies polling settings and supplies the configured account at runtime.
// Stored source and child settings cannot select another account.
func WithLogin(p model.TaskPolling, login string) (model.TaskPolling, error) {
	if p.Type == model.PollingRules {
		var cfg model.PollingRulesConfig
		if err := json.Unmarshal(p.Config, &cfg); err != nil {
			return p, err
		}
		for i, r := range cfg.Rules {
			leaf, err := WithLogin(r.Polling(p), login)
			if err != nil {
				return p, err
			}
			cfg.Rules[i].Config = leaf.Config
		}
		p.Config = jsonBytes(cfg)
		return p, nil
	}
	if p.Type == TypeReview || p.Type == TypeChanges || p.Type == TypePRWait {
		cfg, err := DecodeConfig(p)
		if err != nil {
			return p, err
		}
		required := p.Type == TypeReview || p.Type == TypeChanges
		for _, condition := range conditions(cfg) {
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

func conditions(c Config) []string {
	result := append([]string{}, c.Conditions...)
	for _, group := range c.ConditionGroups {
		result = append(result, group...)
	}
	return result
}
func validateRules(p model.TaskPolling) error {
	if p.Mode != model.PollingOnce || p.Action.Type != model.PollingReturnTask {
		return errors.New("rules must be once with a neutral return action")
	}
	var cfg model.PollingRulesConfig
	dec := json.NewDecoder(bytes.NewReader(p.Config))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return err
	}
	if len(cfg.Rules) == 0 || len(cfg.Rules) > 20 {
		return errors.New("configure 1..20 polling rules")
	}
	ids := map[string]bool{}
	for _, r := range cfg.Rules {
		if strings.TrimSpace(r.ID) == "" || ids[r.ID] {
			return errors.New("unique rule IDs required")
		}
		ids[r.ID] = true
		if r.Type != TypePRWait && r.Type != TypeCron {
			return errors.New("rule source must be PR or cron")
		}
		a := r.Action
		if a.ChildDefinition != nil || a.InitialLaunch != nil {
			return errors.New("rules cannot create child tasks")
		}
		if a.Type == model.PollingResumeTask {
			if a.Column != model.TaskTODO {
				return errors.New("continuation must move the task to TODO")
			}
		} else if a.Type != model.PollingReturnTask || (a.Column != model.TaskInbox && a.Column != model.TaskDone) || a.AutoStart {
			return errors.New("move action requires Inbox or Done without launch")
		}
		if err := Validate(r.Polling(p)); err != nil {
			return fmt.Errorf("rule %s: %w", r.ID, err)
		}

	}
	return nil
}
