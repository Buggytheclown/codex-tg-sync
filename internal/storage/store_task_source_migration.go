package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// Upgrade only legacy source references. Existing tasks and requests already
// own their snapshots; the historical template table remains inert.
func (s *Store) migrateOwnedSourceDefinitions(ctx context.Context) error {
	if err := s.ensureColumn(ctx, "tasks", "source_task_id", `ALTER TABLE tasks ADD COLUMN source_task_id TEXT`); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT task_id,action_json FROM task_polling WHERE json_extract(action_json,'$.type')='create_task' AND json_extract(action_json,'$.template_id') IS NOT NULL ORDER BY task_id`)
	if err != nil {
		return err
	}
	type legacySource struct{ id, raw string }
	var sources []legacySource
	for rows.Next() {
		var r legacySource
		if err = rows.Scan(&r.id, &r.raw); err != nil {
			rows.Close()
			return err
		}
		sources = append(sources, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, source := range sources {
		var old struct {
			Type            string                `json:"type"`
			TemplateID      string                `json:"template_id"`
			ChildDefinition *model.TaskDefinition `json:"child_definition"`
		}
		if err = json.Unmarshal([]byte(source.raw), &old); err != nil {
			return err
		}
		if old.ChildDefinition == nil {
			var definition string
			if err = tx.QueryRowContext(ctx, `SELECT definition_json FROM task_templates WHERE id=?`, old.TemplateID).Scan(&definition); err != nil {
				return fmt.Errorf("source definition migration: %w", err)
			}
			var d model.TaskDefinition
			if err = json.Unmarshal([]byte(definition), &d); err != nil {
				return err
			}
			if err = validateChildDefinition(d); err != nil {
				return err
			}
			d.Polling = cleanChildPolling(d.Polling)
			old.ChildDefinition = &d
		}
		action, err := json.Marshal(model.TaskPollingAction{Type: old.Type, ChildDefinition: old.ChildDefinition})
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE task_polling SET action_json=? WHERE task_id=?`, string(action), source.id); err != nil {
			return err
		}
	}
	// Existing event linkage is provenance, not an inheritance relationship.
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET source_task_id=(SELECT e.task_id FROM task_events e JOIN task_polling p ON p.task_id=e.task_id WHERE e.result_task_id=tasks.id AND e.task_id<>tasks.id AND json_extract(p.action_json,'$.type')='create_task' ORDER BY e.created_at,e.id LIMIT 1) WHERE source_task_id IS NULL AND EXISTS(SELECT 1 FROM task_events e JOIN task_polling p ON p.task_id=e.task_id WHERE e.result_task_id=tasks.id AND e.task_id<>tasks.id AND json_extract(p.action_json,'$.type')='create_task')`); err != nil {
		return err
	}
	return tx.Commit()
}
