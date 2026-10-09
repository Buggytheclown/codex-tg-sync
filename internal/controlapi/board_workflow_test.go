package controlapi

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
	"github.com/mideco-tech/codex-tg/internal/tasks"
)

func TestBoardWorkflowPATCHAtomicSettingsAndLegacyOmission(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	service := tasks.New(store)
	task, err := service.CreateTask(ctx, model.CreateTaskInput{Title: "wait", ThreadID: "chat", Column: model.TaskPollingColumn, Config: model.TaskConfig{ContinuationPrompt: "old"}, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"1","complete_on_merge":false}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler("test", &fakeBackend{}, Options{Board: service, ListenAddress: "127.0.0.1:8099"})
	patch := func(input any, status int) *model.Task {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		w := boardRequest(h, "PATCH", "/v1/tasks/"+task.ID, string(body), "127.0.0.1:8099", "", "application/json")
		if w.Code != status {
			t.Fatalf("PATCH: %d %s, want %d", w.Code, w.Body.String(), status)
		}
		if status != 200 {
			return nil
		}
		var saved model.Task
		if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
			t.Fatal(err)
		}
		return &saved
	}
	p := *task.Polling
	p.Action.Type = model.PollingReturnTask
	input := model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, Config: model.TaskConfig{InitialPrompt: "initial", Execution: task.Config.Execution}, Polling: &p}
	oldVersion, oldGeneration := task.Version, task.Polling.Generation
	task = patch(input, 200)
	if task.Config.ContinuationPrompt != "" || task.Polling.Action.Type != model.PollingReturnTask || task.Version != oldVersion+1 || task.Polling.Generation != oldGeneration+1 {
		t.Fatalf("atomic response: %#v", task)
	}
	patch(input, 409)
	p.Action.Type = model.PollingResumeTask
	input.ExpectedVersion, input.Polling = task.Version, &p
	patch(input, 400)
	unchanged, _ := service.GetTask(ctx, task.ID)
	if !reflect.DeepEqual(task, unchanged) {
		t.Fatal("rejected PATCH changed task")
	}
	input.Config.ContinuationPrompt = "new continuation"
	task = patch(input, 200)
	if accepted, err := store.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, Cursor: json.RawMessage(`{"head":"accepted"}`), CheckedAt: "2026-01-01T10:00:00Z", NextCheckAt: "2026-01-01T11:00:00Z"}, 0); err != nil || !accepted {
		t.Fatalf("polling baseline: %v %v", accepted, err)
	}
	if accepted, err := store.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, CheckedAt: "2026-01-01T10:30:00Z", NextCheckAt: "2026-01-01T11:30:00Z", Error: "retry later"}, 0); err != nil || !accepted {
		t.Fatalf("polling health: %v %v", accepted, err)
	}
	task, _ = service.GetTask(ctx, task.ID)
	for _, null := range []bool{false, true} {
		previous := *task.Polling
		// Send the legacy shape directly, including explicit null as an omitted pointer.
		body := map[string]any{"expected_version": task.Version, "title": task.Title, "description": "legacy edit", "source_url": task.SourceURL, "config": task.Config}
		if null {
			body["polling"] = nil
		}
		task = patch(body, 200)
		previous.Generation++
		if !reflect.DeepEqual(task.Polling, &previous) {
			t.Fatal("omitted polling changed existing settings/runtime beyond generation fence")
		}
	}
}
