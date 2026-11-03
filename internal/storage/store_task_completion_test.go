package storage

import (
	"context"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func startPollingTaskForCompletion(t *testing.T, s *Store, initial, auto bool) (*model.Task, string) {
	t.Helper()
	ctx := context.Background()
	var task *model.Task
	var err error
	if initial {
		source, out := makeTaskBoardSource(t, s, "")
		p := *source.Polling
		p.Action.Column = model.TaskTODO
		p.Action.InitialLaunch = &model.TaskLaunchPolicy{Mode: "manual"}
		if auto {
			p.Action.InitialLaunch.Mode = "auto"
		}
		source, err = s.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: p})
		if err != nil {
			t.Fatal(err)
		}
		out.Generation = source.Polling.Generation
		if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
			t.Fatal(ok, err)
		}
		items, err := s.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for i := range items {
			if items[i].SourceTaskID == source.ID {
				task = &items[i]
			}
		}
	} else {
		p := ruleWait(model.TaskPollingAction{Type: model.PollingResumeTask, Column: model.TaskTODO, Prompt: "continue", AutoStart: auto})
		task, err = s.CreateTask(ctx, model.CreateTaskInput{Title: "task", ThreadID: "chat", Column: model.TaskPollingColumn, Polling: &p})
		if err != nil {
			t.Fatal(err)
		}
		out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, RuleID: "wait", Events: []model.PollingEvent{{Key: "one", Payload: []byte(`{}`)}}}
		if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
			t.Fatal(ok, err)
		}
		task, err = s.GetTask(ctx, task.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if task == nil || task.Column != model.TaskTODO || len(task.Requests) != 1 {
		t.Fatalf("polling task: %+v", task)
	}
	id := task.Requests[0].ID
	if ok, err := s.ClaimExternalLaunchRequest(ctx, id); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.CompleteExternalLaunchRequest(ctx, id, model.ExternalLaunchSessionStarted, "chat", "poll-turn", "", ""); err != nil || !ok {
		t.Fatal(ok, err)
	}
	task, err = s.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return task, id
}

func TestPollingTaskCompletionReturnsInbox(t *testing.T) {
	for _, initial := range []bool{false, true} {
		for _, auto := range []bool{false, true} {
			for _, status := range []string{model.ExternalLaunchSessionCompleted, model.ExternalLaunchSessionFailed, model.ExternalLaunchSessionInterrupted} {
				name := map[bool]string{false: "continuation", true: "initial"}[initial] + "/" + map[bool]string{false: "approved", true: "auto"}[auto] + "/" + status
				t.Run(name, func(t *testing.T) {
					s := openTestStore(t)
					ctx := context.Background()
					task, _ := startPollingTaskForCompletion(t, s, initial, auto)
					if ok, err := s.CompleteExternalTurn(ctx, "chat", "manual-turn", status, "manual"); err != nil || ok {
						t.Fatal("manual turn matched polling request", ok, err)
					}
					got, _ := s.GetTask(ctx, task.ID)
					if got.Column != model.TaskTODO {
						t.Fatal("manual turn moved task", got.Column)
					}
					if ok, err := s.CompleteExternalTurn(ctx, "chat", "poll-turn", status, "done"); err != nil || !ok {
						t.Fatal(ok, err)
					}
					got, _ = s.GetTask(ctx, task.ID)
					if got.Column != model.TaskInbox || got.Version != task.Version+1 || got.ThreadID != "chat" || got.Polling.Generation != task.Polling.Generation {
						t.Fatalf("completion: %+v", got)
					}
					if ok, err := s.CompleteExternalTurn(ctx, "chat", "poll-turn", status, "duplicate"); err != nil || ok {
						t.Fatal(ok, err)
					}
					again, _ := s.GetTask(ctx, task.ID)
					if again.Version != got.Version {
						t.Fatal("duplicate completion moved task")
					}
					events, err := s.ListTaskEvents(ctx, task.ID)
					if err != nil {
						t.Fatal(err)
					}
					count := 0
					for _, event := range events {
						if event.Kind == "polling_run_completed" {
							count++
						}
					}
					if count != 1 {
						t.Fatalf("completion history entries: %d", count)
					}
				})
			}
		}
	}
}

func TestManualTaskCompletionDoesNotMoveTODO(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "manual", Column: model.TaskTODO, ThreadID: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSnapshot(ctx, "chat", model.ThreadSnapshotState{LastSeenTurnID: "manual-turn", LastSeenTurnStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteExternalTurn(ctx, "chat", "manual-turn", model.ExternalLaunchSessionCompleted, "manual result"); err != nil || ok {
		t.Fatal(ok, err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Column != model.TaskTODO || got.Version != task.Version {
		t.Fatal("manual completion changed card", got)
	}
}

func TestPollingTaskCompletionPreservesManualChanges(t *testing.T) {
	for _, change := range []string{"done", "inbox", "polling", "round-trip", "later-turn", "reorder"} {
		t.Run(change, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			task, _ := startPollingTaskForCompletion(t, s, false, false)
			want := change
			switch change {
			case "later-turn":
				want = model.TaskTODO
				if err := s.UpsertSnapshot(ctx, "chat", model.ThreadSnapshotState{LastSeenTurnID: "later-manual-turn", LastSeenTurnStatus: "completed"}); err != nil {
					t.Fatal(err)
				}
			default:
				column := change
				if change == "round-trip" {
					column, want = model.TaskInbox, model.TaskTODO
				} else if change == "reorder" {
					column, want = model.TaskTODO, model.TaskInbox
				}
				var err error
				task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: column, Position: 123})
				if err != nil {
					t.Fatal(err)
				}
				if change == "round-trip" {
					if _, err := s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskTODO}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if ok, err := s.CompleteExternalTurn(ctx, "chat", "poll-turn", model.ExternalLaunchSessionCompleted, "done"); err != nil || !ok {
				t.Fatal(ok, err)
			}
			got, _ := s.GetTask(ctx, task.ID)
			if got.Column != want {
				t.Fatalf("column=%s, want %s", got.Column, want)
			}
		})
	}
}

func TestPollingTaskCompletionRollsBackAndRetries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, requestID := startPollingTaskForCompletion(t, s, false, false)
	if _, err := s.db.Exec(`CREATE TRIGGER reject_return BEFORE UPDATE OF column ON tasks WHEN NEW.column='inbox' BEGIN SELECT RAISE(ABORT,'unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteExternalTurn(ctx, "chat", "poll-turn", model.ExternalLaunchSessionCompleted, "done"); err == nil || ok {
		t.Fatal("partial completion", ok, err)
	}
	request, _ := s.GetExternalLaunchRequest(ctx, requestID)
	if request.Status != model.ExternalLaunchSessionStarted {
		t.Fatal("failed return lost retry", request.Status)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_return`); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.CompleteExternalTurn(ctx, "chat", "poll-turn", model.ExternalLaunchSessionCompleted, "done"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Column != model.TaskInbox {
		t.Fatal(got.Column)
	}
}
