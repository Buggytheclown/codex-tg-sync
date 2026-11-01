package daemon

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func subscriptionPoll(t *testing.T, s *Service) *stubSession {
	t.Helper()
	s.cfg.AppServerMode = "daemon"
	poll := &stubSession{threadReads: map[string]map[string]any{}, threadListResult: map[string]any{"data": []any{}}}
	for _, id := range []string{"thread-1", "thread-2"} {
		poll.threadReads[id] = syncCompletedPayload(id, "turn", "done")
	}
	s.poll, s.pollConnected, s.pollGeneration = poll, true, 7
	return poll
}

func TestSyncIdleTopicsDoNotLoadThreads(t *testing.T) {
	s := activeSyncService(t)
	poll := subscriptionPoll(t, s)
	for i := 3; i <= 100; i++ {
		id := fmt.Sprintf("thread-%d", i)
		if err := s.store.UpsertSyncTopic(context.Background(), model.SyncTopic{SessionID: "s", ChatID: -1001, TopicID: int64(i + 10), ThreadID: id, TelegramState: model.SyncTopicConnected}); err != nil {
			t.Fatal(err)
		}
		poll.threadReads[id] = syncCompletedPayload(id, "turn", "done")
	}
	s.reconcileSync(context.Background())
	s.reconcileSync(context.Background())
	if len(poll.threadResumeCalls) != 0 {
		t.Fatalf("idle chats resumed: %d", len(poll.threadResumeCalls))
	}
}

func TestSyncSubscriptionsFollowActivityAndRetryUnsubscribe(t *testing.T) {
	s := activeSyncService(t)
	poll := subscriptionPoll(t, s)
	ctx := context.Background()
	poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "turn")
	s.reconcileSync(ctx)
	s.reconcileSync(ctx)
	if len(poll.threadResumeCalls) != 1 {
		t.Fatalf("resume calls: %#v", poll.threadResumeCalls)
	}
	poll.threadReads["thread-1"] = syncCompletedPayload("thread-1", "turn", "done")
	poll.threadUnsubscribeErr = errors.New("temporary unsubscribe failure")
	s.reconcileSync(ctx)
	if len(poll.threadUnsubscribeCalls) != 1 {
		t.Fatalf("unsubscribe calls: %#v", poll.threadUnsubscribeCalls)
	}
	if _, ok := s.syncSubscribedThreads["thread-1"]; !ok {
		t.Fatal("failed unsubscribe forgotten")
	}
	poll.threadUnsubscribeErr = nil
	s.reconcileSync(ctx)
	s.reconcileSync(ctx)
	if !reflect.DeepEqual(poll.threadUnsubscribeCalls, []string{"thread-1", "thread-1"}) {
		t.Fatalf("unsubscribe calls: %#v", poll.threadUnsubscribeCalls)
	}
	if len(poll.threadResumeCalls) != 1 {
		t.Fatal("terminal chat was resumed again")
	}
	poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "next-turn")
	s.reconcileSync(ctx)
	if len(poll.threadResumeCalls) != 2 {
		t.Fatal("next desktop turn was not subscribed")
	}
	s.pollGeneration++
	s.reconcileSync(ctx)
	if len(poll.threadResumeCalls) != 3 {
		t.Fatalf("reconnect resumed idle chats: %#v", poll.threadResumeCalls)
	}
}

func TestSyncReadFailureDoesNotRemoveExistingSubscription(t *testing.T) {
	s := activeSyncService(t)
	poll := subscriptionPoll(t, s)
	ctx := context.Background()
	poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "turn")
	s.reconcileSync(ctx)
	poll.threadReadErr = errors.New("read unavailable")
	s.reconcileSync(ctx)
	if len(poll.threadUnsubscribeCalls) != 0 {
		t.Fatalf("unsubscribed on read failure: %#v", poll.threadUnsubscribeCalls)
	}
}

func subscriptionWriter(t *testing.T, s *Service) (*stubSession, appserver.WriterLease[Session]) {
	t.Helper()
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	for _, id := range []string{"writer-a", "writer-b"} {
		lease, err := s.syncWriter.Reserve(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.syncWriter.MarkActive(lease); err != nil {
			t.Fatal(err)
		}
		s.syncLeases[id] = lease
		s.boardContinuationLeases[id] = boardContinuationLease{Lease: lease, TurnID: "turn"}
	}
	return writer, s.syncLeases["writer-a"]
}

func TestSyncWriterUnsubscribeDoesNotCloseOtherTurn(t *testing.T) {
	s := activeSyncService(t)
	writer, _ := subscriptionWriter(t, s)
	s.processBoardSnapshotLocked(context.Background(), appserver.SnapshotFromThreadRead(syncCompletedPayload("writer-a", "turn", "done")))
	if !reflect.DeepEqual(writer.threadUnsubscribeCalls, []string{"writer-a"}) {
		t.Fatalf("unsubscribes: %#v", writer.threadUnsubscribeCalls)
	}
	if writer.closeCalls != 0 || s.syncWriter.Snapshot().Active != 1 {
		t.Fatal("other active writer was closed")
	}
	if _, ok := s.syncLeases["writer-b"]; !ok {
		t.Fatal("other lease lost")
	}
}

func TestSyncWriterUnsubscribeRetryAndReusedThread(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprint(reuse), func(t *testing.T) {
			s := activeSyncService(t)
			writer, _ := subscriptionWriter(t, s)
			ctx := context.Background()
			writer.threadUnsubscribeErr = errors.New("temporary failure")
			s.processBoardSnapshotLocked(ctx, appserver.SnapshotFromThreadRead(syncCompletedPayload("writer-a", "turn", "done")))
			if s.syncWriter.Snapshot().Active != 1 {
				t.Fatal("failed resource cleanup held terminal lease")
			}
			if len(s.syncPendingUnsubscribes) != 1 {
				t.Fatal("failed resource cleanup forgotten")
			}
			writer.threadUnsubscribeErr = nil
			if reuse {
				lease, err := s.syncWriter.Reserve(ctx, "writer-a")
				if err != nil {
					t.Fatal(err)
				}
				if err = s.syncWriter.MarkActive(lease); err != nil {
					t.Fatal(err)
				}
				s.syncLeases["writer-a"] = lease
			}
			s.retrySyncWriterUnsubscribesLocked(ctx)
			want := 2
			if reuse {
				want = 1
			}
			if len(writer.threadUnsubscribeCalls) != want {
				t.Fatalf("stale cleanup touched new turn: %#v", writer.threadUnsubscribeCalls)
			}
			if len(s.syncPendingUnsubscribes) != 0 {
				t.Fatal("cleanup intent not resolved")
			}
		})
	}
}

func TestSyncPendingUnsubscribeDoesNotTouchNewWriterGeneration(t *testing.T) {
	s := activeSyncService(t)
	writer, lease := subscriptionWriter(t, s)
	ctx := context.Background()
	writer.threadUnsubscribeErr = errors.New("temporary failure")
	s.unsubscribeSyncWriterLeaseLocked(ctx, lease)
	if err := s.syncWriter.ForceClose(); err != nil {
		t.Fatal(err)
	}
	next := &stubSession{}
	s.liveFactory = func() Session { return next }
	if err := s.syncWriter.AcceptNewWork(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.syncWriter.Reserve(ctx, "new-thread"); err != nil {
		t.Fatal(err)
	}
	s.retrySyncWriterUnsubscribesLocked(ctx)
	if len(writer.threadUnsubscribeCalls) != 1 || len(next.threadUnsubscribeCalls) != 0 || len(s.syncPendingUnsubscribes) != 0 {
		t.Fatal("old cleanup crossed writer generation")
	}
}

func TestSyncSubscriptionPrunesRemovedTopicsAndOff(t *testing.T) {
	for _, off := range []bool{false, true} {
		t.Run(fmt.Sprint(off), func(t *testing.T) {
			s := activeSyncService(t)
			poll := subscriptionPoll(t, s)
			ctx := context.Background()
			poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "turn")
			s.reconcileSync(ctx)
			if off {
				if _, err := s.store.MarkSyncOff(ctx, "s"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := s.store.DeleteSyncTopic(ctx, "s", 11); err != nil {
					t.Fatal(err)
				}
			}
			s.reconcileSync(ctx)
			if !reflect.DeepEqual(poll.threadUnsubscribeCalls, []string{"thread-1"}) {
				t.Fatalf("orphan subscription: %#v", poll.threadUnsubscribeCalls)
			}
		})
	}
}

type finishingSubscriptionPoll struct{ *stubSession }

func (p *finishingSubscriptionPoll) ThreadResume(ctx context.Context, id, cwd string) (map[string]any, error) {
	result, err := p.stubSession.ThreadResume(ctx, id, cwd)
	p.threadReads[id] = syncCompletedPayload(id, "turn", "finished while subscribing")
	return result, err
}

func TestSyncSubscriptionCatchesCompletionDuringResume(t *testing.T) {
	s := activeSyncService(t)
	poll := subscriptionPoll(t, s)
	poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "turn")
	s.poll = &finishingSubscriptionPoll{poll}
	s.reconcileSync(context.Background())
	if len(poll.threadResumeCalls) != 1 || !reflect.DeepEqual(poll.threadUnsubscribeCalls, []string{"thread-1"}) {
		t.Fatalf("resume=%#v unsubscribe=%#v", poll.threadResumeCalls, poll.threadUnsubscribeCalls)
	}
	snap, err := s.store.GetSnapshot(context.Background(), "thread-1")
	if err != nil || snap == nil || snap.LastSeenTurnStatus != "completed" {
		t.Fatalf("catch-up lost terminal: %#v %v", snap, err)
	}
	if _, ok := s.syncDirtyThreads["thread-1"]; !ok {
		t.Fatal("resume did not schedule catch-up")
	}
}

func TestSyncSubscriptionWaitingAndUnloadedHistory(t *testing.T) {
	for _, status := range []string{"waitingOnApproval", "waitingOnUserInput", "notLoaded", "idle", "unknown"} {
		t.Run(status, func(t *testing.T) {
			s := activeSyncService(t)
			poll := subscriptionPoll(t, s)
			payload := syncRunningPayload("thread-1", "turn")
			payload["thread"].(map[string]any)["status"] = status
			poll.threadReads["thread-1"] = payload
			s.reconcileSync(context.Background())
			want := 0
			if status == "waitingOnApproval" || status == "waitingOnUserInput" {
				want = 1
			}
			if len(poll.threadResumeCalls) != want {
				t.Fatalf("status=%s resumed=%#v", status, poll.threadResumeCalls)
			}
		})
	}
}

func TestSyncShutdownExplicitlyUnsubscribes(t *testing.T) {
	s := activeSyncService(t)
	poll := subscriptionPoll(t, s)
	poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "turn")
	s.reconcileSync(context.Background())
	writer, _ := subscriptionWriter(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(poll.threadUnsubscribeCalls, []string{"thread-1"}) {
		t.Fatalf("shutdown poll unsubscribes: %#v", poll.threadUnsubscribeCalls)
	}
	if len(writer.threadUnsubscribeCalls) != 2 || writer.closeCalls != 1 {
		t.Fatalf("writer unsubscribes=%#v closes=%d", writer.threadUnsubscribeCalls, writer.closeCalls)
	}
}

func TestSyncFailedResumeStillReleasesPossibleSubscription(t *testing.T) {
	s := activeSyncService(t)
	poll := subscriptionPoll(t, s)
	ctx := context.Background()
	poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "turn")
	poll.threadResumeErr = errors.New("request timeout after remote resume")
	s.reconcileSync(ctx)
	if confirmed, ok := s.syncSubscribedThreads["thread-1"]; !ok || confirmed {
		t.Fatal("ambiguous resume was forgotten or marked confirmed")
	}
	poll.threadResumeErr = nil
	s.reconcileSync(ctx)
	if !s.syncSubscribedThreads["thread-1"] {
		t.Fatal("ambiguous resume not retried")
	}
	poll.threadReads["thread-1"] = syncCompletedPayload("thread-1", "turn", "done")
	s.reconcileSync(ctx)
	if !reflect.DeepEqual(poll.threadUnsubscribeCalls, []string{"thread-1"}) {
		t.Fatal("possible subscription leaked")
	}
}

type metadataSubscriptionPoll struct {
	*stubSession
	metadata    map[string]any
	metadataErr error
}

func (p *metadataSubscriptionPoll) ThreadRead(ctx context.Context, id string, turns bool) (map[string]any, error) {
	if !turns && id == "thread-1" {
		return p.metadata, p.metadataErr
	}
	return p.stubSession.ThreadRead(ctx, id, turns)
}
func TestSyncSubscriptionRechecksContradictoryMetadata(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			s := activeSyncService(t)
			poll := subscriptionPoll(t, s)
			ctx := context.Background()
			poll.threadReads["thread-1"] = syncRunningPayload("thread-1", "turn")
			s.reconcileSync(ctx)
			poll.threadReads["thread-1"]["thread"].(map[string]any)["status"] = "idle"
			recheck := &metadataSubscriptionPoll{stubSession: poll, metadata: map[string]any{"thread": map[string]any{"id": "thread-1", "status": map[string]any{"type": "active"}}}}
			if fail {
				recheck.metadataErr = errors.New("metadata unavailable")
			}
			s.poll = recheck
			s.reconcileSync(ctx)
			if len(poll.threadUnsubscribeCalls) != 0 {
				t.Fatal("detached from an active or uncertain runtime")
			}
		})
	}
}

func TestSyncPersistsFinalBeforeReleasingSubscriptions(t *testing.T) {
	for _, board := range []bool{false, true} {
		t.Run(fmt.Sprint(board), func(t *testing.T) {
			s := activeSyncService(t)
			poll := subscriptionPoll(t, s)
			ctx := context.Background()
			var writer *stubSession
			id, turn := "thread-1", "started-turn"
			if board {
				writer, _ = subscriptionWriter(t, s)
				id, turn = "writer-a", "turn"
			} else {
				writer = &stubSession{}
				s.liveFactory = func() Session { return writer }
				response, err := s.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "prompt", 0)
				if err != nil || response == nil || response.TurnID == "" {
					t.Fatalf("dispatch: %#v %v", response, err)
				}
			}
			db, err := sql.Open("sqlite", s.cfg.Paths.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TRIGGER reject_snapshot BEFORE INSERT ON thread_snapshots BEGIN SELECT RAISE(ABORT,'snapshot write failed'); END`); err != nil {
				t.Fatal(err)
			}
			final := syncCompletedPayload(id, turn, "saved final")
			poll.threadReads[id] = final
			process := func() {
				if board {
					s.processBoardSnapshotLocked(ctx, appserver.SnapshotFromThreadRead(final))
				} else {
					s.reconcileSync(ctx)
				}
			}
			process()
			if len(writer.threadUnsubscribeCalls) != 0 {
				t.Fatal("unsubscribed before Final was durable")
			}
			if _, ok := s.syncLeases[id]; !ok {
				t.Fatal("writer released before Final was durable")
			}
			if _, err = db.Exec(`DROP TRIGGER reject_snapshot`); err != nil {
				t.Fatal(err)
			}
			writer.threadUnsubscribeErr = errors.New("cleanup failure must not block Final")
			process()
			snapshot, err := s.store.GetSnapshot(ctx, id)
			if err != nil || snapshot == nil || snapshot.LastSeenTurnStatus != "completed" {
				t.Fatalf("Final missing after recovery: %#v %v", snapshot, err)
			}
			if _, ok := s.syncLeases[id]; ok {
				t.Fatal("resource cleanup held completed lease")
			}
			if !board {
				forum := s.getSyncForum().(*fakeSyncForum)
				count := 0
				for _, send := range forum.sends {
					if strings.Contains(send.text, "saved final") {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("Final deliveries=%d", count)
				}
			}
		})
	}
}

func TestSyncRetriesTerminalLeaseReleaseAfterFinalWasDelivered(t *testing.T) {
	for _, continuation := range []bool{false, true} {
		t.Run(fmt.Sprint(continuation), func(t *testing.T) {
			s := activeSyncService(t)
			poll := subscriptionPoll(t, s)
			ctx := context.Background()
			writer := &stubSession{}
			s.liveFactory = func() Session { return writer }
			response, err := s.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "prompt", 0)
			if err != nil || response == nil || response.TurnID == "" {
				t.Fatalf("dispatch: %#v %v", response, err)
			}
			if continuation {
				lease := s.syncLeases["thread-1"]
				s.boardContinuationLeases["thread-1"] = boardContinuationLease{Lease: lease, TurnID: response.TurnID}
			}
			db, err := sql.Open("sqlite", s.cfg.Paths.DBPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`CREATE TRIGGER reject_terminal BEFORE UPDATE ON sync_topics WHEN NEW.active_turn_state='terminal' BEGIN SELECT RAISE(ABORT,'terminal receipt failure'); END`); err != nil {
				t.Fatal(err)
			}
			poll.threadReads["thread-1"] = syncCompletedPayload("thread-1", "started-turn", "saved final")
			s.reconcileSync(ctx)
			if _, ok := s.syncLeases["thread-1"]; !ok {
				t.Fatal("terminal release failure was ignored")
			}
			if _, err = db.Exec(`DROP TRIGGER reject_terminal`); err != nil {
				t.Fatal(err)
			}
			s.reconcileSync(ctx)
			if _, ok := s.syncLeases["thread-1"]; ok {
				t.Fatal("stable delivery fast path leaked terminal writer")
			}
			count := 0
			for _, send := range s.getSyncForum().(*fakeSyncForum).sends {
				if strings.Contains(send.text, "saved final") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("Final deliveries=%d", count)
			}
			if !reflect.DeepEqual(writer.threadUnsubscribeCalls, []string{"thread-1"}) {
				t.Fatalf("writer unsubscribes=%#v", writer.threadUnsubscribeCalls)
			}
		})
	}
}
