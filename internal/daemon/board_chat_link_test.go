package daemon

import (
	"context"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tasks"
	"testing"
	"time"
)

func TestBoardLinkChatPassivelyReadsWithSyncOff(t *testing.T) {
	s := newTestService(t)
	s.cfg.AppServerMode = "websocket"
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "existing task"})
	if err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"existing-chat": boardThreadPayload("existing-chat", "completed", "turn", "result")}}
	s.poll, s.pollConnected = poll, true
	linked, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "existing-chat"})
	if err != nil {
		t.Fatal(err)
	}
	if linked.ThreadID != "existing-chat" || linked.Execution == nil {
		t.Fatalf("linked: %#v", linked)
	}
	if len(poll.threadResumeCalls)+len(poll.threadStartCalls)+len(poll.turnStartCalls) != 0 {
		t.Fatal("link launched or resumed work")
	}
	s.pollConnected = false
	duplicate, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: linked.Version, ThreadID: linked.ThreadID})
	if err != nil || duplicate.Version != linked.Version {
		t.Fatalf("duplicate: %#v %v", duplicate, err)
	}
}

func TestBoardLinkChatRejectsInvalidVersionBeforeAuthorityRead(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "existing task"})
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{0, -1} {
		_, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: version, ThreadID: "existing-chat"})
		if !errors.Is(err, tasks.ErrInvalid) {
			t.Fatalf("version %d: %v", version, err)
		}
	}
	got, err := s.GetTask(ctx, task.ID)
	if err != nil || got.ThreadID != "" || got.Version != task.Version {
		t.Fatalf("invalid command mutated task: %#v %v", got, err)
	}
}

func TestBoardLinkChatRejectsPendingLaunch(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "existing task"})
	if err != nil {
		t.Fatal(err)
	}
	request := daemonExternalRequest("test:link-pending", "prompt")
	request.TaskID, request.TriggerEventID = task.ID, "event:"+task.ID
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
	if _, err := s.store.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"existing-chat": boardThreadPayload("existing-chat", "completed", "turn", "result")}}
	s.poll, s.pollConnected = poll, true
	if _, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "existing-chat"}); !errors.Is(err, tasks.ErrConflict) {
		t.Fatalf("pending link: %v", err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.ThreadID != "" || got.Version != task.Version {
		t.Fatalf("mutated task: %#v", got)
	}
	stored, _ := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if stored.Status != model.ExternalLaunchPendingApproval {
		t.Fatalf("changed launch: %#v", stored)
	}
}

func TestBoardLinkChatAuthorityFailureDoesNotBind(t *testing.T) {
	for _, kind := range []string{"failure", "missing", "wrong"} {
		t.Run(kind, func(t *testing.T) {
			s := newTestService(t)
			s.cfg.AppServerMode = "websocket"
			ctx := context.Background()
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "existing"})
			if err != nil {
				t.Fatal(err)
			}
			poll := &stubSession{threadReads: map[string]map[string]any{}}
			if kind == "failure" {
				poll.threadReadErr = errors.New("authority unavailable")
			}
			if kind == "wrong" {
				poll.threadReads["requested"] = boardThreadPayload("wrong", "completed", "turn", "")
			}
			s.poll, s.pollConnected = poll, true
			if _, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "requested"}); err == nil {
				t.Fatal("invalid authority linked")
			}
			got, _ := s.GetTask(ctx, task.ID)
			if got.ThreadID != "" || got.Version != task.Version {
				t.Fatalf("authority failure wrote: %#v", got)
			}
			if len(poll.threadResumeCalls)+len(poll.threadStartCalls)+len(poll.turnStartCalls) != 0 {
				t.Fatal("link performed model work")
			}
		})
	}
}

func TestBoardLinkChatReadsOutsideLaunchAndSyncGates(t *testing.T) {
	s := newTestService(t)
	s.cfg.AppServerMode = "websocket"
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "existing"})
	if err != nil {
		t.Fatal(err)
	}
	barrier := &boardReadBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"chat": boardThreadPayload("chat", "completed", "turn", "")}}, entered: make(chan struct{}), release: make(chan struct{})}
	s.poll, s.pollConnected = barrier, true
	s.taskLaunchMu.Lock()
	s.syncMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := s.LinkTaskChat(ctx, task.ID, model.LinkTaskChatInput{ExpectedVersion: task.Version, ThreadID: "chat"})
		done <- err
	}()
	select {
	case <-barrier.entered:
	case <-time.After(time.Second):
		s.taskLaunchMu.Unlock()
		s.syncMu.Unlock()
		close(barrier.release)
		<-done
		t.Fatal("authority read waited for a gate")
	}
	close(barrier.release)
	s.taskLaunchMu.Unlock()
	select {
	case err := <-done:
		s.syncMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		s.syncMu.Unlock()
		<-done
		t.Fatal("link waited for Sync")
	}
}
