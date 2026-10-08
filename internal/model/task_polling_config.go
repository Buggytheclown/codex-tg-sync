package model

import (
	"encoding/json"
	"strings"
)

// CompleteOnMerge preserves explicit choices and applies provenance defaults
// only to legacy waits without a choice. The parent's editable choice is not used.
func TaskPollingCompleteOnMerge(raw json.RawMessage, sourceKey, parentType string) (bool, error) {
	var cfg struct {
		CompleteOnMerge *bool `json:"complete_on_merge"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return false, err
	}
	if cfg.CompleteOnMerge != nil {
		return *cfg.CompleteOnMerge, nil
	}
	return strings.HasPrefix(sourceKey, "arcanum_review:") || strings.HasPrefix(sourceKey, "arcanum_changes:") || parentType == "pr_review_source" || parentType == "pr_changes_source", nil
}
