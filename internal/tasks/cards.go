package tasks

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) ListTaskCards(ctx context.Context) ([]model.TaskCardDTO, error) {
	return s.store.ListTaskCards(ctx)
}

func (s *Service) ReorderTask(ctx context.Context, id string, in model.MoveTaskInput) (*model.TaskCardDTO, error) {
	if err := s.store.ReorderTask(ctx, id, in); err != nil {
		return nil, err
	}
	return s.store.GetTaskCard(ctx, id)
}
