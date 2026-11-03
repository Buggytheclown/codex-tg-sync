package storage

import (
	"context"
	"database/sql"
	"errors"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// Only the exact poller-owned run may return a task for result review. A manual
// column move relinquishes that ownership, even if the task later returns to TODO.
func returnCompletedPollingTaskTx(ctx context.Context, tx *sql.Tx, requestID, threadID, turnID, status string, now model.TimeString) error {
	var taskID string
	err := tx.QueryRowContext(ctx, `
	UPDATE tasks SET column='inbox',version=version+1,updated_at=?
	WHERE column='todo' AND thread_id=? AND id=(
		SELECT r.task_id FROM external_launch_requests r
		JOIN task_events origin ON origin.id=r.trigger_event_id
		WHERE r.id=? AND origin.kind='polling_event'
		AND (origin.task_id=r.task_id OR origin.result_task_id=r.task_id)
		AND NOT EXISTS (
			SELECT 1 FROM task_events moved WHERE moved.task_id=r.task_id
			AND moved.kind='moved' AND moved.rowid>origin.rowid
		)
	)
	AND NOT EXISTS (
		SELECT 1 FROM thread_snapshots WHERE thread_id=?
		AND coalesce(last_seen_turn_id,'')<>'' AND last_seen_turn_id<>?
	)
	RETURNING id`, now, threadID, requestID, threadID, turnID).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return addTaskEventTx(ctx, tx, taskID, "polling_run_completed", map[string]string{
		"request_id": requestID, "thread_id": threadID, "turn_id": turnID,
		"status": status, "from": model.TaskTODO, "to": model.TaskInbox,
	})
}
