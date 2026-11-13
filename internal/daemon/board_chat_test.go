package daemon

import (
	"context"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
)

func TestChatNameLiteralTitle(t *testing.T) {
	got, err := chatName("", "ПР {ticket} {title}", "TASK-42", "Project {ticket} title")
	if err != nil || got != "ПР TASK-42 Project {ticket} title" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err = chatName("{wrong}", "", "", "x"); err == nil {
		t.Fatal("accepted unknown field")
	}
	got, err = chatName("", "ПР {ticket} {title}", "", "Project  title")
	if err != nil || got != "ПР Project  title" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestCreateBoardChatNoTurnAndRetry(t *testing.T) {
	s := newTestService(t)
	s.cfg.BoardChatCWD = t.TempDir()
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "new-chat", "cwd": s.cfg.BoardChatCWD, "model": "gpt-default"}}}
	s.liveFactory = func() Session { return writer }
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Project title", TicketURL: "TASK-42"})
	if err != nil {
		t.Fatal(err)
	}
	input := model.CreateBoardChatInput{ExpectedVersion: task.Version, AttemptID: "attempt"}
	first, err := s.CreateBoardChat(ctx, task.ID, input)
	if err != nil || !first.Fresh || first.State != "ready" {
		t.Fatalf("%+v %v", first, err)
	}
	second, err := s.CreateBoardChat(ctx, task.ID, input)
	if err != nil || second.Fresh || second.ThreadID != first.ThreadID {
		t.Fatalf("%+v %v", second, err)
	}
	if len(writer.threadStartCalls) != 1 || len(writer.turnStartCalls) != 0 || len(writer.threadSetNameCalls) != 1 {
		t.Fatalf("starts %d turns %d names %d", len(writer.threadStartCalls), len(writer.turnStartCalls), len(writer.threadSetNameCalls))
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.ThreadID != "new-chat" {
		t.Fatalf("%+v", got)
	}
}
func TestCreateBoardChatUnknownDoesNotReplay(t *testing.T) {
	s := newTestService(t)
	s.cfg.BoardChatCWD = t.TempDir()
	writer := &stubSession{threadStartErr: errors.New("lost response")}
	s.liveFactory = func() Session { return writer }
	ctx := context.Background()
	task, _ := s.CreateTask(ctx, model.CreateTaskInput{Title: "test"})
	input := model.CreateBoardChatInput{ExpectedVersion: task.Version, AttemptID: "attempt"}
	for range 2 {
		r, err := s.CreateBoardChat(ctx, task.ID, input)
		if err != nil || r.State != "unknown" {
			t.Fatalf("%+v %v", r, err)
		}
	}
	if len(writer.threadStartCalls) != 1 {
		t.Fatalf("%d", len(writer.threadStartCalls))
	}
}

func TestUnexpectedChatDirectoryNeverBecomesReadyOnRetry(t *testing.T) {
	s := newTestService(t)
	s.cfg.BoardChatCWD = t.TempDir()
	writer := &stubSession{threadStartResult: map[string]any{"model": "gpt-default", "thread": map[string]any{"id": "wrong-cwd", "cwd": "/other"}}}
	s.liveFactory = func() Session { return writer }
	ctx := context.Background()
	task, _ := s.CreateTask(ctx, model.CreateTaskInput{Title: "test"})
	input := model.CreateBoardChatInput{ExpectedVersion: task.Version, AttemptID: "attempt"}
	for range 2 {
		r, err := s.CreateBoardChat(ctx, task.ID, input)
		if err != nil || r.State != "validation_failed" {
			t.Fatalf("%+v %v", r, err)
		}
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.ThreadID != "" || len(writer.threadStartCalls) != 1 || len(writer.threadSetNameCalls) != 0 {
		t.Fatal("invalid chat was accepted or recreated")
	}
}

func TestFixNameWaitsForParentAndPreservesExistingName(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	parent, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Project title", TicketURL: "TASK-42"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "PR", CardType: model.CardPR, SourceURL: "https://a.yandex-team.ru/review/42"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err = s.store.LinkTaskChat(ctx, pr.ID, model.LinkTaskChatInput{ExpectedVersion: pr.Version, ThreadID: "fix-chat"})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.store.SetState(ctx, "board.fix.name.fix-chat", pr.ID)
	r, err := s.FixChatName(ctx, pr.ID, false)
	if err != nil || r.Name != "" {
		t.Fatalf("%+v %v", r, err)
	}
	if err = s.store.SetTaskParent(ctx, pr.ID, model.SetTaskParentInput{ExpectedVersion: pr.Version, ParentTaskID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	r, err = s.FixChatName(ctx, pr.ID, false)
	if err != nil || r.Name != "Фикс TASK-42 Project title" {
		t.Fatalf("%+v %v", r, err)
	}
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	current := appserver.SnapshotFromThreadRead(map[string]any{"thread": map[string]any{"id": "fix-chat", "name": "My chosen name"}})
	s.reconcileFixNameLocked(ctx, current)
	if len(writer.threadSetNameCalls) != 0 {
		t.Fatal("manual name overwritten")
	}
	if _, err = s.FixChatName(ctx, pr.ID, true); err != nil {
		t.Fatal(err)
	}
	if len(writer.threadSetNameCalls) != 1 || writer.threadSetNameCalls[0].name != "Фикс TASK-42 Project title" {
		t.Fatalf("%+v", writer.threadSetNameCalls)
	}
	r, err = s.FixChatName(ctx, pr.ID, false)
	if err != nil || r.Name != "" {
		t.Fatal("naming replayed")
	}
	_ = s.store.SetState(ctx, "board.fix.name.fix-chat", pr.ID)
	current = appserver.SnapshotFromThreadRead(map[string]any{"thread": map[string]any{"id": "fix-chat"}})
	s.reconcileFixNameLocked(ctx, current)
	if len(writer.threadSetNameCalls) != 2 {
		t.Fatal("unnamed feedback chat was not named")
	}
}

func TestKnownBoardChatResumesValidationAfterRestart(t *testing.T) {
	s := newTestService(t)
	s.cfg.BoardChatCWD = t.TempDir()
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Project title"})
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"model": "gpt-default", "thread": map[string]any{"id": "recovered", "cwd": s.cfg.BoardChatCWD}}
	s.poll = &stubSession{threadReads: map[string]map[string]any{"recovered": payload}}
	s.pollConnected = true
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	err = s.store.SaveBoardChat(ctx, task.ID, model.BoardChatResult{State: "validating", ThreadID: "recovered", CWD: s.cfg.BoardChatCWD, Model: "gpt-default", Name: "ПР Project title"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateBoardChat(ctx, task.ID, model.CreateBoardChatInput{ExpectedVersion: task.Version, AttemptID: "retry"})
	if err != nil || r.State != "ready" || r.Fresh || len(writer.threadStartCalls) != 0 || len(writer.turnStartCalls) != 0 {
		t.Fatalf("%+v %v", r, err)
	}
}
