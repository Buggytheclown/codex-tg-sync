package daemon

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"time"
)

func (s *Service) ListTaskCards(ctx context.Context) ([]model.TaskCardDTO, error) {
	return s.taskBoard.ListTaskCards(ctx)
}

func (s *Service) SetTaskPriority(ctx context.Context, id string, in model.SetTaskPriorityInput) (*model.TaskCardDTO, error) {
	return s.taskBoard.SetTaskPriority(ctx, id, in)
}

func (s *Service) SetTaskTicket(ctx context.Context, id string, in model.SetTaskTicketInput) (*model.TaskCardDTO, error) {
	return s.taskBoard.SetTaskTicket(ctx, id, in)
}

func (s *Service) SetTaskParent(ctx context.Context, id string, in model.SetTaskParentInput) (*model.TaskCardDTO, error) {
	return s.taskBoard.SetTaskParent(ctx, id, in)
}

func (s *Service) ListDuePRMetadata(ctx context.Context, now time.Time, limit int) ([]string, error) {
	return s.store.ListDuePRMetadata(ctx, now, limit)
}
func (s *Service) RecordPRMetadata(ctx context.Context, id, lifecycle, common string, success bool, now time.Time) error {
	return s.store.RecordPRMetadata(ctx, id, lifecycle, common, success, now)
}
