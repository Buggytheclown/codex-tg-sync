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

func (s *Service) ListDoneTaskCards(ctx context.Context) ([]model.TaskCardDTO, error) {
	return s.taskBoard.ListDoneTaskCards(ctx)
}

func (s *Service) ListPollingTasks(ctx context.Context) ([]model.Task, error) {
	return s.store.ListPollingTasks(ctx)
}
func (s *Service) ListPRFooterDemand(ctx context.Context) (map[int64]string, error) {
	return s.store.ListPRFooterDemand(ctx)
}
func (s *Service) GetState(ctx context.Context, key string) (string, error) {
	return s.store.GetState(ctx, key)
}
func (s *Service) SetState(ctx context.Context, key, value string) error {
	return s.store.SetState(ctx, key, value)
}
func (s *Service) PRIsMerged(ctx context.Context, id int64) (bool, error) {
	return s.store.PRIsMerged(ctx, id)
}
func (s *Service) PublishPRObservation(ctx context.Context, id int64, lifecycle, common string, started, finished time.Time, success bool) error {
	return s.store.PublishPRObservation(ctx, id, lifecycle, common, started, finished, success)
}

func (s *Service) TaskLinks(ctx context.Context, id, kind string) ([]model.TaskReference, error) {
	return s.store.TaskLinks(ctx, id, kind)
}
func (s *Service) ListRegisteredTaskPollers(ctx context.Context) ([]string, error) {
	return s.store.ListRegisteredTaskPollers(ctx)
}
