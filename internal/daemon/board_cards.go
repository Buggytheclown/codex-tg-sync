package daemon

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) ListTaskCards(ctx context.Context) ([]model.TaskCardDTO, error) {
	return s.taskBoard.ListTaskCards(ctx)
}

func (s *Service) SetTaskPriority(ctx context.Context, id string, in model.SetTaskPriorityInput) (*model.TaskCardDTO, error) {
	return s.taskBoard.SetTaskPriority(ctx, id, in)
}
