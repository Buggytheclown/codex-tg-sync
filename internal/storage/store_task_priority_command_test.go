package storage

import (
	"context"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/model"
	"reflect"
	"testing"
)

func TestSetTaskPriorityPreservesPollingAndLaunch(t *testing.T) {
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
	if err := s.SetTaskPriority(ctx, task.ID, model.SetTaskPriorityInput{ExpectedVersion: task.Version, Priority: "high"}); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetTask(ctx, task.ID)
	if after.Priority != "high" || after.Version != before.Version+1 {
		t.Fatalf("priority: %#v", after)
	}
	after.Priority, after.Version, after.UpdatedAt = before.Priority, before.Version, before.UpdatedAt
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("metadata command changed task settings: before=%#v after=%#v", before, after)
	}
	events, _ := s.ListTaskEvents(ctx, task.ID)
	if events[len(events)-1].Kind != "priority_changed" {
		t.Fatalf("events: %#v", events)
	}
	for _, in := range []model.SetTaskPriorityInput{{ExpectedVersion: 0, Priority: "low"}, {ExpectedVersion: task.Version + 1, Priority: "urgent"}, {ExpectedVersion: task.Version, Priority: "low"}} {
		err := s.SetTaskPriority(ctx, task.ID, in)
		want := ErrTaskInvalid
		if in.ExpectedVersion == task.Version {
			want = ErrTaskConflict
		}
		if !errors.Is(err, want) {
			t.Fatalf("%#v: %v", in, err)
		}
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Priority != "high" || got.Version != task.Version+1 {
		t.Fatalf("rejected command wrote: %#v", got)
	}
	if err := s.SetTaskPriority(ctx, "missing", model.SetTaskPriorityInput{ExpectedVersion: 1, Priority: "normal"}); !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("missing: %v", err)
	}
}
