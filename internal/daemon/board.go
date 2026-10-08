package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tasks"
)

func (s *Service) ListTasks(ctx context.Context) ([]model.Task, error) {
	return s.taskBoard.ListTasks(ctx)
}
func (s *Service) GetTask(ctx context.Context, id string) (*model.Task, error) {
	return s.taskBoard.GetTask(ctx, id)
}
func (s *Service) CreateTask(ctx context.Context, in model.CreateTaskInput) (*model.Task, error) {
	if strings.TrimSpace(in.ThreadID) != "" {
		return nil, tasks.ErrInvalid
	}
	task, err := s.taskBoard.CreateTask(ctx, in)
	if err == nil {
		s.wakeSyncReconcile()
	}
	return task, err
}
func (s *Service) UpdateTask(ctx context.Context, id string, in model.UpdateTaskInput) (*model.Task, error) {
	return s.taskBoard.UpdateTask(ctx, id, in)
}

// ReorderTask never enters the launch or Sync gates and cannot change a column.
func (s *Service) ReorderTask(ctx context.Context, id string, in model.MoveTaskInput) (*model.TaskCardDTO, error) {
	if err := s.store.ReorderTask(ctx, id, in); err != nil {
		return nil, err
	}
	return s.store.GetTaskCard(ctx, id)
}

func (s *Service) MoveTask(ctx context.Context, id string, in model.MoveTaskInput) (*model.Task, error) {
	current, err := s.store.GetTaskCard(ctx, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, tasks.ErrNotFound
	}
	if current.Column == in.Column {
		if err := s.store.ReorderTask(ctx, id, in); err != nil {
			return nil, err
		}
		return s.taskBoard.GetTask(ctx, id)
	}
	// Column transitions cancel only definitely unstarted work, serialized with
	// claim, reset and dispatch; they do not wait for unrelated background Sync.
	s.taskLaunchMu.Lock()
	defer s.taskLaunchMu.Unlock()
	task, err := s.taskBoard.MoveTask(ctx, id, in)
	if err == nil {
		s.wakeExternalLaunchRenderer()
		s.wakeSyncReconcile()
	}
	return task, err
}
func (s *Service) SetTaskPolling(ctx context.Context, id string, in model.SetTaskPollingInput) (*model.Task, error) {
	return s.taskBoard.SetTaskPolling(ctx, id, in)
}
func (s *Service) AddChat(ctx context.Context, threadID, title string) (*model.Task, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, tasks.ErrInvalid
	}
	existing, err := s.taskBoard.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range existing {
		if task.ThreadID == threadID {
			return &task, nil
		}
	}
	poll, err := s.controlReadSession(ctx)
	if err != nil {
		return nil, err
	}
	current, err := readAuthoritativeSyncSnapshot(ctx, poll, threadID)
	if err != nil {
		return nil, err
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if strings.TrimSpace(title) == "" {
		title = current.Thread.Title
	}
	task, err := s.taskBoard.AddChat(ctx, threadID, title)
	if err == nil {
		s.persistSyncSnapshot(ctx, model.SyncTopic{ThreadID: threadID}, current)
		s.wakeSyncReconcile()
	}
	return task, err
}

func (s *Service) ListTaskEvents(ctx context.Context, id string) ([]model.TaskEvent, error) {
	return s.taskBoard.ListTaskEvents(ctx, id)
}

func (s *Service) SetTaskPollingValidator(validate func(model.TaskPolling) error) {
	s.taskBoard.SetPollingValidator(validate)
}

func (s *Service) ListDueTaskPolling(ctx context.Context, now time.Time, limit int) ([]model.TaskPolling, error) {
	return s.store.ListDueTaskPolling(ctx, now, limit)
}
func (s *Service) AcceptTaskPollingOutcome(ctx context.Context, out model.TaskPollingOutcome, topicID int64) (bool, error) {
	s.taskLaunchMu.Lock()
	defer s.taskLaunchMu.Unlock()
	out.Error = sanitizeDiagnosticString(out.Error)
	accepted, err := s.store.AcceptTaskPollingOutcome(ctx, out, topicID)
	if err == nil && accepted {
		s.wakeExternalLaunchRenderer()
		s.wakeSyncReconcile()
	}
	return accepted, err
}
func (s *Service) BootstrapTaskSource(ctx context.Context, in model.TaskSourceImport) (*model.Task, error) {
	return s.store.BootstrapTaskSource(ctx, in)
}
func (s *Service) ListLegacyLaunchRequests(ctx context.Context) ([]model.ExternalLaunchRequest, error) {
	return s.store.ListExternalLaunchRequestsByStatus(ctx, nil, 100000)
}

func (s *Service) TaskBoardMigrationDone(ctx context.Context, key string) (bool, error) {
	value, err := s.store.GetState(ctx, "task_board.migration."+key)
	return value == "done", err
}
func (s *Service) CompleteTaskBoardMigration(ctx context.Context, key string) error {
	return s.store.SetState(ctx, "task_board.migration."+key, "done")
}

// LinkTaskChat observes authority passively before entering the launch gate.
func (s *Service) LinkTaskChat(ctx context.Context, id string, in model.LinkTaskChatInput) (*model.Task, error) {
	in.ThreadID = strings.TrimSpace(in.ThreadID)
	if in.ThreadID == "" || in.ExpectedVersion <= 0 {
		return nil, tasks.ErrInvalid
	}
	currentTask, err := s.taskBoard.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if currentTask == nil {
		return nil, tasks.ErrNotFound
	}
	if currentTask.Version != in.ExpectedVersion {
		return nil, tasks.ErrConflict
	}
	if currentTask.Polling != nil && currentTask.Polling.Action.Type == model.PollingCreateTask {
		return nil, tasks.ErrInvalid
	}
	if currentTask.ThreadID != "" {
		s.taskLaunchMu.Lock()
		defer s.taskLaunchMu.Unlock()
		return s.store.LinkTaskChat(ctx, id, in)
	}
	poll, err := s.controlReadSession(ctx)
	if err != nil {
		return nil, err
	}
	current, err := readAuthoritativeSyncSnapshot(ctx, poll, in.ThreadID)
	if err != nil {
		return nil, err
	}
	s.taskLaunchMu.Lock()
	defer s.taskLaunchMu.Unlock()
	task, err := s.store.LinkTaskChat(ctx, id, in)
	if err == nil {
		s.persistSyncSnapshot(ctx, model.SyncTopic{ThreadID: in.ThreadID}, current)
		s.wakeSyncReconcile()
		return s.taskBoard.GetTask(ctx, id)
	}
	return task, err
}
