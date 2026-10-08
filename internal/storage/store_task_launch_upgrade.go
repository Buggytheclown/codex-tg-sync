package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// This configuration bridge runs once per imported source. Subsequent startup
// preserves UI edits, including explicit manual launch and custom source names.
func upgradeImportedSourceLaunchTx(ctx context.Context, tx *sql.Tx, id string, in model.TaskSourceImport) error {
	key := "task_board.source_launch_v1." + in.Key
	var value string
	err := tx.QueryRowContext(ctx, `SELECT value FROM daemon_state WHERE key=?`, key).Scan(&value)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	source, err := getTaskTx(ctx, tx, id)
	if err != nil {
		return err
	}
	changed := false
	isSource := source.Polling != nil && source.Polling.Action.Type == model.PollingCreateTask
	if isSource && in.Key == "arcanum_review" && source.Title == "Assigned PRs" {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET title='PR review' WHERE id=?`, id); err != nil {
			return err
		}
		changed = true
	}
	if isSource && in.Key == "arcanum_review" && source.Polling.Action.InitialLaunch == nil && in.Polling.Action.InitialLaunch != nil {
		p := *source.Polling
		p.Action.InitialLaunch = in.Polling.Action.InitialLaunch
		if err = validateInitialLaunch(p); err != nil {
			return err
		}
		raw, err := json.Marshal(p.Action)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE task_polling SET action_json=?,generation=generation+1 WHERE task_id=?`, string(raw), id); err != nil {
			return err
		}
		changed = true
	}
	// Previous requests remain frozen. Backfill author provenance from accepted
	// event facts or an actual legacy PR sender, never from the synthetic sender.
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET source_author=coalesce((SELECT json_extract(e.payload_json,'$.author') FROM task_events e WHERE e.result_task_id=tasks.id AND json_extract(e.payload_json,'$.author')<>'' ORDER BY e.created_at LIMIT 1),(SELECT r.sender FROM external_launch_requests r WHERE r.task_id=tasks.id AND r.source IN ('arcanum_review','arcanum_changes') AND r.sender<>'' ORDER BY r.created_at LIMIT 1)) WHERE source_task_id=? AND (source_author IS NULL OR source_author='')`, id); err != nil {
		return err
	}
	if changed {
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET version=version+1,updated_at=? WHERE id=?`, model.NowString(), id); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO daemon_state(key,value,updated_at) VALUES(?,?,?)`, key, "done", model.NowString())
	return err
}
