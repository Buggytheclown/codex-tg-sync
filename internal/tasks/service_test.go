package tasks

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func TestServiceDefaultsAndTypedValidation(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	ctx := context.Background()
	service.SetExecutionDefaults(model.TaskExecutionSettings{CWD: "/workspace", Model: "default-model", ReasoningEffort: "high", ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"})
	service.SetPollingValidator(func(p model.TaskPolling) error {
		if p.Type != "known" {
			return errors.New("unknown source")
		}
		return nil
	})
	_, err = service.CreateTask(ctx, model.CreateTaskInput{Title: "bad", Polling: &model.TaskPolling{Type: "unknown", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed validation error: %v", err)
	}
	task, err := service.AddChat(ctx, "thread", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if task.Column != model.TaskTODO || task.Config.Execution.CWD != "/workspace" || task.Config.Execution.Model != "default-model" || task.Config.Execution.ApprovalPolicy != "on-request" {
		t.Fatalf("defaults %#v", task)
	}
	service.SetExecutionDefaults(model.TaskExecutionSettings{CWD: "/different", Model: "different"})
	again, err := service.AddChat(ctx, "thread", "renamed")
	if err != nil || again.ID != task.ID || again.Config.Execution.Model != "default-model" {
		t.Fatalf("dedup changed snapshot %#v %v", again, err)
	}
}
