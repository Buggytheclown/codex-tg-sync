package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestReorderCannotChangeColumnEvenWithoutVersion(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task", Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{0, task.Version} {
		err := s.ReorderTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: version, Column: model.TaskDone, Position: 7})
		if !errors.Is(err, ErrTaskConflict) {
			t.Fatalf("version %d: %v", version, err)
		}
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil || got.Column != task.Column || got.Version != task.Version {
		t.Fatalf("task changed: %#v, %v", got, err)
	}
	if err := s.ReorderTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: task.Column, Position: 7}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReorderTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: task.Column, Position: 8}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale reorder: %v", err)
	}
	got, err = s.GetTask(ctx, task.ID)
	if err != nil || got.Position != 7 || got.Version != task.Version+1 {
		t.Fatalf("bad reorder: %#v, %v", got, err)
	}
}
