package controlapi

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
	"github.com/mideco-tech/codex-tg/internal/tasks"
)

func TestBoardNeighborMoveAPI(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := tasks.New(store)
	ctx := context.Background()
	for _, title := range []string{"first", "second", "third"} {
		if _, err := service.CreateTask(ctx, model.CreateTaskInput{Title: title, Column: model.TaskTODO}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := service.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	moving, anchor := all[2], all[0]
	h := NewHandler("test", &fakeBackend{}, Options{Board: service, ListenAddress: "127.0.0.1:8099"})
	move := func(body string, want int) *model.Task {
		t.Helper()
		w := boardRequest(h, "POST", "/v1/tasks/"+moving.ID+"/move", body, "127.0.0.1:8099", "http://127.0.0.1:8099", "application/json")
		if w.Code != want {
			t.Fatalf("move status = %d: %s", w.Code, w.Body.String())
		}
		if want != 200 {
			return nil
		}
		var task model.Task
		if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		return &task
	}
	saved := move(fmt.Sprintf(`{"expected_version":1,"column":"todo","before_task_id":%q}`, anchor.ID), 200)
	if saved.ID != moving.ID || saved.Version != 2 || saved.Position != 1024 {
		t.Fatalf("response = %#v", saved)
	}
	w := boardRequest(h, "GET", "/v1/tasks", "", "127.0.0.1:8099", "", " ")
	var ordered []model.Task
	if err := json.Unmarshal(w.Body.Bytes(), &ordered); err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 3 || ordered[0].ID != moving.ID || ordered[1].ID != anchor.ID || ordered[2].ID != all[1].ID {
		t.Fatalf("readback = %#v", ordered)
	}
	// Neighbor validation and expected-version checks are actionable conflicts.
	move(fmt.Sprintf(`{"expected_version":1,"column":"todo","before_task_id":%q}`, anchor.ID), 409)
	move(`{"expected_version":2,"column":"todo","before_task_id":"missing"}`, 409)
	move(fmt.Sprintf(`{"expected_version":2,"column":"todo","before_task_id":%q}`, moving.ID), 409)
	move(fmt.Sprintf(`{"expected_version":2,"column":"inbox","before_task_id":%q}`, anchor.ID), 409)
	// Polling readiness still rejects a cross-column drop and rolls back normalization.
	move(`{"expected_version":2,"column":"polling","before_task_id":""}`, 400)
	current, _ := service.GetTask(ctx, moving.ID)
	if current.Version != 2 || current.Column != model.TaskTODO {
		t.Fatalf("rejection wrote task: %#v", current)
	}
	// Cross-column insertion into an empty column and append both persist.
	saved = move(`{"expected_version":2,"column":"inbox","before_task_id":""}`, 200)
	if saved.Column != model.TaskInbox {
		t.Fatalf("cross-column = %#v", saved)
	}
	// Old clients can still provide an explicit position without a neighbor.
	saved = move(`{"expected_version":3,"column":"todo","position":12345}`, 200)
	if saved.Position != 12345 {
		t.Fatalf("legacy position = %v", saved.Position)
	}
	// A configured cross-column insertion activates polling and preserves the anchor.
	p := model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
	prepared, err := service.SetTaskPolling(ctx, moving.ID, model.SetTaskPollingInput{ExpectedVersion: saved.Version, Polling: p})
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := service.CreateTask(ctx, model.CreateTaskInput{Title: "waiting", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	saved = move(fmt.Sprintf(`{"expected_version":%d,"column":"polling","before_task_id":%q}`, prepared.Version, waiting.ID), 200)
	if saved.Polling.Generation != prepared.Polling.Generation+1 {
		t.Fatalf("cross-column did not fence generation: %#v", saved.Polling)
	}
	activeGeneration := saved.Polling.Generation
	saved = move(fmt.Sprintf(`{"expected_version":%d,"column":"polling","before_task_id":""}`, saved.Version), 200)
	if saved.Polling.Generation != activeGeneration {
		t.Fatalf("reorder changed generation: %#v", saved.Polling)
	}
	source, err := service.CreateTask(ctx, model.CreateTaskInput{Title: "source", Column: model.TaskPollingColumn, Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "initial"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	move(fmt.Sprintf(`{"expected_version":%d,"column":"polling","before_task_id":%q}`, saved.Version, source.ID), 400)

}
