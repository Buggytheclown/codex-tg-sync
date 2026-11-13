package controlapi

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
	"github.com/mideco-tech/codex-tg/internal/tasks"
	"github.com/mideco-tech/codex-tg/internal/tracker"
	"path/filepath"
	"testing"
)

type chatBoard struct {
	*tasks.Service
	calls int
}

func (b *chatBoard) LookupTracker(ctx context.Context, key string) (tracker.Issue, error) {
	b.calls++
	return tracker.Issue{Key: "TASK-42", Summary: "Title", URL: "https://st.yandex-team.ru/TASK-42"}, nil
}
func (b *chatBoard) CreateBoardChat(ctx context.Context, id string, in model.CreateBoardChatInput) (model.BoardChatResult, error) {
	b.calls++
	return model.BoardChatResult{ThreadID: "new-chat", State: "ready", Fresh: true}, nil
}
func TestBoardChatAndTrackerTrustBoundaries(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	backend := &chatBoard{Service: tasks.New(store)}
	h := NewHandler("test", &fakeBackend{}, Options{Board: backend, ListenAddress: "127.0.0.1:8099"})
	for _, path := range []string{"/v1/tracker/lookup", "/v1/tasks/task/create-chat"} {
		before := backend.calls
		w := boardRequest(h, "POST", path, `{}`, "127.0.0.1:8099", "http://evil.example:8099", "application/json")
		if w.Code != 403 || backend.calls != before {
			t.Fatalf("guard %d", w.Code)
		}
		w = boardRequest(h, "POST", path, `{}`, "127.0.0.1:8099", "", "application/json")
		if w.Code != 200 || backend.calls != before+1 {
			t.Fatalf("command %d %s", w.Code, w.Body.String())
		}
	}
}
