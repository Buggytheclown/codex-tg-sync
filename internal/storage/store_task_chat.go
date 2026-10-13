package storage

import (
	"context"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// LinkTaskChat must be serialized with launch claim/dispatch by its caller.
func (s *Store) LinkTaskChat(ctx context.Context, id string, in model.LinkTaskChatInput) (*model.Task, error) {
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.ExpectedVersion <= 0 {
		return nil, fmt.Errorf("%w: expected_version must be positive", ErrTaskInvalid)
	}
	if in.ThreadID == "" {
		return nil, fmt.Errorf("%w: thread_id required", ErrTaskInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	task, err := getTaskTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := checkTaskVersion(task, in.ExpectedVersion); err != nil {
		return nil, err
	}
	if task.Polling != nil && task.Polling.Action.Type == model.PollingCreateTask {
		return nil, fmt.Errorf("%w: sources cannot link a chat", ErrTaskInvalid)
	}
	if task.ThreadID == in.ThreadID {
		tx.Rollback()
		return s.GetTask(ctx, id)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM tasks WHERE thread_id=? AND id<>?`, in.ThreadID, id).Scan(&count); err != nil {
		return nil, err
	}
	if count != 0 {
		return nil, fmt.Errorf("%w: chat already belongs to another task", ErrTaskConflict)
	}
	// Initial attachment must agree with recovered launch history. A rebind
	// preserves terminal requests on their original chat identities.
	if task.ThreadID == "" {
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM external_launch_requests WHERE task_id=? AND coalesce(thread_id,'')<>'' AND thread_id<>?`, id, in.ThreadID).Scan(&count); err != nil {
			return nil, err
		}
		if count != 0 {
			return nil, fmt.Errorf("%w: task launch history belongs to a different chat", ErrTaskConflict)
		}
	}
	// Even failed/unknown requests may still create or recover a different chat.
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM external_launch_requests WHERE task_id=? AND status NOT IN ('dismissed','rejected_sender','session_completed','session_interrupted','session_failed')`, id).Scan(&count); err != nil {
		return nil, err
	}
	if count != 0 {
		return nil, fmt.Errorf("%w: Handle/dismiss the pending launch request in Telegram before linking an existing chat", ErrTaskConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET thread_id=?,version=version+1,updated_at=? WHERE id=?`, in.ThreadID, model.NowString(), id); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE task_polling SET generation=generation+1 WHERE task_id=?`, id); err != nil {
		return nil, err
	}
	if err := addTaskEventTx(ctx, tx, id, "chat_linked", map[string]string{"thread_id": in.ThreadID, "previous_thread_id": task.ThreadID}); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, id)
}
