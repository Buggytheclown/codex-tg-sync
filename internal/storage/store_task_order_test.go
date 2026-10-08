package storage

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestTaskNeighborMoveOrdersEqualPositionsAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: title, Column: model.TaskTODO, Config: model.TaskConfig{InitialPrompt: title}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
	}
	sort.Strings(ids)
	a, b, c := ids[0], ids[1], ids[2]
	before := a
	moved, err := s.MoveTask(ctx, c, model.MoveTaskInput{ExpectedVersion: 1, Column: model.TaskTODO, BeforeTaskID: &before})
	if err != nil {
		t.Fatal(err)
	}
	if moved.Version != 2 {
		t.Fatalf("version = %d", moved.Version)
	}
	assertOrder := func(want []string) {
		t.Helper()
		all, err := s.ListTasks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, task := range all {
			if task.Column == model.TaskTODO {
				got = append(got, task.ID)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
	assertOrder([]string{c, a, b})
	for _, id := range []string{a, b} {
		neighbor, err := s.GetTask(ctx, id)
		if err != nil || neighbor.Version != 1 || neighbor.Config.InitialPrompt != neighbor.Title || neighbor.Column != model.TaskTODO {
			t.Fatalf("normalization changed neighboring card: %#v %v", neighbor, err)
		}
	}
	// Another card's version stays usable after position normalization.
	before = b
	if _, err := s.MoveTask(ctx, a, model.MoveTaskInput{ExpectedVersion: 1, Column: model.TaskTODO, BeforeTaskID: &before}); err != nil {
		t.Fatal(err)
	}
	before = ""
	moved, err = s.MoveTask(ctx, c, model.MoveTaskInput{ExpectedVersion: moved.Version, Column: model.TaskTODO, BeforeTaskID: &before})
	if err != nil {
		t.Fatal(err)
	}
	assertOrder([]string{a, b, c})
	before = a
	if _, err := s.MoveTask(ctx, c, model.MoveTaskInput{ExpectedVersion: 1, Column: model.TaskTODO, BeforeTaskID: &before}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale = %v", err)
	}
	before = "missing"
	if _, err := s.MoveTask(ctx, c, model.MoveTaskInput{ExpectedVersion: moved.Version, Column: model.TaskTODO, BeforeTaskID: &before}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale neighbor = %v", err)
	}
	assertOrder([]string{a, b, c})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertOrder([]string{a, b, c})
}

func TestTaskReorderPreservesPollingGenerationAndPendingRequests(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	for _, column := range []string{model.TaskTODO, model.TaskDone, model.TaskPollingColumn} {
		t.Run(column, func(t *testing.T) {
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: column, Column: column, Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
			if err != nil {
				t.Fatal(err)
			}
			// Fixture deliberately represents an unstarted request already in this column.
			_, err = s.db.ExecContext(ctx, `INSERT INTO external_launch_requests(id,source,external_id,sender,title,prompt,status,telegram_topic_id,created_at,updated_at,task_id) VALUES(?, 'test', ?, 'test', 'test', 'test', ?, 1, ?, ?, ?)`, task.ID, task.ID, model.ExternalLaunchPendingApproval, model.NowString(), model.NowString(), task.ID)
			if err != nil {
				t.Fatal(err)
			}
			s.SetTaskPollingValidator(func(model.TaskPolling) error { return errors.New("unavailable validator") })
			defer s.SetTaskPollingValidator(nil)
			before := ""
			moved, err := s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: column, BeforeTaskID: &before})
			if err != nil {
				t.Fatal(err)
			}
			if moved.Polling.Generation != task.Polling.Generation || moved.Polling.NextCheckAt != task.Polling.NextCheckAt {
				t.Fatalf("reorder rearmed polling: %#v", moved.Polling)
			}
			request, err := s.GetExternalLaunchRequest(ctx, task.ID)
			if err != nil || request.Status != model.ExternalLaunchPendingApproval {
				t.Fatalf("reorder cancelled request: %#v %v", request, err)
			}
		})
	}
}
