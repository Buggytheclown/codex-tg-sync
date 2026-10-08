package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type pollingConfigReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func taskCompleteOnMerge(ctx context.Context, db pollingConfigReader, t *model.Task) (bool, error) {
	parentType := ""
	parentKey := ""
	if t.SourceTaskID != "" {
		err := db.QueryRowContext(ctx, `SELECT coalesce(source_key,'') FROM tasks WHERE id=?`, t.SourceTaskID).Scan(&parentKey)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
	}
	if parentKey == "source:arcanum_review" {
		parentType = "pr_review_source"
	}
	if parentKey == "source:arcanum_changes" {
		parentType = "pr_changes_source"
	}
	return model.TaskPollingCompleteOnMerge(t.Polling.Config, t.SourceKey, parentType)
}

func projectMergeChoice(p *model.TaskPolling, enabled bool) error {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(p.Config, &cfg); err != nil {
		return err
	}
	if cfg == nil {
		return errors.New("polling config requires JSON object")
	}
	raw, err := json.Marshal(enabled)
	if err != nil {
		return err
	}
	cfg["complete_on_merge"] = raw
	p.Config, err = json.Marshal(cfg)
	return err
}

// Detail reads expose effective legacy choices without changing stored settings
// or task versions. Source defaults belong only to future children.
func projectMergeSettings(ctx context.Context, db pollingConfigReader, t *model.Task) error {
	p := t.Polling
	if p == nil {
		return nil
	}
	if p.Type == "pr_wait" {
		enabled, err := taskCompleteOnMerge(ctx, db, t)
		if err != nil {
			return err
		}
		return projectMergeChoice(p, enabled)
	}
	if p.Action.ChildDefinition != nil && p.Action.ChildDefinition.Polling != nil {
		wait := p.Action.ChildDefinition.Polling
		if wait.Type == "pr_wait" {
			enabled, err := model.TaskPollingCompleteOnMerge(wait.Config, "", p.Type)
			if err != nil {
				return err
			}
			return projectMergeChoice(wait, enabled)
		}
	}
	return nil
}

func freezeMergeChoice(p *model.TaskPolling, sourceKey, sourceType string) error {
	if p.Type != "pr_wait" {
		return nil
	}
	enabled, err := model.TaskPollingCompleteOnMerge(p.Config, sourceKey, sourceType)
	if err != nil {
		return err
	}
	return projectMergeChoice(p, enabled)
}
