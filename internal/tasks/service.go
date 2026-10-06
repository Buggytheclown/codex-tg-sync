// Package tasks is the local task board's shared application boundary.
package tasks

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

var (
	ErrConflict      = storage.ErrTaskConflict
	ErrInvalid       = storage.ErrTaskInvalid
	ErrNotFound      = storage.ErrTaskNotFound
	ErrTemplateInUse = storage.ErrTaskTemplateInUse
)

type Service struct {
	store     *storage.Store
	mu        sync.RWMutex
	validator func(model.TaskPolling) error
}

func New(store *storage.Store) *Service { return &Service{store: store} }

// SetPollingValidator installs source-specific shape validation at composition.
// The function must be pure: no network or storage calls.
func (s *Service) SetPollingValidator(fn func(model.TaskPolling) error) {
	s.mu.Lock()
	s.validator = fn
	s.store.SetTaskPollingValidator(fn)
	s.mu.Unlock()
}
func (s *Service) validate(p *model.TaskPolling) error {
	if p == nil {
		return nil
	}
	s.mu.RLock()
	fn := s.validator
	s.mu.RUnlock()
	if fn != nil {
		if err := fn(*p); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		return nil
	}
	return nil
}
func (s *Service) ListTasks(ctx context.Context) ([]model.Task, error) { return s.store.ListTasks(ctx) }
func (s *Service) GetTask(ctx context.Context, id string) (*model.Task, error) {
	return s.store.GetTask(ctx, id)
}
func (s *Service) CreateTask(ctx context.Context, in model.CreateTaskInput) (*model.Task, error) {
	if err := s.validate(in.Polling); err != nil {
		return nil, err
	}
	return s.store.CreateTask(ctx, in)
}
func (s *Service) UpdateTask(ctx context.Context, id string, in model.UpdateTaskInput) (*model.Task, error) {
	return s.store.UpdateTask(ctx, id, in)
}
func (s *Service) MoveTask(ctx context.Context, id string, in model.MoveTaskInput) (*model.Task, error) {
	if in.Column == model.TaskPollingColumn {
		t, err := s.GetTask(ctx, id)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, ErrNotFound
		}
		if err := s.validate(t.Polling); err != nil {
			return nil, err
		}
	}
	return s.store.MoveTask(ctx, id, in)
}
func (s *Service) SetTaskPolling(ctx context.Context, id string, in model.SetTaskPollingInput) (*model.Task, error) {
	if err := s.validate(&in.Polling); err != nil {
		return nil, err
	}
	return s.store.SetTaskPolling(ctx, id, in)
}
func (s *Service) AddChat(ctx context.Context, threadID, title string) (*model.Task, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, ErrInvalid
	}
	if title == "" {
		title = "Chat " + threadID
	}
	return s.store.CreateTask(ctx, model.CreateTaskInput{Title: title, ThreadID: threadID, Column: model.TaskTODO})
}
func (s *Service) ListTaskTemplates(ctx context.Context) ([]model.TaskTemplate, error) {
	return s.store.ListTaskTemplates(ctx)
}
func (s *Service) CreateTaskTemplate(ctx context.Context, in model.SaveTaskTemplateInput) (*model.TaskTemplate, error) {
	return s.store.CreateTaskTemplate(ctx, in)
}
func (s *Service) UpdateTaskTemplate(ctx context.Context, id string, in model.SaveTaskTemplateInput) (*model.TaskTemplate, error) {
	return s.store.UpdateTaskTemplate(ctx, id, in)
}
func (s *Service) DeleteTaskTemplate(ctx context.Context, id string, v int64) error {
	return s.store.DeleteTaskTemplate(ctx, id, v)
}
func (s *Service) ListTaskEvents(ctx context.Context, id string) ([]model.TaskEvent, error) {
	return s.store.ListTaskEvents(ctx, id)
}

func (s *Service) SetExecutionDefaults(settings model.TaskExecutionSettings) {
	s.store.SetTaskExecutionDefaults(settings)
}
