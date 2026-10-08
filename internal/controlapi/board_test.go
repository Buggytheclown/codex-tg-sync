package controlapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tasks"
)

type fakeBoard struct {
	mu        sync.Mutex
	mutations int
	version   int64
	err       error
	card      *model.TaskCardDTO
	fullLists int
}

func (b *fakeBoard) ListTasks(context.Context) ([]model.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fullLists++
	return []model.Task{{ID: "task-1", Title: "Task", Column: "todo", Version: b.version}}, nil
}
func (b *fakeBoard) ListTaskCards(context.Context) ([]model.TaskCardDTO, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.card != nil {
		return []model.TaskCardDTO{*b.card}, nil
	}
	return []model.TaskCardDTO{{ID: "task-1", Title: "Task", Column: "todo", Version: b.version, Requests: []model.TaskCardRequest{}}}, nil
}
func (b *fakeBoard) ReorderTask(context.Context, string, model.MoveTaskInput) (*model.TaskCardDTO, error) {
	t, err := b.change()
	return &model.TaskCardDTO{ID: t.ID, Version: t.Version, Requests: []model.TaskCardRequest{}}, err
}
func (b *fakeBoard) GetTask(context.Context, string) (*model.Task, error) {
	return &model.Task{ID: "task-1"}, nil
}
func (b *fakeBoard) change() (*model.Task, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mutations++
	b.version++
	return &model.Task{ID: "task-1", Version: b.version}, b.err
}
func (b *fakeBoard) CreateTask(context.Context, model.CreateTaskInput) (*model.Task, error) {
	return b.change()
}
func (b *fakeBoard) UpdateTask(context.Context, string, model.UpdateTaskInput) (*model.Task, error) {
	return b.change()
}
func (b *fakeBoard) MoveTask(context.Context, string, model.MoveTaskInput) (*model.Task, error) {
	return b.change()
}
func (b *fakeBoard) SetTaskPolling(context.Context, string, model.SetTaskPollingInput) (*model.Task, error) {
	return b.change()
}
func (b *fakeBoard) AddChat(context.Context, string, string) (*model.Task, error) { return b.change() }
func (b *fakeBoard) ListTaskEvents(context.Context, string) ([]model.TaskEvent, error) {
	return []model.TaskEvent{}, nil
}

func boardRequest(handler http.Handler, method, path, body, host, origin, contentType string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}
func TestBoardGuardsBeforeBackend(t *testing.T) {
	b := &fakeBoard{}
	h := NewHandler("test", &fakeBackend{}, Options{Board: b, ListenAddress: "127.0.0.1:8099"})
	cases := []struct {
		method, host, origin, ct, body string
		status                         int
	}{
		{"GET", "evil.example:8099", "", "", "", 403},
		{"GET", "127.0.0.1:8100", "", "", "", 403},
		{"POST", "127.0.0.1:8099", "null", "application/json", "{}", 403},
		{"POST", "127.0.0.1:8099", "https://evil.example", "application/json", "{}", 403},
		{"POST", "127.0.0.1:8099", "http://127.0.0.1:8099/", "application/json", "{}", 403},
		{"POST", "127.0.0.1:8099", "", "text/plain", "{}", 415},
		{"POST", "127.0.0.1:8099", "", "application/json", "{\"unknown\":true}", 400},
		{"POST", "127.0.0.1:8099", "", "application/json", "{} {}", 400},
		{"POST", "127.0.0.1:8099", "", "application/json", "null", 400},
	}
	for _, c := range cases {
		w := boardRequest(h, c.method, "/v1/tasks", c.body, c.host, c.origin, c.ct)
		if w.Code != c.status {
			t.Errorf("%+v => %d %s", c, w.Code, w.Body.String())
		}
	}
	if b.mutations != 0 {
		t.Fatalf("rejected requests reached backend: %d", b.mutations)
	}
	for _, origin := range []string{"", "http://127.0.0.1:8099"} {
		w := boardRequest(h, "POST", "/v1/tasks", `{"title":"New"}`, "127.0.0.1:8099", origin, "application/json; charset=utf-8")
		if w.Code != 201 {
			t.Fatalf("valid request: %d %s", w.Code, w.Body.String())
		}
	}
}
func TestBoardConflictAndInvalidErrors(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
	}{{tasks.ErrConflict, 409}, {tasks.ErrInvalid, 400}, {tasks.ErrNotFound, 404}} {
		h := NewHandler("test", &fakeBackend{}, Options{Board: &fakeBoard{err: c.err}, ListenAddress: "localhost:8099"})
		w := boardRequest(h, "POST", "/v1/tasks/task-1/move", `{"expected_version":2,"column":"done"}`, "localhost:8099", "", "application/json")
		if w.Code != c.status {
			t.Errorf("%v => %d %s", c.err, w.Code, w.Body.String())
		}
	}
}
func TestBoardDevelopmentOriginIsExplicit(t *testing.T) {
	h := NewHandler("test", &fakeBackend{}, Options{Board: &fakeBoard{}, ListenAddress: "127.0.0.1:8099", DevOrigin: "http://localhost:5173"})
	w := boardRequest(h, "POST", "/v1/tasks", `{"title":"New"}`, "127.0.0.1:8099", "http://localhost:5173", "application/json")
	if w.Code != 201 || w.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("dev origin %d %s", w.Code, w.Body.String())
	}
	w = boardRequest(h, "GET", "/v1/tasks", "", "127.0.0.1:8099", "http://localhost:5174", "")
	if w.Code != 403 {
		t.Fatalf("unexpected dev origin accepted: %d", w.Code)
	}
}
func TestBoardStaticDoesNotMaskMissingAPIOrAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>Board</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHandler("test", &fakeBackend{}, Options{Board: &fakeBoard{}, StaticDir: dir, ListenAddress: "localhost:8099"})
	for _, c := range []struct {
		path   string
		status int
	}{{"/", 200}, {"/tasks/example", 200}, {"/v1/unknown", 404}, {"/v1", 404}, {"/assets/missing.js", 404}, {"/missing.css", 404}} {
		w := boardRequest(h, "GET", c.path, "", "localhost:8099", "", "")
		if w.Code != c.status {
			t.Errorf("%s => %d", c.path, w.Code)
		}
	}
	h = NewHandler("test", &fakeBackend{}, Options{StaticDir: filepath.Join(dir, "absent"), ListenAddress: "localhost:8099"})
	w := boardRequest(h, "GET", "/", "", "localhost:8099", "", "")
	if w.Code != 503 || !strings.Contains(w.Body.String(), "npm run build") {
		t.Fatalf("missing build = %d %s", w.Code, w.Body.String())
	}
}
func TestBoardSSEInvalidatesAfterMutation(t *testing.T) {
	b := &fakeBoard{}
	h := NewHandler("test", &fakeBackend{}, Options{Board: b, EventInterval: 10 * time.Millisecond})
	server := httptest.NewServer(h)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/v1/task-events", nil)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE headers: %d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	readRevision := func() string {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(line, "data: ") {
				var payload struct {
					Revision string `json:"revision"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err != nil {
					t.Fatal(err)
				}
				if payload.Revision != "initial" {
					return payload.Revision
				}
			}
		}
	}
	before := readRevision()
	_, _ = b.change()
	if after := readRevision(); after == before {
		t.Fatalf("mutation did not change revision: %q", after)
	}

	cancel()
}
func TestBoardListsAreArrays(t *testing.T) {
	h := NewHandler("test", &fakeBackend{}, Options{Board: &fakeBoard{}})
	for _, path := range []string{"/v1/tasks", "/v1/task-cards", "/v1/tasks/task-1/events"} {
		w := boardRequest(h, "GET", path, "", "localhost:8099", "", "")
		var array []json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &array); err != nil {
			t.Fatalf("%s: %s %v", path, w.Body.String(), err)
		}
	}
}

func TestTemplateAPIIsRemoved(t *testing.T) {
	h := NewHandler("test", &fakeBackend{}, Options{Board: &fakeBoard{}, ListenAddress: "127.0.0.1:8765"})
	for _, path := range []string{"/v1/task-templates", "/v1/task-templates/old"} {
		r := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765"+path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("retired route %s: %d", path, w.Code)
		}
	}
}

func TestBoardCompactInvalidationIncludesRuntimeAndSettings(t *testing.T) {
	b := &fakeBoard{card: &model.TaskCardDTO{ID: "card", Version: 1, Requests: []model.TaskCardRequest{}, Execution: &model.TaskCardExecution{}, Polling: &model.TaskCardPolling{}}}
	hub := newBoardHub(b, time.Millisecond)
	ch := hub.subscribe()
	defer hub.unsubscribe(ch)
	next := func() string {
		t.Helper()
		select {
		case revision := <-ch:
			return revision
		case <-time.After(time.Second):
			t.Fatal("missing compact invalidation")
			return ""
		}
	}
	if next() != "initial" {
		t.Fatal("missing initial")
	}
	previous := next()
	changes := []func(){
		func() { b.card.Version++ },
		func() { b.card.Execution.LastSeenTurnStatus = "completed" },
		func() { b.card.Execution.LastSeenThreadStatus = "idle" },
		func() { b.card.Execution.LastPollAt = "2026-01-01T00:00:00Z" },
		func() {
			b.card.Requests = append(b.card.Requests, model.TaskCardRequest{ID: "request", Status: "starting"})
		},
		func() { b.card.Requests[0].Status = "completed" },
		func() { b.card.Polling.LastAttemptAt = "2026-01-01T00:00:00Z" },
		func() { b.card.Polling.NextCheckAt = "2026-01-02T00:00:00Z" },
		func() { b.card.Polling.LastSuccessAt = "2026-01-01T00:00:00Z" },
		func() { b.card.Polling.LastError = "failed" },
		func() { b.card.Polling.Problem = "invalid settings" },
	}
	for i, change := range changes {
		b.mu.Lock()
		change()
		b.mu.Unlock()
		revision := next()
		if revision == previous {
			t.Fatalf("change %d ignored", i)
		}
		previous = revision
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fullLists != 0 {
		t.Fatalf("SSE loaded %d full task lists", b.fullLists)
	}
}

func (b *fakeBoard) LinkTaskChat(context.Context, string, model.LinkTaskChatInput) (*model.Task, error) {
	return b.change()
}
