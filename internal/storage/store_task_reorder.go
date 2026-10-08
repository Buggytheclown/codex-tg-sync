package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// ReorderTask changes ordering only. The column fence is checked in the same
// transaction as the version, so a stale reorder can never cancel a launch.
func (s *Store) ReorderTask(ctx context.Context, id string, in model.MoveTaskInput) error {
	return s.moveTask(ctx, id, in, true)
}
