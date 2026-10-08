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

func TestBoardPriorityCommandReturnsCompactCardAndGuards(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := tasks.New(store)
	task, err := service.CreateTask(context.Background(), model.CreateTaskInput{Title: "task", Config: model.TaskConfig{InitialPrompt: "private frozen prompt"}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler("test", &fakeBackend{}, Options{Board: service, ListenAddress: "127.0.0.1:8099"})
	path := "/v1/tasks/" + task.ID + "/priority"
	body := fmt.Sprintf(`{"expected_version":%d,"priority":"low"}`, task.Version)
	for _, tc := range []struct {
		method, host, origin, ct, body string
		status                         int
	}{
		{"POST", "evil.example:8099", "", "application/json", body, 403},
		{"POST", "127.0.0.1:8099", "http://evil.example:8099", "application/json", body, 403},
		{"POST", "127.0.0.1:8099", "", "text/plain", body, 415},
		{"POST", "127.0.0.1:8099", "", "application/json", `{"expected_version":1,"priority":"low","config":{}}`, 400},
		{"GET", "127.0.0.1:8099", "", "application/json", body, 405},
	} {
		w := boardRequest(h, tc.method, path, tc.body, tc.host, tc.origin, tc.ct)
		if w.Code != tc.status {
			t.Fatalf("guard: %d %s", w.Code, w.Body.String())
		}
	}
	before, _ := service.GetTask(context.Background(), task.ID)
	if before.Version != task.Version {
		t.Fatal("guard wrote")
	}
	w := boardRequest(h, "POST", path, body, "127.0.0.1:8099", "", "application/json")
	if w.Code != 200 {
		t.Fatalf("priority: %d %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["config"]; ok {
		t.Fatal("priority returned full task settings")
	}
	var card model.TaskCardDTO
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.Priority != "low" || card.Version != task.Version+1 {
		t.Fatalf("card: %#v", card)
	}
	w = boardRequest(h, "POST", path, body, "127.0.0.1:8099", "", "application/json")
	if w.Code != 409 {
		t.Fatalf("stale: %d", w.Code)
	}
	w = boardRequest(h, "POST", path, `{"expected_version":2,"priority":"urgent"}`, "127.0.0.1:8099", "", "application/json")
	if w.Code != 400 {
		t.Fatalf("invalid: %d", w.Code)
	}
}

func TestBoardTicketCommandReturnsCompactCardAndGuards(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := tasks.New(store)
	task, err := service.CreateTask(context.Background(), model.CreateTaskInput{Title: "task", Config: model.TaskConfig{InitialPrompt: "private frozen prompt"}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler("test", &fakeBackend{}, Options{Board: service, ListenAddress: "127.0.0.1:8099"})
	path := "/v1/tasks/" + task.ID + "/ticket"
	body := fmt.Sprintf(`{"expected_version":%d,"ticket_url":"https://tracker.example/TASK-123"}`, task.Version)
	for _, tc := range []struct {
		method, host, origin, ct, body string
		status                         int
	}{
		{"POST", "evil.example:8099", "", "application/json", body, 403},
		{"POST", "127.0.0.1:8099", "http://evil.example:8099", "application/json", body, 403},
		{"POST", "127.0.0.1:8099", "", "text/plain", body, 415},
		{"POST", "127.0.0.1:8099", "", "application/json", `{"expected_version":1}`, 400},
		{"POST", "127.0.0.1:8099", "", "application/json", `{"expected_version":1,"ticket_url":null}`, 400},
		{"POST", "127.0.0.1:8099", "", "application/json", `{"expected_version":1,"ticket_url":"https://tracker.example/TASK-123","config":{}}`, 400},
		{"GET", "127.0.0.1:8099", "", "application/json", body, 405},
	} {
		w := boardRequest(h, tc.method, path, tc.body, tc.host, tc.origin, tc.ct)
		if w.Code != tc.status {
			t.Fatalf("guard: %d %s", w.Code, w.Body.String())
		}
	}
	before, _ := service.GetTask(context.Background(), task.ID)
	if before.Version != task.Version {
		t.Fatal("guard wrote")
	}
	w := boardRequest(h, "POST", path, body, "127.0.0.1:8099", "", "application/json")
	if w.Code != 200 {
		t.Fatalf("priority: %d %s", w.Code, w.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["config"]; ok {
		t.Fatal("priority returned full task settings")
	}
	var card model.TaskCardDTO
	if err := json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card.TicketURL != "https://tracker.example/TASK-123" || card.Version != task.Version+1 {
		t.Fatalf("card: %#v", card)
	}
	w = boardRequest(h, "POST", path, body, "127.0.0.1:8099", "", "application/json")
	if w.Code != 409 {
		t.Fatalf("stale: %d", w.Code)
	}
	w = boardRequest(h, "POST", path, `{"expected_version":2,"ticket_url":"javascript:alert(1)"}`, "127.0.0.1:8099", "", "application/json")
	if w.Code != 400 {
		t.Fatalf("invalid: %d", w.Code)
	}
}
