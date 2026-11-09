package daemon

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestManualLaunchPassesBlockedBackgroundBoardRead(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	if _, err := s.taskBoard.AddChat(ctx, "passive-chat", "passive chat"); err != nil {
		t.Fatal(err)
	}
	reader := &priorityReadBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"passive-chat": boardThreadPayload("passive-chat", "completed", "old-turn", "old answer")}}, entered: make(chan struct{}), release: make(chan struct{})}
	s.poll = reader
	s.pollConnected = true
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "new-launch", "cwd": "/project"}}}
	s.liveFactory = func() Session { return writer }
	request := prepareStartingExternalRequest(t, s, "priority:unrelated-launch", "work")
	bgDone := make(chan struct{})
	go func() { s.reconcileSync(ctx); close(bgDone) }()
	<-reader.entered
	done := make(chan struct{})
	go func() { s.dispatchExternalLaunchRequest(ctx, request.ID); close(done) }()
	passed := false
	select {
	case <-done:
		passed = true
	case <-time.After(time.Second):
	}
	close(reader.release)
	<-bgDone
	<-done
	if !passed {
		t.Fatal("manual launch waited for unrelated background IO")
	}
	result, err := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || result.Status != model.ExternalLaunchSessionStarted || result.TurnID == "" {
		t.Fatal(result, err)
	}
}

type priorityReadBarrier struct {
	*stubSession
	entered, release chan struct{}
	once             sync.Once
}

func (r *priorityReadBarrier) ThreadRead(ctx context.Context, id string, turns bool) (map[string]any, error) {
	r.once.Do(func() {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	})
	return r.stubSession.ThreadRead(ctx, id, turns)
}

func TestManualLaunchPassesBackgroundSyncIO(t *testing.T) {
	for _, kind := range []string{"read", "list", "resume", "unsubscribe"} {
		t.Run(kind, func(t *testing.T) {
			s := activeSyncService(t)
			ctx := context.Background()
			s.cfg.AppServerMode = "websocket"
			payload := syncCompletedPayload("thread-1", "old-turn", "old answer")
			if kind == "resume" {
				payload = syncRunningPayload("thread-1", "old-turn")
			}
			reader := &priorityRPCBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"thread-1": payload}}, kind: kind, entered: make(chan struct{}), release: make(chan struct{})}
			s.poll = reader
			s.pollConnected = true
			s.pollGeneration = 1
			s.syncSubscribedPollGeneration = 1
			if kind == "unsubscribe" {
				s.syncSubscribedThreads = map[string]bool{"thread-1": true}
			}
			writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "new-launch", "cwd": "/project"}}}
			s.liveFactory = func() Session { return writer }
			request := prepareStartingExternalRequest(t, s, "priority:"+kind, "work")
			bgDone := make(chan struct{})
			go func() { s.reconcileSync(ctx); close(bgDone) }()
			select {
			case <-reader.entered:
			case <-time.After(3 * time.Second):
				close(reader.release)
				<-bgDone
				t.Fatal("background boundary not reached")
			}
			done := make(chan struct{})
			go func() { s.dispatchExternalLaunchRequest(ctx, request.ID); close(done) }()
			passed := false
			select {
			case <-done:
				passed = true
			case <-time.After(time.Second):
			}
			close(reader.release)
			<-bgDone
			<-done
			if !passed {
				t.Fatal("manual dispatch waited for background " + kind)
			}
			r, err := s.store.GetExternalLaunchRequest(ctx, request.ID)
			if err != nil || r.Status != model.ExternalLaunchSessionStarted || r.TurnID == "" {
				t.Fatal(r, err)
			}
		})
	}
}

type priorityRPCBarrier struct {
	*stubSession
	kind             string
	entered, release chan struct{}
	enteredOnce      atomic.Bool
}

func (r *priorityRPCBarrier) wait(ctx context.Context, kind string) {
	if r.kind == kind && r.enteredOnce.CompareAndSwap(false, true) {
		close(r.entered)
		select {
		case <-r.release:
		case <-ctx.Done():
		}
	}
}
func (r *priorityRPCBarrier) ThreadRead(ctx context.Context, id string, turns bool) (map[string]any, error) {
	r.wait(ctx, "read")
	return r.stubSession.ThreadRead(ctx, id, turns)
}
func (r *priorityRPCBarrier) ThreadList(ctx context.Context, limit int, cursor string) (map[string]any, error) {
	r.wait(ctx, "list")
	return r.stubSession.ThreadList(ctx, limit, cursor)
}
func (r *priorityRPCBarrier) ThreadResume(ctx context.Context, id, cwd string) (map[string]any, error) {
	r.wait(ctx, "resume")
	return r.stubSession.ThreadResume(ctx, id, cwd)
}
func (r *priorityRPCBarrier) ThreadUnsubscribe(ctx context.Context, id string) error {
	r.wait(ctx, "unsubscribe")
	return r.stubSession.ThreadUnsubscribe(ctx, id)
}

func TestBackgroundReadCannotCompleteNewManualTurn(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	old := syncCompletedPayload("thread-1", "old-turn", "old answer")
	reader := &priorityRPCBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"thread-1": old}}, kind: "read", entered: make(chan struct{}), release: make(chan struct{})}
	s.poll = reader
	s.pollConnected = true
	writer := &stubSession{threadReads: map[string]map[string]any{"thread-1": old}}
	s.liveFactory = func() Session { return writer }
	r := daemonExternalRequest("priority:same-target", "continue")
	r.TargetThreadID = "thread-1"
	r = prepareStartingExternalRequestRecord(t, s, r)
	bgDone := make(chan struct{})
	go func() { s.reconcileSyncThread(ctx, "thread-1"); close(bgDone) }()
	<-reader.entered
	done := make(chan struct{})
	go func() { s.dispatchExternalLaunchRequest(ctx, r.ID); close(done) }()
	passed := false
	select {
	case <-done:
		passed = true
	case <-time.After(time.Second):
	}
	close(reader.release)
	<-bgDone
	<-done
	if !passed {
		t.Fatal("manual continuation waited for old observation")
	}
	got, err := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if err != nil || got.Status != model.ExternalLaunchSessionStarted || s.syncWriter.Snapshot().Active != 1 {
		t.Fatal(got, err, s.syncWriter.Snapshot())
	}
	snapshot, err := s.store.GetSnapshot(ctx, "thread-1")
	if err != nil || (snapshot != nil && snapshot.LastSeenTurnID == "old-turn") {
		t.Fatal("stale background snapshot was applied", snapshot, err)
	}
}

// Real transport waits on the existing governor; this boundary simulates that
// parked background edit without blocking independent foreground sends.
type priorityCardSender struct {
	*recordingSender
	entered, release chan struct{}
}

func (p *priorityCardSender) EditMessage(ctx context.Context, chat, topic, id int64, text string, buttons [][]model.ButtonSpec, options model.SendOptions) error {
	if options.Background {
		close(p.entered)
		select {
		case <-p.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.recordingSender.EditMessage(ctx, chat, topic, id, text, buttons, options)
}
func TestNewCardPassesParkedBackgroundRenderer(t *testing.T) {
	s := activeSyncService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := prepareStartingExternalRequest(t, s, "old:repaint", "old work")
	if ok, err := s.store.CompleteExternalLaunchRequest(ctx, r.ID, model.ExternalLaunchSessionStarted, "old-thread", "old-turn", "", ""); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.store.CompleteExternalTurn(ctx, "old-thread", "old-turn", model.ExternalLaunchSessionCompleted, "old final"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.store.MarkExternalLaunchRequestTelegramRendered(ctx, r.ID, 501, model.ExternalLaunchSessionCompleted); err != nil {
		t.Fatal(err)
	}
	sender := &priorityCardSender{recordingSender: &recordingSender{}, entered: make(chan struct{}), release: make(chan struct{})}
	s.SetSender(sender)
	// Start only the production render worker path; other loops are irrelevant.
	s.mu.Lock()
	s.started = true
	s.runCtx = ctx
	s.cancel = cancel
	s.mu.Unlock()
	s.processExternalLaunchRequests(ctx)
	select {
	case <-sender.entered:
	case <-time.After(time.Second):
		t.Fatal("background edit did not start")
	}
	fresh := daemonExternalRequest("new:manual-card", "new work")
	if _, err := s.IngestExternalRequests(ctx, "test", 2, []model.ExternalLaunchRequest{fresh}); err != nil {
		cancel()
		t.Fatal(err)
	}
	s.processExternalLaunchRequests(ctx)
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].text, "new work") {
		cancel()
		t.Fatalf("foreground send stuck: %+v", sender.messages)
	}
	close(sender.release)
	s.wg.Wait()
	if len(sender.edits) != 1 || !sender.edits[0].options.Background {
		t.Fatal(sender.edits)
	}
}

func TestManualRecoveryPromotesItsParkedCard(t *testing.T) {
	s := activeSyncService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := prepareStartingExternalRequest(t, s, "old:promote", "old work")
	if ok, err := s.store.CompleteExternalLaunchRequest(ctx, r.ID, model.ExternalLaunchSessionStarted, "old-thread", "old-turn", "", ""); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := s.store.CompleteExternalTurn(ctx, "old-thread", "old-turn", model.ExternalLaunchSessionCompleted, "old final"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.store.MarkExternalLaunchRequestTelegramRendered(ctx, r.ID, 501, model.ExternalLaunchSessionCompleted); err != nil {
		t.Fatal(err)
	}
	sender := &priorityCardSender{recordingSender: &recordingSender{}, entered: make(chan struct{}), release: make(chan struct{})}
	s.SetSender(sender)
	s.mu.Lock()
	s.started = true
	s.runCtx = ctx
	s.cancel = cancel
	s.mu.Unlock()
	s.processExternalLaunchRequests(ctx)
	<-sender.entered
	if err := s.store.PrioritizeExternalTelegram(ctx, r.ID); err != nil {
		cancel()
		t.Fatal(err)
	}
	s.processExternalLaunchRequests(ctx) // Cancels only this request's background wait.
	s.wg.Wait()
	s.processExternalLaunchRequests(ctx)
	s.wg.Wait()
	if len(sender.edits) != 1 || sender.edits[0].options.Background {
		t.Fatal(sender.edits)
	}
	s.processDeliveryBatch(ctx)
	if len(sender.messages) != 0 {
		t.Fatal("manual repaint backfilled a terminal notice", sender.messages)
	}
	got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if got.TelegramPriorityVersion != got.TelegramRenderedPriorityVersion {
		t.Fatal("priority not consumed", got)
	}
}

func TestBackgroundObservationRejectsChangedSessionOrTransport(t *testing.T) {
	for _, change := range []string{"session", "transport"} {
		t.Run(change, func(t *testing.T) {
			s := activeSyncService(t)
			ctx := context.Background()
			reader := &priorityRPCBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"thread-1": syncCompletedPayload("thread-1", "old-turn", "stale answer")}}, kind: "read", entered: make(chan struct{}), release: make(chan struct{})}
			s.poll = reader
			s.pollConnected = true
			done := make(chan struct{})
			go func() { s.reconcileSyncThread(ctx, "thread-1"); close(done) }()
			<-reader.entered
			s.syncMu.Lock()
			if change == "session" {
				if err := s.store.BeginSyncActivation(ctx, "replacement-session", -1001); err != nil {
					s.syncMu.Unlock()
					close(reader.release)
					<-done
					t.Fatal(err)
				}
				if err := s.store.FinishSyncActivation(ctx, "replacement-session", "{}", true); err != nil {
					s.syncMu.Unlock()
					close(reader.release)
					<-done
					t.Fatal(err)
				}
			} else {
				s.mu.Lock()
				s.poll = &stubSession{}
				s.pollGeneration++
				s.mu.Unlock()
			}
			s.syncMu.Unlock()
			close(reader.release)
			<-done
			snapshot, err := s.store.GetSnapshot(ctx, "thread-1")
			if err != nil || (snapshot != nil && snapshot.LastSeenTurnID == "old-turn") {
				t.Fatal("late observation crossed ownership boundary", snapshot, err)
			}
		})
	}
}

func TestBoardSweepDoesNotReadCachedOldPollAfterReconnect(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	for _, id := range []string{"first-passive", "second-passive"} {
		if _, err := s.taskBoard.AddChat(ctx, id, id); err != nil {
			t.Fatal(err)
		}
	}
	reader := &priorityRPCBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{
		"first-passive":  syncCompletedPayload("first-passive", "first-old", "stale"),
		"second-passive": syncCompletedPayload("second-passive", "second-old", "stale"),
	}}, kind: "read", entered: make(chan struct{}), release: make(chan struct{})}
	s.poll = reader
	s.pollConnected = true
	done := make(chan struct{})
	go func() { s.reconcileSync(ctx); close(done) }()
	<-reader.entered
	s.syncMu.Lock()
	s.mu.Lock()
	s.poll = &stubSession{}
	s.pollGeneration++
	s.mu.Unlock()
	s.syncMu.Unlock()
	close(reader.release)
	<-done
	for _, id := range []string{"first-passive", "second-passive"} {
		snapshot, err := s.store.GetSnapshot(ctx, id)
		if err != nil || snapshot != nil {
			t.Fatal("cached old poll crossed reconnect", id, snapshot, err)
		}
	}
}

func TestOldWriterCleanupOrdersSameTargetWithoutBlockingOtherLaunch(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	old := syncCompletedPayload("thread-1", "old-turn", "old final")
	writer := &priorityRPCBarrier{stubSession: &stubSession{threadReads: map[string]map[string]any{"thread-1": old}, threadStartResult: map[string]any{"thread": map[string]any{"id": "other-launch", "cwd": "/project"}}}, kind: "unsubscribe", entered: make(chan struct{}), release: make(chan struct{})}
	s.liveFactory = func() Session { return writer }
	first, err := s.syncWriter.Reserve(ctx, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.syncWriter.MarkActive(first); err != nil {
		t.Fatal(err)
	}
	other, err := s.syncWriter.Reserve(ctx, "keep-writer-alive")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.syncWriter.MarkActive(other); err != nil {
		t.Fatal(err)
	}
	if err = s.syncWriter.MarkTerminal(first); err != nil {
		t.Fatal(err)
	}
	s.syncPendingUnsubscribes = map[string]appserver.WriterLease[Session]{"thread-1": first}
	s.poll = &stubSession{threadReads: map[string]map[string]any{"thread-1": old}}
	s.pollConnected = true
	target := daemonExternalRequest("priority:reuse-cleanup", "continue")
	target.TargetThreadID = "thread-1"
	target = prepareStartingExternalRequestRecord(t, s, target)
	unrelated := prepareStartingExternalRequest(t, s, "priority:other-during-cleanup", "other work")
	cleanup := make(chan struct{})
	go func() { s.syncMu.Lock(); s.retrySyncWriterUnsubscribesLocked(ctx); s.syncMu.Unlock(); close(cleanup) }()
	<-writer.entered
	sameDone := make(chan struct{})
	go func() { s.dispatchExternalLaunchRequest(ctx, target.ID); close(sameDone) }()
	otherDone := make(chan struct{})
	go func() { s.dispatchExternalLaunchRequest(ctx, unrelated.ID); close(otherDone) }()
	passed := false
	select {
	case <-otherDone:
		passed = true
	case <-time.After(time.Second):
	}
	select {
	case <-sameDone:
		t.Error("same target raced an outstanding connection mutation")
	default:
	}
	close(writer.release)
	<-cleanup
	<-sameDone
	<-otherDone
	if !passed {
		t.Fatal("unrelated launch waited for old connection cleanup")
	}
	got, _ := s.store.GetExternalLaunchRequest(ctx, target.ID)
	if got.Status != model.ExternalLaunchSessionStarted || len(writer.threadResumeCalls) != 1 || len(writer.threadUnsubscribeCalls) != 1 {
		t.Fatal(got, writer.threadResumeCalls, writer.threadUnsubscribeCalls)
	}
}

func TestContinuationRevalidatesAdmissionAfterTargetCleanup(t *testing.T) {
	for _, change := range []string{"session", "off", "security"} {
		t.Run(change, func(t *testing.T) {
			s := activeSyncService(t)
			ctx := context.Background()
			process := &stubSession{}
			pending := appserver.WriterLease[Session]{Process: process, ThreadID: "thread-1"}
			released := make(chan struct{})
			s.syncPendingUnsubscribes = map[string]appserver.WriterLease[Session]{"thread-1": pending}
			key := syncSubscriptionOperation{process, "thread-1"}
			s.syncSubscriptionOperations = map[syncSubscriptionOperation]chan struct{}{key: released}
			r := daemonExternalRequest("priority:admission-"+change, "continue")
			r.TargetThreadID = "thread-1"
			r = prepareStartingExternalRequestRecord(t, s, r)
			entered, done := make(chan struct{}), make(chan struct{})
			dispatchCtx := context.WithValue(ctx, syncExternalRequestKey{}, r.ID)
			go func() {
				s.syncMu.Lock()
				s.taskLaunchMu.Lock()
				close(entered)
				s.dispatchExternalContinuationLocked(dispatchCtx, r)
				s.taskLaunchMu.Unlock()
				s.syncMu.Unlock()
				close(done)
			}()
			<-entered
			s.syncMu.Lock() // Acquired only after the waiter released both gates.
			var err error
			if change == "session" {
				err = s.store.BeginSyncActivation(ctx, "replacement", -1001)
				if err == nil {
					err = s.store.FinishSyncActivation(ctx, "replacement", "{}", true)
				}
			} else if change == "off" {
				_, err = s.store.MarkSyncOff(ctx, "s")
			} else {
				db, openErr := sql.Open("sqlite", s.cfg.Paths.DBPath)
				err = openErr
				if err == nil {
					_, err = db.Exec(`UPDATE sync_state SET security_state='invalid'`)
					db.Close()
				}
			}
			delete(s.syncSubscriptionOperations, key)
			delete(s.syncPendingUnsubscribes, "thread-1")
			close(released)
			s.syncMu.Unlock()
			<-done
			if err != nil {
				t.Fatal(err)
			}
			got, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
			if got.Status != model.ExternalLaunchPendingApproval || len(process.threadResumeCalls) != 0 || len(process.turnStartCalls) != 0 {
				t.Fatal("stale admission dispatched", got, process.threadResumeCalls)
			}
		})
	}
}

func TestMaintenanceKeepsVisibleButtonsUntilReplacement(t *testing.T) {
	s := activeSyncService(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := prepareStartingExternalRequest(t, s, "old:buttons", "old work")
	if ok, err := s.store.CompleteExternalLaunchRequest(ctx, r.ID, model.ExternalLaunchFailed, "", "", "test", "old error"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.store.MarkExternalLaunchRequestTelegramRendered(ctx, r.ID, 501, model.ExternalLaunchFailed); err != nil {
		t.Fatal(err)
	}
	original := model.CallbackRoute{Token: "visible-old-button", RequestID: r.ID, ThreadID: r.ID, Action: "external_launch_retry", Status: model.CallbackStatusActive, TelegramMessageID: 501, PayloadJSON: "{}", CreatedAt: model.NowString()}
	if err := s.store.PutCallbackRoute(ctx, original); err != nil {
		t.Fatal(err)
	}
	sender := &priorityCardSender{recordingSender: &recordingSender{}, entered: make(chan struct{}), release: make(chan struct{})}
	s.SetSender(sender)
	s.mu.Lock()
	s.started = true
	s.runCtx = ctx
	s.cancel = cancel
	s.mu.Unlock()
	s.processExternalLaunchRequests(ctx)
	<-sender.entered
	route, err := s.store.GetCallbackRoute(ctx, original.Token)
	visible := err == nil && route != nil && route.Status == model.CallbackStatusActive
	close(sender.release)
	s.wg.Wait()
	if !visible {
		t.Fatal("parked maintenance invalidated the visible manual button", route, err)
	}
	route, err = s.store.GetCallbackRoute(ctx, original.Token)
	if err != nil || route.Status != model.CallbackStatusExpired {
		t.Fatal("successful edit did not replace old route", route, err)
	}
}
