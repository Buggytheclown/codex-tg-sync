package storage

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestTaskWorkflowSaveValidatesResultingSettings(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"1","complete_on_merge":false}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "wait", ThreadID: "chat", Column: model.TaskPollingColumn, Config: model.TaskConfig{ContinuationPrompt: "old continuation"}, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{model.PollingReturnTask, model.PollingResumeTask} {
		p.Action.Type = action
		config := task.Config
		config.InitialPrompt = "new initial"
		config.ContinuationPrompt = ""
		if action == model.PollingResumeTask {
			config.ContinuationPrompt = "new continuation"
		}
		before := task
		task, err = s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, Config: config, Polling: &p})
		if err != nil {
			t.Fatalf("atomic transition to %s: %v", action, err)
		}
		if task.Config != config || task.Polling.Action.Type != action || task.Version != before.Version+1 || task.Polling.Generation != before.Polling.Generation+1 {
			t.Fatalf("settings not saved atomically: %#v", task)
		}
		if task.Column != before.Column || task.ThreadID != before.ThreadID || task.Position != before.Position {
			t.Fatal("workflow save changed task runtime identity or placement")
		}
	}
}

func TestTaskWorkflowSaveRejectedChangesRollBack(t *testing.T) {
	for _, reason := range []string{"stale", "missing continuation", "missing chat", "invalid shape", "adapter validation", "source action", "source owner"} {
		t.Run(reason, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			p := model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"1"}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
			thread := "chat"
			if reason == "missing chat" {
				thread = ""
			}
			if reason == "source owner" {
				p.Mode = model.PollingRepeat
				p.Action = model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "first"}}}
			}
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "wait", ThreadID: thread, Column: model.TaskPollingColumn, Config: model.TaskConfig{InitialPrompt: "original"}, Polling: &p})
			if err != nil {
				t.Fatal(err)
			}
			before, _ := s.GetTask(ctx, task.ID)
			eventsBefore, _ := s.ListTaskEvents(ctx, task.ID)
			p = *task.Polling
			p.Config = json.RawMessage(`{"pr_id":"2"}`)
			input := model.UpdateTaskInput{ExpectedVersion: task.Version, Title: "edited", Config: task.Config, Polling: &p}
			want := ErrTaskInvalid
			switch reason {
			case "stale":
				input.ExpectedVersion--
				want = ErrTaskConflict
			case "missing continuation", "missing chat":
				p.Action.Type = model.PollingResumeTask
				if reason == "missing chat" {
					input.Config.ContinuationPrompt = "continue"
				}
			case "invalid shape":
				p.Mode = model.PollingRepeat
			case "adapter validation":
				s.SetTaskPollingValidator(func(model.TaskPolling) error { return errors.New("invalid wait target") })
			case "source action":
				p.Mode = model.PollingRepeat
				p.Action = model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "first"}}}
			}
			if _, err := s.UpdateTask(ctx, task.ID, input); !errors.Is(err, want) {
				t.Fatalf("rejected save: %v, want %v", err, want)
			}
			after, _ := s.GetTask(ctx, task.ID)
			eventsAfter, _ := s.ListTaskEvents(ctx, task.ID)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(eventsBefore, eventsAfter) {
				t.Fatal("rejected save partially changed task, polling or history")
			}
		})
	}
}

func TestTaskWorkflowSaveOwnsRuntimeAndCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "wait", Column: model.TaskPollingColumn, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"1"}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE task_polling SET cursor_json='{"head":"accepted"}',next_check_at='future',last_attempt_at='attempt',last_success_at='success',last_error='error' WHERE task_id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	task, _ = s.GetTask(ctx, task.ID)
	for _, target := range []string{"1", "2"} {
		before := task
		p := *task.Polling
		p.Config = json.RawMessage(`{"pr_id":"` + target + `","conditions":["all_resolved"],"complete_on_merge":true}`)
		p.TaskID = "other-task"
		p.Generation = 999
		p.Cursor = json.RawMessage(`{"head":"caller"}`)
		p.NextCheckAt, p.LastAttemptAt, p.LastSuccessAt, p.LastError = "caller", "caller", "caller", "caller"
		task, err = s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, Config: task.Config, Polling: &p})
		if err != nil {
			t.Fatal(err)
		}
		wantCursor := `{"head":"accepted"}`
		if target == "2" {
			wantCursor = `{}`
		}
		if task.Version != before.Version+1 || task.Polling.Generation != before.Polling.Generation+1 || task.Polling.TaskID != task.ID || string(task.Polling.Cursor) != wantCursor {
			t.Fatalf("runtime/cursor: %#v", task.Polling)
		}
		if task.Polling.NextCheckAt != "" || task.Polling.LastAttemptAt != "" || task.Polling.LastSuccessAt != "" || task.Polling.LastError != "" {
			t.Fatalf("save retained caller/runtime health: %#v", task.Polling)
		}
		if complete, err := model.TaskPollingCompleteOnMerge(task.Polling.Config, "", ""); err != nil || !complete {
			t.Fatalf("explicit merge option lost: %v %v", complete, err)
		}
		if accepted, err := s.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: task.ID, Generation: before.Polling.Generation, Cursor: json.RawMessage(`{"head":"stale"}`)}, 0); err != nil || accepted {
			t.Fatalf("old generation outcome accepted: %v %v", accepted, err)
		}
	}
}

func TestTaskWorkflowSavePollingWriteFailureRollsBackTask(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	p := model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"1"}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "original", Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetTask(ctx, task.ID)
	eventsBefore, _ := s.ListTaskEvents(ctx, task.ID)
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER reject_polling_update BEFORE UPDATE ON task_polling BEGIN SELECT RAISE(ABORT, 'polling write rejected'); END`); err != nil {
		t.Fatal(err)
	}
	p.Config = json.RawMessage(`{"pr_id":"2"}`)
	if _, err := s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: "edited", Config: model.TaskConfig{InitialPrompt: "edited"}, Polling: &p}); err == nil {
		t.Fatal("expected polling persistence failure")
	}
	after, _ := s.GetTask(ctx, task.ID)
	eventsAfter, _ := s.ListTaskEvents(ctx, task.ID)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(eventsBefore, eventsAfter) {
		t.Fatal("failed polling write partially changed task or history")
	}
}

func TestTaskWorkflowSaveAddsInactivePollingWithoutChangingFrozenRequest(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, outcome := makeTaskBoardSource(t, s, "")
	source.Polling.Action.ChildDefinition.Polling = nil
	source, err := s.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: *source.Polling})
	if err != nil {
		t.Fatal(err)
	}
	outcome.Generation = source.Polling.Generation
	if _, err := s.AcceptTaskPollingOutcome(ctx, outcome, 123); err != nil {
		t.Fatal(err)
	}
	items, _ := s.ListTasks(ctx)
	var child model.Task
	for _, task := range items {
		if task.ID != source.ID {
			child = task
		}
	}
	requestBefore, _ := s.GetExternalLaunchRequest(ctx, child.Requests[0].ID)
	p := model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"1","complete_on_merge":false}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}
	config := child.Config
	config.InitialPrompt = "future initial"
	config.ContinuationPrompt = "future continuation"
	config.Execution.CWD = "/new-workspace"
	got, err := s.UpdateTask(ctx, child.ID, model.UpdateTaskInput{ExpectedVersion: child.Version, Title: child.Title, SourceURL: child.SourceURL, Config: config, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	requestAfter, _ := s.GetExternalLaunchRequest(ctx, child.Requests[0].ID)
	if !reflect.DeepEqual(requestBefore, requestAfter) || len(got.Requests) != len(child.Requests) || got.SourceTaskID != source.ID || got.SourceKey != child.SourceKey || got.Column != child.Column || got.ThreadID != child.ThreadID {
		t.Fatal("workflow save changed frozen launch, provenance, column or chat")
	}
	if got.Polling == nil || got.Polling.Generation != 1 || got.Config != config {
		t.Fatalf("new inactive waiting settings: %#v", got)
	}
}
