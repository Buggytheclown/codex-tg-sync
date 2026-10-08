package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// SetTaskPriority changes metadata only; launch and polling ownership stay intact.
func (s *Store) SetTaskPriority(ctx context.Context, id string, in model.SetTaskPriorityInput) error {
	if in.ExpectedVersion <= 0 || !validTaskPriority(in.Priority) {
		return fmt.Errorf("%w: positive expected_version and low, normal or high priority required", ErrTaskInvalid)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM tasks WHERE id=?`, id).Scan(&version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskNotFound
		}
		return err
	}
	if version != in.ExpectedVersion {
		return ErrTaskConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET priority=?,version=version+1,updated_at=? WHERE id=?`, in.Priority, model.NowString(), id); err != nil {
		return err
	}
	if err := addTaskEventTx(ctx, tx, id, "priority_changed", map[string]string{"priority": in.Priority}); err != nil {
		return err
	}
	return tx.Commit()
}
