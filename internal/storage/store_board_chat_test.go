package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"path/filepath"
	"testing"
)

func TestBoardChatReservationRecovery(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "test"})
	if err != nil {
		t.Fatal(err)
	}
	r, create, err := s.BeginBoardChat(ctx, task.ID, model.CreateBoardChatInput{ExpectedVersion: task.Version, AttemptID: "attempt"})
	if err != nil || !create || r.State != "prepared" {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err = s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "another"}); err == nil {
		t.Fatal("reserved task rebound")
	}
	r.State = "unknown"
	if err = s.SaveBoardChat(ctx, task.ID, r); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, create, err = s.BeginBoardChat(ctx, task.ID, model.CreateBoardChatInput{ExpectedVersion: task.Version, AttemptID: "new-attempt"})
	if err != nil || create {
		t.Fatal("unknown operation replayed")
	}
	if _, err = s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "recovered"}); err != nil {
		t.Fatal(err)
	}
	if state, _ := s.GetState(ctx, "board.chat."+task.ID); state != "" {
		t.Fatal("recovery retained reservation")
	}
}
