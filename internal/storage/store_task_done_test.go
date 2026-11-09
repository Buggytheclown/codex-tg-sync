package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestTaskDoneEntryPersistsAndIgnoresEditsAndReorder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "done", Column: model.TaskDone})
	if err != nil {
		t.Fatal(err)
	}
	if task.DoneAt == "" || task.DoneAt != task.CreatedAt {
		t.Fatalf("creation date: %+v", task)
	}
	first := task.DoneAt
	task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskDone, Position: 99})
	if err != nil || task.DoneAt != first {
		t.Fatalf("same column changed date: %+v %v", task, err)
	}
	// A generic edit's updated_at must not become the completion date.
	if _, err = s.db.Exec(`UPDATE tasks SET title='edited',updated_at='2099-01-01T00:00:00Z' WHERE id=?`, task.ID); err != nil {
		t.Fatal(err)
	}
	before := "missing" // Done chronology does not depend on an insertion anchor.
	task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskDone, BeforeTaskID: &before})
	if err != nil || task.DoneAt != first {
		t.Fatalf("reorder changed date: %+v %v", task, err)
	}
	task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskTODO})
	if err != nil || task.DoneAt != "" {
		t.Fatalf("exit: %+v %v", task, err)
	}
	_, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: 1, Column: model.TaskDone})
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatal(err)
	}
	unchanged, _ := s.GetTask(ctx, task.ID)
	if unchanged.DoneAt != "" {
		t.Fatal("conflicting move wrote completion date")
	}
	task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskDone})
	if err != nil || task.DoneAt == "" || task.DoneAt == first {
		t.Fatalf("re-entry: %+v %v", task, err)
	}
	last := task.DoneAt
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, err = s.GetTask(ctx, task.ID)
	if err != nil || task.DoneAt != last {
		t.Fatal("lost on reopen", task, err)
	}
	card, err := s.GetTaskCard(ctx, task.ID)
	if err != nil || card.DoneAt != last {
		t.Fatal("missing compact date", card, err)
	}
}

func TestTaskDonePollingUsesAcceptedTime(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "rule", true: "legacy merge"}[legacy], func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			p := ruleWait(model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskDone})
			if legacy {
				p = model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"42","complete_on_merge":true}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
			}
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "wait", Column: model.TaskPollingColumn, Polling: &p})
			if err != nil {
				t.Fatal(err)
			}
			out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, RuleID: "wait", CheckedAt: "2001-01-01T00:00:00Z", Events: []model.PollingEvent{{Key: "event", Payload: json.RawMessage(`{}`)}}}
			if legacy {
				out.PRStatus = "merged"
				out.Events = nil
			}
			start := time.Now().Add(-time.Second)
			ok, err := s.AcceptTaskPollingOutcome(ctx, out, 0)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			task, _ = s.GetTask(ctx, task.ID)
			date, err := time.Parse(time.RFC3339Nano, string(task.DoneAt))
			if err != nil || date.Before(start) {
				t.Fatal("backdated transition", task.DoneAt, err)
			}
			first := task.DoneAt
			ok, err = s.AcceptTaskPollingOutcome(ctx, out, 0)
			if err != nil || ok {
				t.Fatal("accepted stale outcome", ok, err)
			}
			task, _ = s.GetTask(ctx, task.ID)
			if task.DoneAt != first {
				t.Fatal("duplicate changed completion")
			}
		})
	}
}

func TestTaskDoneMigrationUsesOnlyLatestUnambiguousEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	fixtures := []struct {
		name   string
		events []string
		want   string
	}{
		{"created", []string{`created|{"column":"done"}|2026-11-01T00:00:00Z`}, "2026-10-31T23:59:59.999999Z"},
		{"returned", []string{`moved|{"from":"todo","to":"done"}|2026-11-01T12:00:00Z`, `moved|{"from":"done","to":"polling"}|2026-11-02T12:00:00Z`, `moved|{"from":"polling","to":"done"}|2026-11-03T12:00:00Z`, `edited|{}|2026-11-04T12:00:00Z`}, "2026-11-03T12:00:00Z"},
		{"ambiguous", []string{`moved|{"from":"todo","to":"done"}|2026-11-01T12:00:00Z`, `moved|{"from":"done","to":"polling"}|2026-11-02T12:00:00Z`, `polling_event|{}|2026-11-03T12:00:00Z`}, ""},
		{"merged", []string{`pr_merged|{}|2026-11-03T12:00:00Z`, `reordered|{"from":"done","to":"done"}|2026-11-04T12:00:00Z`}, "2026-11-03T12:00:00Z"},
		{"invalid latest", []string{`created|{"column":"done"}|2026-11-01T12:00:00Z`, `polling_event|{}|invalid`}, ""},
		{"same instant ambiguous", []string{`moved|{"from":"polling","to":"done"}|2026-11-03T12:00:00Z`, `polling_event|{}|2026-11-03T12:00:00Z`}, ""},
		{"fractional order", []string{`created|{"column":"todo"}|2026-11-01T12:00:00Z`, `moved|{"from":"todo","to":"done"}|2026-11-01T12:00:00.000001Z`}, "2026-11-01T12:00:00.000001Z"},
		{"malformed history", []string{`created|{"column":"done"}|2026-11-01T12:00:00Z`, `moved|not-json|2026-11-03T12:00:00Z`}, ""},
		{"missing", nil, ""}, {"invalid", []string{`created|{"column":"done"}|invalid`}, ""},
	}
	ids := map[string]string{}
	for _, f := range fixtures {
		task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: f.name, Column: model.TaskDone})
		if err != nil {
			t.Fatal(err)
		}
		ids[f.name] = task.ID
		if _, err = s.db.Exec(`DELETE FROM task_events WHERE task_id=?`, task.ID); err != nil {
			t.Fatal(err)
		}
		for i, event := range f.events {
			parts := strings.Split(event, "|")
			if parts[0] == "created" {
				// Distinguish task creation from its history write crossing midnight.
				created := parts[2]
				if f.name == "created" {
					created = "2026-10-31T23:59:59.999999Z"
				}
				if _, err = s.db.Exec(`UPDATE tasks SET created_at=? WHERE id=?`, created, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = s.db.Exec(`INSERT INTO task_events(id,task_id,kind,payload_json,created_at) VALUES(?,?,?,?,?)`, fmt.Sprintf("%s-%d", f.name, i), task.ID, parts[0], parts[1], parts[2]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = s.db.Exec(`ALTER TABLE tasks DROP COLUMN done_at`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		task, err := s.GetTask(ctx, ids[f.name])
		if err != nil || string(task.DoneAt) != f.want || task.Version != 1 {
			t.Fatalf("%s: %+v %v", f.name, task, err)
		}
	}
	// Backfill is one-time, not re-run for unknown dates after a restart.
	if _, err = s.db.Exec(`INSERT INTO task_events(id,task_id,kind,payload_json,created_at) VALUES('late',?,'pr_merged','{}','2026-11-05T00:00:00Z')`, ids["missing"]); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, _ := s.GetTask(ctx, ids["missing"])
	if task.DoneAt != "" {
		t.Fatal("backfill repeated")
	}
}

func TestTaskDoneFailedTransitionRollsBackDate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_move_event BEFORE INSERT ON task_events WHEN NEW.kind='moved' BEGIN SELECT RAISE(ABORT,'simulated history failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskDone}); err == nil {
		t.Fatal("expected write failure")
	}
	after, err := s.GetTask(ctx, task.ID)
	if err != nil || after.DoneAt != "" || after.Column != task.Column || after.Version != task.Version {
		t.Fatal("partial transition", after, err)
	}
}

func TestTaskDoneMigrationRollsBackSchemaWithBackfillFailure(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "done", Column: model.TaskDone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`ALTER TABLE tasks DROP COLUMN done_at; CREATE TRIGGER reject_backfill BEFORE UPDATE ON tasks BEGIN SELECT RAISE(ABORT,'simulated backfill failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateTaskDoneDates(ctx); err == nil {
		t.Fatal("expected backfill failure")
	}
	exists, err := s.hasColumn(ctx, "tasks", "done_at")
	if err != nil || exists {
		t.Fatal("schema partially committed", exists, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_backfill`); err != nil {
		t.Fatal(err)
	}
	if err = s.migrateTaskDoneDates(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetTask(ctx, task.ID)
	if err != nil || after.DoneAt != task.DoneAt || after.Version != task.Version {
		t.Fatal("retry lost state", after, err)
	}
}
