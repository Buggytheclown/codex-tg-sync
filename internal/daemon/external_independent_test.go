package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestExternalDispatchDoesNotCallTelegram(t *testing.T) {
	for _, name := range []string{"unavailable", "create failure", "send failure"} {
		t.Run(name, func(t *testing.T) {
			s := activeSyncService(t)
			writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "independent", "cwd": "/project"}}}
			s.liveFactory = func() Session { return writer }
			forum := &fakeSyncForum{createErrAt: 1, createErr: errors.New("lost response"), sendErrAt: 1}
			if name != "unavailable" {
				s.SetSyncForum(forum)
			}
			r := prepareStartingExternalRequest(t, s, "independent:"+name, "work")
			s.dispatchExternalLaunchRequest(context.Background(), r.ID)
			got, err := s.store.GetExternalLaunchRequest(context.Background(), r.ID)
			if err != nil || got.Status != model.ExternalLaunchSessionStarted || got.ThreadID != "independent" || got.TurnID == "" {
				t.Fatalf("request=%#v error=%v", got, err)
			}
			if len(forum.creates) != 0 || len(forum.sends) != 0 || len(writer.turnStartCalls) != 1 {
				t.Fatalf("dispatch touched Telegram or missed turn: %#v %#v %#v", forum.creates, forum.sends, writer.turnStartCalls)
			}
		})
	}
}

func storeExternalRunningSnapshot(t *testing.T, s *Service, id, prompt string) {
	t.Helper()
	current := appserver.SnapshotFromThreadRead(syncRunningPayloadWithUser(id, "started-turn", "u", prompt, ""))
	current.Thread.Title = syncPromptTopicTitle(prompt)
	current.Thread.CWD = "/project"
	if _, _, err := s.persistSyncSnapshot(context.Background(), model.SyncTopic{ThreadID: id}, current); err != nil {
		t.Fatal(err)
	}
}

func TestExternalTelegramRetryDoesNotReplayCodex(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "retry-thread", "cwd": "/project"}}}
	s.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{nextTopicID: 50, createErrAt: 1, createErr: errors.New("lost response")}
	s.SetSyncForum(forum)
	sender := &recordingSender{}
	s.SetSender(sender)
	r := prepareStartingExternalRequest(t, s, "retry:telegram", "work")
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	storeExternalRunningSnapshot(t, s, "retry-thread", r.Prompt)
	s.runSyncDelivery(ctx, "retry-thread")
	s.processExternalLaunchRequests(ctx)
	if len(sender.edits) != 1 {
		t.Fatal(sender.edits)
	}
	token := callbackTokenForButton(sender.edits[0].buttons, "Retry")
	if token == "" {
		t.Fatal("missing retry")
	}
	route, err := s.store.GetCallbackRoute(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.handleExternalLaunchCallback(ctx, -1001, 77, 501, route); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if _, err = s.handleExternalLaunchCallback(ctx, -1001, 77, 501, route); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	if len(forum.creates) != 2 || len(writer.threadStartCalls) != 1 || len(writer.turnStartCalls) != 1 {
		t.Fatalf("create=%v writer=%#v", forum.creates, writer)
	}
	s.runSyncDelivery(ctx, "retry-thread")
	p, err := s.store.ThreadTelegramProjection(ctx, "s", "retry-thread")
	if err != nil || p.State != "connected" {
		t.Fatal(p, err)
	}
}

func TestExternalUnknownTurnDoesNotAdoptUnrelatedLatest(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	r := prepareStartingExternalRequest(t, s, "unknown:unrelated", "work")
	if _, err := s.store.CompleteExternalLaunchRequest(ctx, r.ID, model.ExternalLaunchOutcomeUnknown, "thread", "", "dispatch_unknown", "lost response"); err != nil {
		t.Fatal(err)
	}
	storeExternalRunningSnapshot(t, s, "thread", "unrelated work")
	got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	response, err := s.checkExternalLaunchOutcome(ctx, *got)
	got, _ = s.store.GetExternalLaunchRequest(ctx, r.ID)
	if err != nil || got.Status != model.ExternalLaunchOutcomeUnknown || got.TurnID != "" {
		t.Fatal(got, response, err)
	}
}

func TestExternalRetryReusesThreadAfterDefinitiveTurnRejection(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "same-thread", "cwd": "/project"}}, turnStartErr: errors.New("invalid input"), threadReads: map[string]map[string]any{"same-thread": boardThreadPayload("same-thread", "completed", "", "")}}
	s.liveFactory = func() Session { return writer }
	s.poll = &stubSession{threadReads: writer.threadReads}
	s.pollConnected = true
	r := prepareStartingExternalRequest(t, s, "retry:first-turn", "work")
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if got.Status != model.ExternalLaunchFailed || got.ThreadID != "same-thread" || got.DispatchPhase != model.ExternalDispatchThreadReady {
		t.Fatal(got)
	}
	changed, err := s.store.RetryExternalLaunchRequest(ctx, r.ID)
	if err != nil || !changed {
		t.Fatal(changed, err)
	}
	writer.turnStartErr = nil
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	got, _ = s.store.GetExternalLaunchRequest(ctx, r.ID)
	if got.Status != model.ExternalLaunchSessionStarted || len(writer.threadStartCalls) != 1 || len(writer.turnStartCalls) != 1 {
		t.Fatalf("%#v starts=%v turns=%v", got, writer.threadStartCalls, writer.turnStartCalls)
	}
}

func TestPresentationWaitDoesNotRetainExecutionLock(t *testing.T) {
	s := activeSyncService(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		s.syncMu.Lock()
		_ = s.syncForumCallLocked(func() error { close(entered); <-release; return nil })
		s.syncMu.Unlock()
		close(done)
	}()
	<-entered
	acquired := make(chan struct{})
	go func() {
		s.syncMu.Lock()
		s.taskLaunchMu.Lock()
		s.taskLaunchMu.Unlock()
		s.syncMu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("Telegram IO retained execution lock")
	}
	close(release)
	<-done
}

type blockedExternalThreadSession struct {
	*stubSession
	service          *Service
	entered, release chan struct{}
}

func (w *blockedExternalThreadSession) ThreadStart(ctx context.Context, cwd string, options appserver.ThreadStartOptions) (map[string]any, error) {
	w.service.unlockSyncDispatch(ctx)
	close(w.entered)
	<-w.release
	w.service.lockSyncDispatch(ctx)
	return w.stubSession.ThreadStart(ctx, cwd, options)
}
func TestExternalMoveDuringThreadCreationCancelsOnlyUnsentFirstTurn(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "cancel", Column: model.TaskInbox})
	if err != nil {
		t.Fatal(err)
	}
	w := &blockedExternalThreadSession{stubSession: &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "retained", "cwd": "/project"}}}, service: s, entered: make(chan struct{}), release: make(chan struct{})}
	s.liveFactory = func() Session { return w }
	r := daemonExternalRequest("cancel:thread-wait", "work")
	r.TaskID = task.ID
	r.TriggerEventID = "event:cancel"
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	r.ApprovalPolicy = &policy
	r.ApprovalsReviewer = &reviewer
	r.SandboxMode = &sandbox
	r = prepareStartingExternalRequestRecord(t, s, r)
	done := make(chan struct{})
	go func() { s.dispatchExternalLaunchRequest(ctx, r.ID); close(done) }()
	<-w.entered
	if _, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskDone}); err != nil {
		close(w.release)
		<-done
		t.Fatal(err)
	}
	close(w.release)
	<-done
	got, err := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if err != nil || got.Status != model.ExternalLaunchDismissed || got.ThreadID != "retained" || len(w.turnStartCalls) != 0 {
		t.Fatalf("%#v err=%v turns=%v", got, err, w.turnStartCalls)
	}
}

func TestExternalConnectedTerminalPersistenceFailureRetainsWriter(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "terminal-thread", "cwd": "/project"}}}
	s.liveFactory = func() Session { return writer }
	s.SetSyncForum(&fakeSyncForum{nextTopicID: 70})
	r := prepareStartingExternalRequest(t, s, "terminal:persist", "work")
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	storeExternalRunningSnapshot(t, s, "terminal-thread", r.Prompt)
	s.runSyncDelivery(ctx, "terminal-thread")
	topic, err := s.store.GetActiveSyncTopicByThread(ctx, "s", "terminal-thread")
	if err != nil || topic == nil {
		t.Fatal(topic, err)
	}
	db, err := sql.Open("sqlite", s.cfg.Paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER reject_external_terminal BEFORE UPDATE ON external_launch_requests WHEN NEW.status='session_completed' BEGIN SELECT RAISE(ABORT,'terminal write failed'); END`); err != nil {
		t.Fatal(err)
	}
	state, _ := s.store.GetSyncState(ctx)
	current := appserver.SnapshotFromThreadRead(syncCompletedPayload("terminal-thread", "started-turn", "done"))
	s.syncMu.Lock()
	s.processSyncSnapshotLocked(ctx, state, s.getSyncForum(), *topic, current, "test")
	s.syncMu.Unlock()
	if s.syncWriter.Snapshot().Active != 1 {
		t.Fatal("released writer before terminal was durable")
	}
	if _, err = db.Exec(`DROP TRIGGER reject_external_terminal`); err != nil {
		t.Fatal(err)
	}
	s.syncMu.Lock()
	s.processSyncSnapshotLocked(ctx, state, s.getSyncForum(), *topic, current, "test")
	s.syncMu.Unlock()
	if s.syncWriter.Snapshot().Active != 0 {
		t.Fatal("writer not released after terminal recovery")
	}
}

func TestExternalLaterTurnReleasesOriginalRunWithoutRestart(t *testing.T) {
	for _, connected := range []bool{false, true} {
		t.Run(fmt.Sprint(connected), func(t *testing.T) {
			s := activeSyncService(t)
			ctx := context.Background()
			s.cfg.AppServerMode = "websocket"
			first := "00000000-0000-7000-8000-000000000001"
			later := "00000000-0000-7000-8000-000000000002"
			writer := &boardTurnSession{stubSession: &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "superseded", "cwd": "/project"}}}, turnID: first}
			s.liveFactory = func() Session { return writer }
			s.SetSyncForum(&fakeSyncForum{nextTopicID: 90})
			r := prepareStartingExternalRequest(t, s, "superseded:runtime", "work")
			s.dispatchExternalLaunchRequest(ctx, r.ID)
			var topic *model.SyncTopic
			if connected {
				if err := s.ensureExternalTopic(ctx, "superseded"); err != nil {
					t.Fatal(err)
				}
				topic, _ = s.store.GetActiveSyncTopicByThread(ctx, "s", "superseded")
			}
			current := appserver.SnapshotFromThreadRead(boardThreadPayload("superseded", "inProgress", later, ""))
			state, _ := s.store.GetSyncState(ctx)
			if connected {
				s.syncMu.Lock()
				s.processSyncSnapshotLocked(ctx, state, s.getSyncForum(), *topic, current, "test")
				s.syncMu.Unlock()
			} else {
				s.syncMu.Lock()
				s.processBoardSnapshotLocked(ctx, current)
				s.syncMu.Unlock()
			}
			if connected {
				s.syncMu.Lock()
				s.processSyncSnapshotLocked(ctx, state, s.getSyncForum(), *topic, current, "test")
				s.syncMu.Unlock()
			} else {
				s.syncMu.Lock()
				s.processBoardSnapshotLocked(ctx, current)
				s.syncMu.Unlock()
			}
			got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
			if got.Status != model.ExternalLaunchSessionCompleted || s.syncWriter.Snapshot().Active != 0 {
				t.Fatal(got, s.syncWriter.Snapshot())
			}
		})
	}
}

func TestExternalDetachedLaterTurnReleasesOriginalRun(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	s.cfg.AppServerMode = "websocket"
	first := "00000000-0000-7000-8000-000000000001"
	later := "00000000-0000-7000-8000-000000000002"
	writer := &boardTurnSession{stubSession: &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "detached-later", "cwd": "/project"}}, threadReads: map[string]map[string]any{"detached-later": boardThreadPayload("detached-later", "inProgress", later, "")}}, turnID: first}
	s.liveFactory = func() Session { return writer }
	s.SetSyncForum(&fakeSyncForum{nextTopicID: 100})
	r := prepareStartingExternalRequest(t, s, "detached:later", "work")
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	if err := s.ensureExternalTopic(ctx, "detached-later"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.deactivateSync(ctx); err != nil {
		t.Fatal(err)
	}
	s.syncMu.Lock()
	s.reconcileDetachedSyncWorkLocked(ctx)
	s.syncMu.Unlock()
	got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if got.Status != model.ExternalLaunchSessionCompleted || s.syncWriter.Snapshot().Active != 0 {
		t.Fatal(got, s.syncWriter.Snapshot())
	}
}

type blockedExternalRenderer struct {
	*recordingSender
	entered, release chan struct{}
}

func (b *blockedExternalRenderer) EditMessage(ctx context.Context, chat, topic, id int64, text string, buttons [][]model.ButtonSpec, options model.SendOptions) error {
	close(b.entered)
	<-b.release
	return b.recordingSender.EditMessage(ctx, chat, topic, id, text, buttons, options)
}
func TestExternalSupersessionDoesNotWaitForBlockedRenderer(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	s.cfg.AppServerMode = "websocket"
	first := "00000000-0000-7000-8000-000000000001"
	later := "00000000-0000-7000-8000-000000000002"
	writer := &boardTurnSession{stubSession: &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "render-blocked", "cwd": "/project"}}}, turnID: first}
	s.liveFactory = func() Session { return writer }
	r := prepareStartingExternalRequest(t, s, "render:blocked", "work")
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	renderer := &blockedExternalRenderer{recordingSender: &recordingSender{}, entered: make(chan struct{}), release: make(chan struct{})}
	s.SetSender(renderer)
	done := make(chan struct{})
	go func() { s.processExternalLaunchRequests(ctx); close(done) }()
	<-renderer.entered
	s.syncMu.Lock()
	s.processBoardSnapshotLocked(ctx, appserver.SnapshotFromThreadRead(boardThreadPayload("render-blocked", "inProgress", later, "")))
	s.syncMu.Unlock()
	active := s.syncWriter.Snapshot().Active
	got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	close(renderer.release)
	<-done
	if active != 0 || got.Status != model.ExternalLaunchSessionCompleted {
		t.Fatal(active, got)
	}
}

func TestExternalJournalWriteFailureDoesNotDispatch(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	r := prepareStartingExternalRequest(t, s, "journal:failure", "work")
	db, err := sql.Open("sqlite", s.cfg.Paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER reject_dispatch_journal BEFORE UPDATE ON external_launch_requests WHEN NEW.dispatch_phase='thread_sending' BEGIN SELECT RAISE(ABORT,'journal rejected'); END`); err != nil {
		t.Fatal(err)
	}
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if got.Status != model.ExternalLaunchFailed || got.DispatchPhase != model.ExternalDispatchNotSent || len(writer.threadStartCalls) != 0 || len(writer.turnStartCalls) != 0 {
		t.Fatal(got, writer)
	}
}

func TestClosingUnknownExternalRequestDoesNotPinCompletedWriter(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "closed-known", "cwd": "/project"}}}
	s.liveFactory = func() Session { return writer }
	s.SetSyncForum(&fakeSyncForum{nextTopicID: 110})
	r := prepareStartingExternalRequest(t, s, "closed:known", "work")
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	if err := s.ensureExternalTopic(ctx, "closed-known"); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", s.cfg.Paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE external_launch_requests SET status='outcome_unknown' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.store.CloseExternalLaunchRequest(ctx, r.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	state, _ := s.store.GetSyncState(ctx)
	topic, _ := s.store.GetActiveSyncTopicByThread(ctx, "s", "closed-known")
	s.syncMu.Lock()
	s.processSyncSnapshotLocked(ctx, state, s.getSyncForum(), *topic, appserver.SnapshotFromThreadRead(syncCompletedPayload("closed-known", "started-turn", "done")), "test")
	s.syncMu.Unlock()
	if s.syncWriter.Snapshot().Active != 0 {
		t.Fatal("closed request pinned its completed writer")
	}
}

type externalLaunchCountingForum struct {
	*fakeSyncForum
	ordinary, launch int
}

func (f *externalLaunchCountingForum) CreateSyncTopic(ctx context.Context, title string) (int64, error) {
	f.ordinary++
	return f.fakeSyncForum.CreateSyncTopic(ctx, title)
}
func (f *externalLaunchCountingForum) CreateLaunchTopic(ctx context.Context, title string) (int64, error) {
	f.launch++
	return f.fakeSyncForum.CreateSyncTopic(ctx, title)
}
func TestCompletedExternalTopicCreationRetainsOneAttemptPolicy(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "quick-completion", "cwd": "/project"}}}
	s.liveFactory = func() Session { return writer }
	forum := &externalLaunchCountingForum{fakeSyncForum: &fakeSyncForum{createErrAt: 1, createErr: NewSyncForumFailure(SyncForumFailureRetryable, errors.New("429"))}}
	s.SetSyncForum(forum)
	r := prepareStartingExternalRequest(t, s, "quick:completion", "work")
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	s.syncMu.Lock()
	s.processBoardSnapshotLocked(ctx, appserver.SnapshotFromThreadRead(syncCompletedPayload("quick-completion", "started-turn", "done")))
	s.syncMu.Unlock()
	if err := s.ensureExternalTopic(ctx, "quick-completion"); err == nil {
		t.Fatal("expected 429")
	}
	got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if got.Status != model.ExternalLaunchSessionCompleted || forum.launch != 1 || forum.ordinary != 0 {
		t.Fatal(got, forum.launch, forum.ordinary)
	}
}

// Old terminal cards may predate the terminal-notice ledger. A startup repaint
// must not turn that absence into a new completion event.
func TestLegacyCompletedCardRefreshDoesNotBackfillTerminalNotice(t *testing.T) {
	ctx := context.Background()
	s := newTestService(t)
	s.cfg.SyncGroupID = -1001
	r := daemonExternalRequest("legacy:already-completed", "old completed work")
	if _, err := s.IngestExternalRequests(ctx, "test", 1, []model.ExternalLaunchRequest{r}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.MarkExternalLaunchRequestTelegramSent(ctx, r.ID, 501, model.ExternalLaunchPendingApproval); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.store.ClaimExternalLaunchRequest(ctx, r.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.store.CompleteExternalLaunchRequest(ctx, r.ID, model.ExternalLaunchSessionStarted, "old-thread", "old-turn", "", ""); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.store.CompleteExternalTurn(ctx, "old-thread", "old-turn", model.ExternalLaunchSessionCompleted, "old final"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.store.MarkExternalLaunchRequestTelegramRendered(ctx, r.ID, 501, model.ExternalLaunchSessionCompleted); err != nil {
		t.Fatal(err)
	}
	cfg := s.cfg
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", cfg.Paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"dispatch_phase", "telegram_rendered_fp"} {
		if _, err = db.Exec("ALTER TABLE external_launch_requests DROP COLUMN " + column); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	db.Close()
	upgraded, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	sender := &recordingSender{}
	upgraded.SetSender(sender)
	if _, err = upgraded.store.RefreshExternalLaunchActionCards(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := upgraded.store.GetExternalLaunchRequest(ctx, r.ID)
	if err != nil || stored == nil || stored.TelegramRenderedStatus != model.ExternalLaunchSessionCompleted {
		t.Fatalf("startup lost the already-rendered terminal status: request=%+v err=%v", stored, err)
	}
	upgraded.processExternalLaunchRequests(ctx)
	upgraded.processDeliveryBatch(ctx)
	if len(sender.edits) != 1 {
		t.Fatalf("historical card was not repainted: edits=%d", len(sender.edits))
	}
	if len(sender.messages) != 0 {
		t.Fatalf("startup repaint generated %d historical completion notices", len(sender.messages))
	}
}
