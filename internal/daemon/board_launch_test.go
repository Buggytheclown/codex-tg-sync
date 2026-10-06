package daemon

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/polling"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestBoardContinuationStartsOnlyTargetThread(t *testing.T) {
	s := activeSyncService(t)
	payload := boardThreadPayload("board-thread", "completed", "old-turn", "old answer")
	poll := &stubSession{threadReads: map[string]map[string]any{"board-thread": payload}}
	writer := &stubSession{threadReads: map[string]map[string]any{"board-thread": payload}}
	s.poll, s.pollConnected = poll, true
	s.liveFactory = func() Session { return writer }
	request := daemonExternalRequest("test:continuation", "continue frozen prompt")
	request.TargetThreadID = "board-thread"
	policy, reviewer, sandbox := "never", "auto_review", "workspace-write"
	request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
	request = prepareStartingExternalRequestRecord(t, s, request)
	s.dispatchExternalLaunchRequest(context.Background(), request.ID)
	got, err := s.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if err != nil || got.Status != model.ExternalLaunchSessionStarted || got.ThreadID != "board-thread" {
		t.Fatalf("request=%#v err=%v", got, err)
	}
	if len(writer.threadStartCalls) != 0 || len(writer.turnSteerCalls) != 0 || len(writer.turnStartCalls) != 1 || writer.turnStartCalls[0].threadID != "board-thread" {
		t.Fatalf("writer=%#v", writer)
	}
	if len(writer.threadResumeCalls) != 1 || writer.threadResumeCalls[0].threadID != "board-thread" || writer.turnStartCalls[0].approvalPolicy != policy {
		t.Fatalf("resume/options=%#v %#v", writer.threadResumeCalls, writer.turnStartCalls)
	}
	s.dispatchExternalLaunchRequest(context.Background(), request.ID)
	if len(writer.turnStartCalls) != 1 {
		t.Fatal("continuation replayed")
	}
}

func TestBoardContinuationBusyOrMissingNeverSteersOrCreates(t *testing.T) {
	for _, kind := range []string{"busy", "missing", "resume_missing"} {
		t.Run(kind, func(t *testing.T) {
			s := activeSyncService(t)
			payload := boardThreadPayload("board-thread", "completed", "old-turn", "answer")
			if kind == "busy" {
				payload = boardThreadPayload("board-thread", "inProgress", "active-turn", "")
			}
			poll := &stubSession{threadReads: map[string]map[string]any{"board-thread": payload}}
			writer := &stubSession{threadReads: map[string]map[string]any{"board-thread": payload}}
			if kind == "missing" {
				poll.threadReadErr = errors.New("no rollout found for thread id")
			}
			if kind == "resume_missing" {
				writer.threadResumeErr = errors.New("no rollout found for thread id")
			}
			s.poll, s.pollConnected = poll, true
			s.liveFactory = func() Session { return writer }
			request := daemonExternalRequest("test:"+kind, "continue")
			request.TargetThreadID = "board-thread"
			request = prepareStartingExternalRequestRecord(t, s, request)
			s.dispatchExternalLaunchRequest(context.Background(), request.ID)
			got, _ := s.store.GetExternalLaunchRequest(context.Background(), request.ID)
			want := model.ExternalLaunchFailed
			if kind == "busy" {
				want = model.ExternalLaunchPendingApproval
			}
			if got.Status != want || got.ErrorSummary == "" {
				t.Fatalf("request=%#v want %s", got, want)
			}
			if kind == "busy" && !strings.Contains(got.ErrorSummary, "Start") {
				t.Fatalf("non-actionable busy error: %s", got.ErrorSummary)
			}
			if len(writer.threadStartCalls)+len(writer.turnStartCalls)+len(writer.turnSteerCalls) != 0 {
				t.Fatalf("unexpected mutation: %#v", writer)
			}
		})
	}
}

func boardThreadPayload(id, status, turnID, final string) map[string]any {
	threadStatus := "idle"
	if status == "inProgress" {
		threadStatus = "active"
	}
	return map[string]any{"thread": map[string]any{"id": id, "cwd": "/project", "status": map[string]any{"type": threadStatus}, "turns": []any{map[string]any{"id": turnID, "status": status, "items": []any{map[string]any{"id": "final", "type": "agentMessage", "text": final}}}}}}
}

func TestBoardMoveCancelsClaimedRequestBeforeDispatch(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "cancel before start", Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	request := daemonExternalRequest("test:cancel-claimed", "must never start")
	request.TaskID, request.TriggerEventID = task.ID, "event:"+task.ID
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
	request = prepareStartingExternalRequestRecord(t, s, request)
	_, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskTODO})
	if err != nil {
		t.Fatal(err)
	}
	s.dispatchExternalLaunchRequest(ctx, request.ID)
	got, _ := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if got.Status != model.ExternalLaunchDismissed {
		t.Fatalf("uncancelled request=%#v", got)
	}
	if len(writer.threadStartCalls)+len(writer.turnStartCalls) != 0 {
		t.Fatal("cancelled request started")
	}
}

func TestBoardSnapshotProducerTracksChatsWithSyncOffAndNoTopic(t *testing.T) {
	for _, mode := range []string{"no-topic", "off", "expired-topic"} {
		t.Run(mode, func(t *testing.T) {
			s := activeSyncService(t)
			s.cfg.AppServerMode = "websocket"
			ctx := context.Background()
			payload := boardThreadPayload("board-projection", "completed", "terminal-turn", "shared terminal result")
			poll := &stubSession{threadReads: map[string]map[string]any{"board-projection": payload}}
			s.poll, s.pollConnected = poll, true
			task, err := s.AddChat(ctx, "board-projection", "linked chat")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "off" {
				state, _ := s.store.GetSyncState(ctx)
				if _, err := s.store.MarkSyncOff(ctx, state.SessionID); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "expired-topic" {
				state, _ := s.store.GetSyncState(ctx)
				if err := s.store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: state.SessionID, ChatID: state.ChatID, TopicID: 30, ThreadID: "board-projection", TelegramState: model.SyncTopicCleanup}); err != nil {
					t.Fatal(err)
				}
			}
			s.reconcileSync(ctx)
			for _, resume := range poll.threadResumeCalls {
				if resume.threadID == "board-projection" {
					t.Fatal("passive board projection resumed chat and changed its activity")
				}
			}

			got, err := s.GetTask(ctx, task.ID)
			if err != nil || got.Execution == nil || got.Execution.LastSeenTurnID != "terminal-turn" {
				t.Fatalf("task=%#v err=%v", got, err)
			}
			// A late event read of the same turn cannot replace the terminal projection.
			poll.threadReads["board-projection"] = boardThreadPayload("board-projection", "inProgress", "terminal-turn", "")
			s.markSyncThreadDirty("board-projection")
			s.reconcileDirtySyncThreads(ctx)
			got, _ = s.GetTask(ctx, task.ID)
			if got.Execution.LastSeenTurnStatus != "completed" {
				t.Fatalf("terminal regressed: %#v", got.Execution)
			}
			poll.threadReads["board-projection"] = boardThreadPayload("board-projection", "completed", "new-terminal-turn", "updated result")
			s.syncPollEventProcess, s.syncPollEventGeneration = poll, 7
			s.handleSyncPollEvent(ctx, poll, appserver.Event{Method: "turn/completed", Params: map[string]any{"threadId": "board-projection", "turnId": "new-terminal-turn"}}, 7)
			s.reconcileDirtySyncThreads(ctx)
			got, _ = s.GetTask(ctx, task.ID)
			if got.Execution.LastSeenTurnID != "new-terminal-turn" {
				t.Fatalf("dirty board event did not update projection: %#v", got.Execution)
			}
			for _, resume := range poll.threadResumeCalls {
				if resume.threadID == "board-projection" {
					t.Fatal("dirty board event resumed passive chat")
				}
			}

		})
	}
}

type boardReadBarrier struct {
	*stubSession
	entered chan struct{}
	release chan struct{}
}

func (s *boardReadBarrier) ThreadRead(ctx context.Context, id string, turns bool) (map[string]any, error) {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return s.stubSession.ThreadRead(ctx, id, turns)
}
func TestBoardBusyResetAndMoveShareLaunchGate(t *testing.T) {
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
	request := daemonExternalRequest("test:busy-move", "continue")
	request.TaskID, request.TriggerEventID, request.TargetThreadID = task.ID, "event:"+task.ID, "busy-chat"
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
		t.Fatalf("move crossed launch gate: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(barrier.release)
	<-dispatched
	if err := <-moved; err != nil {
		t.Fatal(err)
	}
	got, _ := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if got.Status != model.ExternalLaunchDismissed {
		t.Fatalf("busy reset resurrected moved task: %#v", got)
	}
}
func TestBoardUnknownContinuationCannotReplayOrBindPreviousTurn(t *testing.T) {
	s := activeSyncService(t)
	payload := boardThreadPayload("unknown-chat", "completed", "previous-turn", "answer")
	poll := &stubSession{threadReads: map[string]map[string]any{"unknown-chat": payload}}
	writer := &stubSession{threadReads: map[string]map[string]any{"unknown-chat": payload}, turnStartErr: errors.New("request timeout for turn/start")}
	s.poll, s.pollConnected = poll, true
	s.liveFactory = func() Session { return writer }
	request := daemonExternalRequest("test:unknown-continuation", "continue")
	request.TargetThreadID = "unknown-chat"
	request = prepareStartingExternalRequestRecord(t, s, request)
	s.dispatchExternalLaunchRequest(context.Background(), request.ID)
	got, _ := s.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if got.Status != model.ExternalLaunchOutcomeUnknown {
		t.Fatalf("request=%#v", got)
	}
	s.persistSyncSnapshot(context.Background(), model.SyncTopic{ThreadID: "unknown-chat"}, appserver.SnapshotFromThreadRead(payload))
	response, err := s.checkExternalLaunchOutcome(context.Background(), *got)
	if err != nil || !strings.Contains(response.CallbackText, "remains unknown") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	got, _ = s.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if got.Status != model.ExternalLaunchOutcomeUnknown {
		t.Fatalf("prior turn incorrectly bound: %#v", got)
	}
}
func TestBoardAddChatDuplicateWorksWhileAppServerUnavailable(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	task, err := s.taskBoard.AddChat(ctx, "linked-chat", "chat")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AddChat(ctx, "linked-chat", "new title")
	if err != nil || got.ID != task.ID {
		t.Fatalf("task=%#v err=%v", got, err)
	}
}

func TestBoardInitialLaunchReconcilesDurableReceiptAfterBindingFailure(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "initial task", Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.cfg.Paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TRIGGER fail_started_binding BEFORE UPDATE ON external_launch_requests WHEN NEW.status='session_started' BEGIN SELECT RAISE(ABORT,'injected completion failure'); END`); err != nil {
		t.Fatal(err)
	}
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "receipt-thread", "cwd": "/work"}}}
	s.liveFactory = func() Session { return writer }
	s.SetSyncForum(&fakeSyncForum{nextTopicID: 20})
	request := daemonExternalRequest("test:initial-receipt", "initial frozen prompt")
	request.TaskID, request.TriggerEventID = task.ID, "event:"+task.ID
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
	request = prepareStartingExternalRequestRecord(t, s, request)
	s.dispatchExternalLaunchRequest(ctx, request.ID)
	got, _ := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if got.Status != model.ExternalLaunchOutcomeUnknown {
		t.Fatalf("request=%#v", got)
	}
	if _, err := db.Exec(`DROP TRIGGER fail_started_binding`); err != nil {
		t.Fatal(err)
	}
	s.reconcileDurableExternalLaunchReceipts(ctx)
	got, _ = s.store.GetExternalLaunchRequest(ctx, request.ID)
	linked, _ := s.GetTask(ctx, task.ID)
	if got.Status != model.ExternalLaunchSessionStarted || linked.ThreadID != "receipt-thread" {
		t.Fatalf("request=%#v task=%#v", got, linked)
	}
	s.dispatchExternalLaunchRequest(ctx, request.ID)
	if len(writer.turnStartCalls) != 1 {
		t.Fatal("binding recovery replayed initial launch")
	}
}

type boardTurnSession struct {
	*stubSession
	turnID string
}

func (s *boardTurnSession) TurnStart(ctx context.Context, id, prompt, cwd string, options appserver.TurnStartOptions) (map[string]any, error) {
	_, err := s.stubSession.TurnStart(ctx, id, prompt, cwd, options)
	if err != nil {
		return nil, err
	}
	return map[string]any{"turn": map[string]any{"id": s.turnID}}, nil
}
func TestBoardHistoricalContinuationCannotReleaseNewActiveLease(t *testing.T) {
	for _, secondOrigin := range []string{"board", "telegram"} {
		t.Run(secondOrigin, func(t *testing.T) {
			s := activeSyncService(t)
			ctx := context.Background()
			idle := boardThreadPayload("lease-chat", "completed", "00000000-0000-0000-0000-000000000000", "answer")
			writer := &boardTurnSession{stubSession: &stubSession{threadReads: map[string]map[string]any{"lease-chat": idle}}, turnID: "00000000-0000-0000-0000-000000000001"}
			s.poll, s.pollConnected = &stubSession{threadReads: map[string]map[string]any{"lease-chat": idle}}, true
			s.liveFactory = func() Session { return writer }
			request := daemonExternalRequest("test:lease-first", "first continuation")
			request.TargetThreadID = "lease-chat"
			request = prepareStartingExternalRequestRecord(t, s, request)
			s.dispatchExternalLaunchRequest(ctx, request.ID)
			s.processBoardSnapshotLocked(ctx, appserver.SnapshotFromThreadRead(boardThreadPayload("lease-chat", "completed", "00000000-0000-0000-0000-000000000001", "first result")))
			if _, ok := s.syncLeases["lease-chat"]; ok {
				t.Fatal("first terminal lease not released")
			}
			writer.turnID = "00000000-0000-0000-0000-000000000002"
			writer.threadReads["lease-chat"] = boardThreadPayload("lease-chat", "completed", "00000000-0000-0000-0000-000000000001", "first result")
			if secondOrigin == "board" {
				next := daemonExternalRequest("test:lease-second", "next continuation")
				next.TargetThreadID = "lease-chat"
				next = prepareStartingExternalRequestRecord(t, s, next)
				s.dispatchExternalLaunchRequest(ctx, next.ID)
			} else {
				lease, err := s.syncWriter.Reserve(ctx, "lease-chat")
				if err != nil {
					t.Fatal(err)
				}
				if err := s.syncWriter.MarkActive(lease); err != nil {
					t.Fatal(err)
				}
				s.syncLeases["lease-chat"] = lease
			}
			lease, ok := s.syncLeases["lease-chat"]
			if !ok {
				t.Fatal("second lease missing before snapshot")
			}
			s.processBoardSnapshotLocked(ctx, appserver.SnapshotFromThreadRead(boardThreadPayload("lease-chat", "inProgress", "00000000-0000-0000-0000-000000000002", "")))
			current, ok := s.syncLeases["lease-chat"]
			if !ok || current.Generation != lease.Generation || s.syncWriter.Snapshot().Active != 1 {
				t.Fatalf("historical continuation released current active turn: lease=%#v writer=%#v", current, s.syncWriter.Snapshot())
			}
		})
	}
}
func TestBoardContinuationWhileSyncOffRequiresEnableAndExplicitStart(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	state, _ := s.store.GetSyncState(ctx)
	if _, err := s.store.MarkSyncOff(ctx, state.SessionID); err != nil {
		t.Fatal(err)
	}
	_ = s.syncWriter.BeginDrain()
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	request := daemonExternalRequest("test:off-continuation", "continue")
	request.TargetThreadID = "off-chat"
	request = prepareStartingExternalRequestRecord(t, s, request)
	s.dispatchExternalLaunchRequest(ctx, request.ID)
	got, _ := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if got.Status != model.ExternalLaunchPendingApproval || got.ErrorType != "sync_inactive" || !strings.Contains(got.ErrorSummary, "enable Sync") || !strings.Contains(got.ErrorSummary, "Start") {
		t.Fatalf("request=%#v", got)
	}
	if len(writer.turnStartCalls) != 0 {
		t.Fatal("off continuation dispatched")
	}
}

type boardNoopChecker struct{}

func (boardNoopChecker) Check(_ context.Context, p model.TaskPolling, _ time.Time) (model.TaskPollingOutcome, error) {
	return model.TaskPollingOutcome{Cursor: p.Cursor}, nil
}
func TestBoardPollerTypeEditPreservesOneHealthEntryAndTitle(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	tmpl, err := s.CreateTaskTemplate(ctx, model.SaveTaskTemplateInput{Name: "source template", Definition: model.TaskTemplateDefinition{Title: "new task", Config: model.TaskConfig{InitialPrompt: "prompt"}}})
	if err != nil {
		t.Fatal(err)
	}
	p := model.TaskPolling{Type: polling.TypeReview, Mode: model.PollingRepeat, Config: json.RawMessage(`{"login":"reviewer"}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, TemplateID: tmpl.ID}}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "My source", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	runner := polling.NewRunner(s, boardNoopChecker{}, 77)
	if err := runner.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	p.Type, p.Config = polling.TypeCron, json.RawMessage(`{"cron":"0 10 * * *","timezone":"UTC"}`)
	if _, err := s.SetTaskPolling(ctx, task.ID, model.SetTaskPollingInput{ExpectedVersion: task.Version, Polling: p}); err != nil {
		t.Fatal(err)
	}
	if err := runner.PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.PollersSnapshot(ctx)
	if err != nil || strings.Count(snapshot, "My source") != 1 || strings.Contains(snapshot, "pr_review_source") || strings.Contains(snapshot, "board.cron") {
		t.Fatalf("snapshot=%s err=%v", snapshot, err)
	}
	states, err := s.store.ListState(ctx)
	keys := []string{}
	for key := range states {
		if strings.HasPrefix(key, "poller.board.") {
			keys = append(keys, key)
		}
	}
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys=%#v err=%v", keys, err)
	}
}

func TestBoardContinuationSyncTerminalClearsOwnershipBeforeSameGenerationTelegramTurn(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	firstID, secondID := "00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"
	idle := syncCompletedPayload("thread-1", "00000000-0000-0000-0000-000000000000", "prior result")
	writer := &boardTurnSession{stubSession: &stubSession{threadReads: map[string]map[string]any{"thread-1": idle}}, turnID: firstID}
	s.poll, s.pollConnected = &stubSession{threadReads: map[string]map[string]any{"thread-1": idle}}, true
	s.liveFactory = func() Session { return writer }
	request := daemonExternalRequest("test:sync-terminal-first", "first continuation")
	request.TargetThreadID = "thread-1"
	request = prepareStartingExternalRequestRecord(t, s, request)
	s.dispatchExternalLaunchRequest(ctx, request.ID)
	firstLease, ok := s.syncLeases["thread-1"]
	if !ok {
		t.Fatal("first continuation lease missing")
	}
	other, err := s.syncWriter.Reserve(ctx, "other-active-chat")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.syncWriter.MarkActive(other); err != nil {
		t.Fatal(err)
	}
	s.syncLeases["other-active-chat"] = other
	state, _ := s.store.GetSyncState(ctx)
	topic, _ := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, "thread-1")
	s.processSyncSnapshotLocked(ctx, state, s.getSyncForum(), *topic, appserver.SnapshotFromThreadRead(syncCompletedPayload("thread-1", firstID, "first result")), "sync_poll")
	if _, ok := s.syncLeases["thread-1"]; ok {
		t.Fatal("Sync terminal did not release first lease")
	}
	if _, ok := s.boardContinuationLeases["thread-1"]; ok {
		t.Fatal("Sync terminal retained continuation ownership")
	}
	writer.turnID = secondID
	response, err := s.HandleMessageWithID(ctx, -1001, 11, 999, 123456789, "new Telegram turn", 0)
	if err != nil || response.TurnID != secondID {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	nextLease, ok := s.syncLeases["thread-1"]
	if !ok || nextLease.Generation != firstLease.Generation {
		t.Fatalf("next lease=%#v first=%#v", nextLease, firstLease)
	}
	topic, _ = s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, "thread-1")
	s.processSyncSnapshotLocked(ctx, state, s.getSyncForum(), *topic, appserver.SnapshotFromThreadRead(syncRunningPayload("thread-1", secondID)), "sync_poll")
	if _, ok := s.syncLeases["thread-1"]; !ok || s.syncWriter.Snapshot().Active != 2 {
		t.Fatalf("historical ownership released same-generation new turn: %#v", s.syncWriter.Snapshot())
	}
}

type boardActivitySession struct {
	*stubSession
	thread  map[string]any
	resumed bool
}

func (s *boardActivitySession) ThreadResume(ctx context.Context, id, cwd string) (map[string]any, error) {
	if id == s.thread["id"] {
		s.resumed = true
		s.thread["updatedAt"] = time.Now().Unix()
	}
	return s.stubSession.ThreadResume(ctx, id, cwd)
}
func (s *boardActivitySession) ThreadList(context.Context, int, string) (map[string]any, error) {
	return map[string]any{"data": []any{s.thread}}, nil
}
func TestBoardPassiveReadCannotMakeHistoricalChatFreshForSyncDiscovery(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	s.cfg.AppServerMode = "websocket"
	old := time.Now().Add(-72 * time.Hour).Unix()
	row := map[string]any{"id": "historical-board-chat", "title": "Historical chat", "createdAt": old, "updatedAt": old, "status": "completed"}
	payload := syncCompletedPayload("historical-board-chat", "old-turn", "old result")
	payload["thread"].(map[string]any)["createdAt"], payload["thread"].(map[string]any)["updatedAt"] = old, old
	poll := &boardActivitySession{stubSession: &stubSession{threadReads: map[string]map[string]any{"historical-board-chat": payload}}, thread: row}
	s.poll, s.pollConnected = poll, true
	task, err := s.AddChat(ctx, "historical-board-chat", "Historical chat")
	if err != nil {
		t.Fatal(err)
	}
	forum := &fakeSyncForum{nextTopicID: 50}
	s.SetSyncForum(forum)
	// The second pass previously saw synthetic recent activity created by the
	// first board pass's ThreadResume and created an unwanted Telegram topic.
	s.reconcileSync(ctx)
	s.reconcileSync(ctx)
	if poll.resumed || len(forum.creates) != 0 {
		t.Fatalf("passive read caused activity/topic creation: resumed=%t creates=%#v", poll.resumed, forum.creates)
	}
	linked, _ := s.GetTask(ctx, task.ID)
	if linked.Execution == nil || linked.Execution.LastSeenTurnID != "old-turn" {
		t.Fatalf("read-only projection unavailable: %#v", linked)
	}
	// Real Desktop activity remains eligible for the existing Sync discovery.
	row["updatedAt"] = time.Now().Unix()
	s.reconcileSync(ctx)
	if len(forum.creates) != 1 {
		t.Fatalf("actual recent Desktop activity was not discovered: creates=%#v", forum.creates)
	}
}

func TestBoardHistoricalTerminalMetadataBumpDoesNotRecreateTopic(t *testing.T) {
	for _, nextStatus := range []string{"completed", "inProgress"} {
		t.Run(nextStatus, func(t *testing.T) {
			s := activeSyncService(t)
			ctx := context.Background()
			s.cfg.AppServerMode = "websocket"
			old := time.Now().Add(-72 * time.Hour)
			threadID := "metadata-bumped-board-chat"
			payload := syncCompletedPayload(threadID, "old-turn", "old result")
			payload["thread"].(map[string]any)["updatedAt"] = time.Now().Unix()
			row := map[string]any{"id": threadID, "title": "Historical chat", "createdAt": old.Unix(), "updatedAt": time.Now().Unix(), "status": "completed"}
			poll := &stubSession{threadReads: map[string]map[string]any{threadID: payload}, threadListResult: map[string]any{"data": []any{row}}}
			s.poll, s.pollConnected = poll, true
			// Preserve an already observed terminal result from before the metadata
			// bump; compact timing is local observation history, not raw App Server time.
			s.now = func() time.Time { return old }
			if _, err := s.AddChat(ctx, threadID, "Historical chat"); err != nil {
				t.Fatal(err)
			}
			s.now = time.Now

			forum := &fakeSyncForum{nextTopicID: 50}
			s.SetSyncForum(forum)
			s.reconcileSync(ctx)
			s.reconcileSync(ctx)
			if len(forum.creates) != 0 {
				t.Fatalf("known old terminal turn recreated topic from bumped metadata: %#v", forum.creates)
			}
			if nextStatus == "inProgress" {
				row["status"] = "inProgress"
				poll.threadReads[threadID] = syncRunningPayload(threadID, "new-turn")
			} else {
				next := syncCompletedPayload(threadID, "new-turn", "new real result")
				next["thread"].(map[string]any)["turns"].([]any)[0].(map[string]any)["updatedAt"] = time.Now().Unix()
				poll.threadReads[threadID] = next
			}
			s.reconcileSync(ctx)
			s.reconcileSync(ctx)
			if len(forum.creates) != 1 {
				t.Fatalf("new authoritative Desktop turn was not discovered: %#v", forum.creates)
			}
		})
	}
}

func TestBoardDiscoveryKnownTerminalObservationFenceFallbacks(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour).Format(time.RFC3339Nano)
	for _, tc := range []struct {
		name, threadStatus, turnStatus, stamp string
		malformed, missing                    bool
		want                                  bool
	}{
		{name: "known old terminal", threadStatus: "completed", turnStatus: "completed", stamp: old, want: false},
		{name: "recent terminal", threadStatus: "completed", turnStatus: "completed", stamp: now.Format(time.RFC3339Nano), want: true},
		{name: "current active", threadStatus: "inProgress", turnStatus: "completed", stamp: old, want: true},
		{name: "current waiting", threadStatus: "waitingOnApproval", turnStatus: "completed", stamp: old, want: true},
		{name: "snapshot active", threadStatus: "notLoaded", turnStatus: "inProgress", stamp: old, want: true},
		{name: "missing snapshot", threadStatus: "completed", missing: true, want: true},
		{name: "malformed snapshot", threadStatus: "completed", malformed: true, want: true},
		{name: "missing timestamp", threadStatus: "completed", turnStatus: "completed", want: true},
		{name: "malformed timestamp", threadStatus: "completed", turnStatus: "completed", stamp: "invalid", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestService(t)
			ctx := context.Background()
			if !tc.missing {
				compact, _ := json.Marshal(appserver.ThreadReadSnapshot{Thread: model.Thread{ID: "board-chat"}, LatestTurnStatus: tc.turnStatus, LatestTurnUpdatedAt: tc.stamp})
				if tc.malformed {
					compact = []byte(`[]`)
				}
				if err := s.store.UpsertSnapshot(ctx, "board-chat", model.ThreadSnapshotState{CompactJSON: compact}); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.boardDiscoveryActivityRecent(ctx, model.Thread{ID: "board-chat", Status: tc.threadStatus}, now.Add(-24*time.Hour).Unix())
			if err != nil || got != tc.want {
				t.Fatalf("recent=%t err=%v want=%t", got, err, tc.want)
			}
		})
	}
}
