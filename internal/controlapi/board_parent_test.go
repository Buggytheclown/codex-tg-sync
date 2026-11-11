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

func TestParentCommandIsVersionedTypedAndCompact(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := tasks.New(store)
	ctx := context.Background()
	parent, err := service.CreateTask(ctx, model.CreateTaskInput{Title: "Development", CardType: model.CardTask, Column: model.TaskReview})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := service.CreateTask(ctx, model.CreateTaskInput{Title: "PR", CardType: model.CardPR, SourceURL: "42", Config: model.TaskConfig{InitialPrompt: "private frozen prompt"}})
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler("test", &fakeBackend{}, Options{Board: service, ListenAddress: "127.0.0.1:8099"})
	path := "/v1/tasks/" + pr.ID + "/parent"
	body := fmt.Sprintf(`{"expected_version":%d,"parent_task_id":%q}`, pr.Version, parent.ID)
	for _, tc := range []struct {
		origin, contentType string
		status              int
	}{{"http://evil.example:8099", "application/json", 403}, {"", "text/plain", 415}} {
		w := boardRequest(h, "POST", path, body, "127.0.0.1:8099", tc.origin, tc.contentType)
		if w.Code != tc.status {
			t.Fatalf("guard: %d %s", w.Code, w.Body.String())
		}
	}
	w := boardRequest(h, "POST", path, body, "127.0.0.1:8099", "", "application/json")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var card map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	if card["parent_task_id"] != parent.ID || card["config"] != nil {
		t.Fatalf("not compact: %#v", card)
	}
	w = boardRequest(h, "POST", path, body, "127.0.0.1:8099", "", "application/json")
	if w.Code != 409 {
		t.Fatalf("stale: %d", w.Code)
	}
	w = boardRequest(h, "POST", "/v1/tasks/"+parent.ID+"/parent", fmt.Sprintf(`{"expected_version":%d,"parent_task_id":%q}`, parent.Version, pr.ID), "127.0.0.1:8099", "", "application/json")
	if w.Code != 400 {
		t.Fatalf("Task adopted parent: %d", w.Code)
	}
}
