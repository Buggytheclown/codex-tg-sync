package storage

import (
	"context"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/model"
	"path/filepath"
	"reflect"
	"testing"
)

func TestTaskTicketDefaultsUpgradeReopenAndLegacyUpdate(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task", SourceURL: "https://source.example/item"})
	if err != nil {
		t.Fatal(err)
	}
	if task.TicketURL != "" {
		t.Fatal(task.TicketURL)
	}
	if _, err := s.db.Exec(`ALTER TABLE tasks DROP COLUMN ticket_url`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.GetTask(ctx, task.ID)
	if err != nil || task.TicketURL != "" {
		t.Fatalf("upgrade: %#v %v", task, err)
	}
	if err := s.SetTaskTicket(ctx, task.ID, model.SetTaskTicketInput{ExpectedVersion: task.Version, TicketURL: "https://tracker.example/TASK-123"}); err != nil {
		t.Fatal(err)
	}
	task, _ = s.GetTask(ctx, task.ID)
	task, err = s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, SourceURL: task.SourceURL, Config: task.Config})
	if err != nil || task.TicketURL != "https://tracker.example/TASK-123" {
		t.Fatalf("legacy: %#v %v", task, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	card, err := s.GetTaskCard(ctx, task.ID)
	if err != nil || card.TicketURL != task.TicketURL || card.SourceURL != task.SourceURL {
		t.Fatalf("projection: %#v %v", card, err)
	}
	for _, value := range []string{"javascript:alert(1)", "file:///tmp/ticket", "//tracker.example/TASK-123", "https://user:pass@tracker.example/TASK-123", "https:///TASK-123", "https://tracker.example:bad/TASK-123"} {
		if err := s.SetTaskTicket(ctx, task.ID, model.SetTaskTicketInput{ExpectedVersion: task.Version, TicketURL: value}); !errors.Is(err, ErrTaskInvalid) {
			t.Fatalf("accepted %q: %v", value, err)
		}
	}
	if err := s.SetTaskTicket(ctx, task.ID, model.SetTaskTicketInput{ExpectedVersion: 1, TicketURL: ""}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale: %v", err)
	}
	if err := s.SetTaskTicket(ctx, task.ID, model.SetTaskTicketInput{ExpectedVersion: task.Version, TicketURL: ""}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.TicketURL != "" || got.SourceURL != task.SourceURL {
		t.Fatalf("clear: %#v", got)
	}
}

func TestSetTaskTicketPreservesPollingAndLaunch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task", Position: 75, Column: model.TaskPollingColumn, Config: model.TaskConfig{InitialPrompt: "prompt"}, Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if err != nil {
		t.Fatal(err)
	}
	request := testExternalLaunchRequest("test:priority")
	request.TaskID, request.TriggerEventID = task.ID, "event"
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
	if _, err := s.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE task_polling SET generation=42,cursor_json='{"observed":true}',next_check_at='2030-01-01T00:00:00Z',last_attempt_at='2026-01-01T00:00:00Z',last_success_at='2026-01-01T00:00:00Z',last_error='retained diagnostic' WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetTask(ctx, task.ID)
	if err := s.SetTaskTicket(ctx, task.ID, model.SetTaskTicketInput{ExpectedVersion: task.Version, TicketURL: "https://tracker.example/TASK-123"}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetTask(ctx, task.ID)
	if after.TicketURL != "https://tracker.example/TASK-123" || after.Version != before.Version+1 {
		t.Fatalf("priority: %#v", after)
	}
	after.TicketURL, after.Version, after.UpdatedAt = before.TicketURL, before.Version, before.UpdatedAt
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("metadata command changed task settings: before=%#v after=%#v", before, after)
	}
	events, _ := s.ListTaskEvents(ctx, task.ID)
	if events[len(events)-1].Kind != "ticket_link_changed" {
		t.Fatalf("events: %#v", events)
	}
	for _, in := range []model.SetTaskTicketInput{{ExpectedVersion: 0, TicketURL: "http://tracker.example/TASK-123"}, {ExpectedVersion: task.Version + 1, TicketURL: "javascript:alert(1)"}, {ExpectedVersion: task.Version, TicketURL: "http://tracker.example/TASK-123"}} {
		err := s.SetTaskTicket(ctx, task.ID, in)
		want := ErrTaskInvalid
		if in.ExpectedVersion == task.Version {
			want = ErrTaskConflict
		}
		if !errors.Is(err, want) {
			t.Fatalf("%#v: %v", in, err)
		}
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.TicketURL != "https://tracker.example/TASK-123" || got.Version != task.Version+1 {
		t.Fatalf("rejected command wrote: %#v", got)
	}
	if err := s.SetTaskTicket(ctx, "missing", model.SetTaskTicketInput{ExpectedVersion: 1, TicketURL: ""}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestTaskTicketCreateAndOptionalUpdateValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task", TicketURL: "http://tracker.example/TASK-123"})
	if err != nil {
		t.Fatal(err)
	}
	if task.TicketURL != "http://tracker.example/TASK-123" {
		t.Fatal(task.TicketURL)
	}
	bad := "javascript:alert(1)"
	if _, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "bad", TicketURL: bad}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: "changed", TicketURL: &bad}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatal(err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if !reflect.DeepEqual(got, task) {
		t.Fatal("invalid update mutated task")
	}
	empty := ""
	got, err = s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, TicketURL: &empty})
	if err != nil || got.TicketURL != "" {
		t.Fatalf("clear update: %#v %v", got, err)
	}
}
