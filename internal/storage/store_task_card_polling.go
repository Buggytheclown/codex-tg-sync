package storage

import (
	"encoding/json"
	"time"

	"github.com/mideco-tech/codex-tg/internal/cronpoller"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// Only trigger metadata belongs on a card, without the rule action or prompt.
func cardPollingRules(p model.TaskPolling, sourceKey, parentKey string, now time.Time, active bool) []model.TaskCardPollingRule {
	if p.Type == model.PollingRules {
		var cfg model.PollingRulesConfig
		if json.Unmarshal(p.Config, &cfg) != nil {
			return nil
		}
		rules := make([]model.TaskCardPollingRule, 0, len(cfg.Rules))
		for _, rule := range cfg.Rules {
			rules = append(rules, cardPollingRule(rule.Polling(p), rule.Title, now, active))
		}
		return rules
	}
	rule := cardPollingRule(p, "", now, active)
	rules := []model.TaskCardPollingRule{rule}
	if p.Type == "pr_wait" {
		merged, _ := model.TaskPollingCompleteOnMerge(p.Config, sourceKey, mergeParentType(parentKey))
		if merged {
			rules = append(rules, model.TaskCardPollingRule{Type: p.Type, PRID: rule.PRID, ConditionGroups: [][]string{{"pr_merged"}}})
		}
	}
	return rules
}

func cardPollingRule(p model.TaskPolling, title string, now time.Time, active bool) model.TaskCardPollingRule {
	rule := model.TaskCardPollingRule{Type: p.Type, Title: title}
	var cfg struct {
		PRID            string     `json:"pr_id"`
		ConditionGroups [][]string `json:"condition_groups"`
		Conditions      []string   `json:"conditions"`
		Cron            string     `json:"cron"`
		Timezone        string     `json:"timezone"`
		MaxLateness     string     `json:"max_lateness"`
	}
	if json.Unmarshal(p.Config, &cfg) != nil {
		return rule
	}
	rule.PRID = cfg.PRID
	rule.ConditionGroups = cfg.ConditionGroups
	if len(rule.ConditionGroups) == 0 {
		for _, condition := range cfg.Conditions {
			rule.ConditionGroups = append(rule.ConditionGroups, []string{condition})
		}
	}
	if p.Type == "cron" && active {
		slot, err := cronpoller.NextOccurrence(cfg.Cron, cfg.Timezone, cfg.MaxLateness, now)
		if err == nil && !slot.IsZero() {
			rule.NextScheduledAt = model.TimeString(slot.UTC().Format(time.RFC3339))
		}
	}
	return rule
}
