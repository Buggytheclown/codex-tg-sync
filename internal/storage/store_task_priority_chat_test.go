package storage

import (
	"context"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/model"
	"path/filepath"
	"testing"
)

func TestTaskPriorityAndLinkChat(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task"})
	if err != nil {
		t.Fatal(err)
	}
	if task.Priority != "normal" {
		t.Fatalf("default priority: %q", task.Priority)
	}
	high := "high"
	task, err = s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, Priority: &high})
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title})
	if err != nil || task.Priority != high {
		t.Fatalf("preserve priority: %#v %v", task, err)
	}
	invalid := "urgent"
	if _, err := s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, Priority: &invalid}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "bad", Priority: invalid}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatal(err)
	}
	before := task.Version
	task, err = s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: before, ThreadID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if task.ThreadID != "chat" || task.Version != before+1 || task.Priority != high {
		t.Fatalf("link: %#v", task)
	}
	again, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "chat"})
	if err != nil || again.Version != task.Version {
		t.Fatalf("idempotent: %#v %v", again, err)
	}
	other, _ := s.CreateTask(ctx, model.CreateTaskInput{Title: "other"})
	if _, err := s.LinkTaskChat(ctx, other.ID, model.LinkTaskChatInput{ExpectedVersion: other.Version, ThreadID: "chat"}); !errors.Is(err, ErrTaskConflict) {
		t.Fatal(err)
	}
	card, err := s.GetTaskCard(ctx, task.ID)
	if err != nil || card.Priority != high {
		t.Fatalf("card: %#v %v", card, err)
	}
}

func TestTaskPriorityAdditiveUpgradeAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "old"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE tasks DROP COLUMN priority`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.GetTask(ctx, task.ID)
	if err != nil || old.Priority != "normal" || old.Version != task.Version {
		t.Fatalf("upgrade: %#v %v", old, err)
	}
	high := "high"
	old, err = s.UpdateTask(ctx, old.ID, model.UpdateTaskInput{ExpectedVersion: old.Version, Title: old.Title, Priority: &high})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old, err = s.GetTask(ctx, task.ID)
	if err != nil || old.Priority != high {
		t.Fatalf("reopen: %#v %v", old, err)
	}
}

func TestTaskLinkChatPreservesPollingAndFencesOutcomes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "wait", Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if err != nil {
		t.Fatal(err)
	}
	before := task.Polling.Generation
	linked, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if linked.Polling.Generation != before+1 || string(linked.Polling.Config) != string(task.Polling.Config) || string(linked.Polling.Cursor) != string(task.Polling.Cursor) || linked.Column != task.Column {
		t.Fatalf("polling changed: %#v", linked)
	}
	accepted, err := s.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: task.ID, Generation: before, Cursor: []byte(`{}`)}, 0)
	if err != nil || accepted {
		t.Fatalf("stale outcome: %v %v", accepted, err)
	}
	if _, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "chat"}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale link: %v", err)
	}
}

func TestTaskLinkChatRejectsLaunchIdentityAndActiveStates(t *testing.T) {
	for _, status := range []string{model.ExternalLaunchPendingApproval, model.ExternalLaunchStarting, model.ExternalLaunchFailed, model.ExternalLaunchOutcomeUnknown, model.ExternalLaunchSessionStarted, model.ExternalLaunchSessionCompleted} {
		t.Run(status, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task"})
			if err != nil {
				t.Fatal(err)
			}
			request := testExternalLaunchRequest("test:link")
			request.TaskID, request.TriggerEventID = task.ID, "event"
			policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
			request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
			if _, err := s.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{request}); err != nil {
				t.Fatal(err)
			}
			thread := ""
			if status == model.ExternalLaunchSessionCompleted {
				thread = "different"
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET status=?,thread_id=? WHERE id=?`, status, thread, request.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "chat"}); !errors.Is(err, ErrTaskConflict) {
				t.Fatalf("status %s allowed link: %v", status, err)
			}
			got, _ := s.GetTask(ctx, task.ID)
			if got.ThreadID != "" || got.Version != task.Version {
				t.Fatalf("rejected link mutated: %#v", got)
			}
			stored, _ := s.GetExternalLaunchRequest(ctx, request.ID)
			if stored.Status != status {
				t.Fatal("link changed frozen request")
			}
		})
	}
}

func TestTaskSourceCannotLinkChat(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "work"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.LinkTaskChat(ctx, source.ID, model.LinkTaskChatInput{ExpectedVersion: source.Version, ThreadID: "chat"}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("source linked: %v", err)
	}
}
