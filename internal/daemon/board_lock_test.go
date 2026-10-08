package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestBoardLocalChangesDoNotWaitForSync(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "existing", Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	done := make(chan error, 1)
	go func() {
		if _, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "new", Column: model.TaskInbox}); err != nil {
			done <- err
			return
		}
		_, err := s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskDone})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("local board changes blocked by background Sync")
	}
}

func TestBoardReorderDoesNotWaitForDispatchIO(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	task, err := s.taskBoard.AddChat(ctx, "busy-chat", "busy chat")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	barrier := &boardReadBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"busy-chat": boardThreadPayload("busy-chat", "inProgress", "active-turn", "")}}, entered: make(chan struct{}), release: make(chan struct{})}
	s.poll, s.pollConnected = barrier, true
	request := daemonExternalRequest("test:reorder-during-dispatch", "continue")
	request.TaskID, request.TriggerEventID, request.TargetThreadID = task.ID, "event:"+task.ID, "busy-chat"
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
	request = prepareStartingExternalRequestRecord(t, s, request)
	dispatched := make(chan struct{})
	go func() { s.dispatchExternalLaunchRequest(ctx, request.ID); close(dispatched) }()
	<-barrier.entered
	defer func() { close(barrier.release); <-dispatched }()
	done := make(chan error, 1)
	go func() {
		_, err := s.ReorderTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: task.Column, Position: 5})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("reorder blocked by dispatch network IO")
	}
	got, err := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || got.Status != model.ExternalLaunchStarting {
		t.Fatalf("reorder changed launch: %#v, %v", got, err)
	}
}

func TestBoardLaunchFirstMovePreservesStartedWork(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	task, err := s.taskBoard.AddChat(ctx, "ready-chat", "ready chat")
	if err != nil {
		t.Fatal(err)
	}
	task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	payload := boardThreadPayload("ready-chat", "completed", "previous", "answer")
	barrier := &boardReadBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"ready-chat": payload}}, entered: make(chan struct{}), release: make(chan struct{})}
	writer := &stubSession{threadReads: map[string]map[string]any{"ready-chat": payload}}
	s.poll, s.pollConnected = barrier, true
	s.liveFactory = func() Session { return writer }
	request := daemonExternalRequest("test:launch-first-move", "continue")
	request.TaskID, request.TriggerEventID, request.TargetThreadID = task.ID, "event:"+task.ID, "ready-chat"
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
	request = prepareStartingExternalRequestRecord(t, s, request)
	dispatched := make(chan struct{})
	go func() { s.dispatchExternalLaunchRequest(ctx, request.ID); close(dispatched) }()
	<-barrier.entered
	moved := make(chan error, 1)
	go func() {
		_, err := s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskDone})
		moved <- err
	}()
	select {
	case err := <-moved:
		close(barrier.release)
		<-dispatched
		t.Fatalf("move crossed launch gate: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(barrier.release)
	<-dispatched
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
	got, err := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || got.Status != model.ExternalLaunchSessionStarted || len(writer.turnStartCalls) != 1 {
		t.Fatalf("started work cancelled: %#v, %v, starts=%d", got, err, len(writer.turnStartCalls))
	}
}

func TestBoardMoveMissingTask(t *testing.T) {
	s := newTestService(t)
	if _, err := s.MoveTask(context.Background(), "missing", model.MoveTaskInput{Column: model.TaskDone}); err == nil {
		t.Fatal("missing move succeeded")
	}
}

func TestLaunchCallbackDoesNotWaitForSyncAndDispatchRechecksOff(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	request := daemonExternalRequest("test:callback-sync-off", "must not start")
	if _, err := s.IngestExternalRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkExternalLaunchRequestTelegramSent(ctx, request.ID, 501, model.ExternalLaunchPendingApproval); err != nil {
		t.Fatal(err)
	}
	route := &model.CallbackRoute{Action: "external_launch_start", ThreadID: request.ID, RequestID: request.ID, TelegramMessageID: 501, Status: model.CallbackStatusActive}
	s.syncMu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := s.handleExternalLaunchCallback(ctx, s.cfg.SyncGroupID, request.TelegramTopicID, 501, route)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			s.syncMu.Unlock()
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		s.syncMu.Unlock()
		t.Fatal("launch claim waited for background Sync")
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		s.syncMu.Unlock()
		t.Fatal(err)
	}
	if _, err := s.store.MarkSyncOff(ctx, state.SessionID); err != nil {
		s.syncMu.Unlock()
		t.Fatal(err)
	}
	s.syncMu.Unlock()
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	s.SetSyncForum(forum)
	s.dispatchExternalLaunchRequest(ctx, request.ID)
	got, err := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || got.Status != model.ExternalLaunchPendingApproval || len(writer.threadStartCalls) != 0 || len(forum.creates) != 0 {
		t.Fatalf("Sync-off dispatch escaped fence: %#v, %v", got, err)
	}
}

func TestBoardPriorityDoesNotWaitForSyncOrLaunch(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task"})
	if err != nil {
		t.Fatal(err)
	}
	s.syncMu.Lock()
	s.taskLaunchMu.Lock()
	done := make(chan error, 1)
	go func() {
		card, err := s.SetTaskPriority(ctx, task.ID, model.SetTaskPriorityInput{ExpectedVersion: task.Version, Priority: "high"})
		if err == nil && (card.Priority != "high" || card.Version != task.Version+1) {
			err = fmt.Errorf("bad priority card: %#v", card)
		}
		done <- err
	}()
	select {
	case err := <-done:
		s.taskLaunchMu.Unlock()
		s.syncMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		s.taskLaunchMu.Unlock()
		s.syncMu.Unlock()
		<-done
		t.Fatal("priority waited for gate")
	}
}
