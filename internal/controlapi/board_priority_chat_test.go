package controlapi

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
	"github.com/mideco-tech/codex-tg/internal/tasks"
	"path/filepath"
	"testing"
)

func TestBoardPriorityAndChatCommands(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := tasks.New(store)
	h := NewHandler("test", &fakeBackend{}, Options{Board: service, ListenAddress: "127.0.0.1:8099"})
	command := func(method, path, body string, status int) *model.Task {
		t.Helper()
		w := boardRequest(h, method, path, body, "127.0.0.1:8099", "", "application/json")
		if w.Code != status {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body.String())
		}
		if status >= 400 {
			return nil
		}
		var task model.Task
		if err := json.Unmarshal(w.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		return &task
	}
	task := command("POST", "/v1/tasks", `{"title":"task","priority":"high"}`, 201)
	path := "/v1/tasks/" + task.ID
	if task.Priority != "high" {
		t.Fatal(task.Priority)
	}
	task = command("PATCH", path, fmt.Sprintf(`{"expected_version":%d,"title":"edited"}`, task.Version), 200)
	if task.Priority != "high" {
		t.Fatal("legacy PATCH reset priority")
	}
	command("PATCH", path, fmt.Sprintf(`{"expected_version":%d,"title":"bad","priority":"urgent"}`, task.Version), 400)
	task = command("POST", path+"/chat", fmt.Sprintf(`{"expected_version":%d,"thread_id":"chat"}`, task.Version), 200)
	if task.ThreadID != "chat" || task.Priority != "high" {
		t.Fatalf("response: %#v", task)
	}
	command("POST", path+"/chat", `{"expected_version":1,"thread_id":"chat"}`, 409)
	command("POST", path+"/chat", fmt.Sprintf(`{"expected_version":%d,"thread_id":"different"}`, task.Version), 409)
	got, _ := service.GetTask(context.Background(), task.ID)
	if got.ThreadID != "chat" || got.Version != task.Version {
		t.Fatalf("rejected command wrote: %#v", got)
	}
}
