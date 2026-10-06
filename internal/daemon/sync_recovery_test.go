package daemon

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestSyncDetachedRecoveryReadsAuthorityWithoutReplay(t *testing.T) {
	for _, status := range []string{"completed", "running", "superseded"} {
		t.Run(status, func(t *testing.T) {
			service := activeSyncService(t)
			ctx := context.Background()
			forum := &fakeSyncForum{}
			service.SetSyncForum(forum)
			receipt, _, err := service.store.AcceptSyncMessage(ctx, -1001, 11, 501)
			if err != nil {
				t.Fatal(err)
			}
			if err := service.store.MarkSyncStarting(ctx, receipt, 7); err != nil {
				t.Fatal(err)
			}
			if err := service.store.MarkSyncDispatchState(ctx, receipt, model.SyncReceiptDispatched, "old-turn", model.SyncTurnActive, 7); err != nil {
				t.Fatal(err)
			}
			topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
			topic.StatusMessageID = 90
			topic.StatusTurnID = "old-turn"
			if err := service.store.UpsertSyncTopic(ctx, *topic); err != nil {
				t.Fatal(err)
			}
			before, _ := service.store.GetSyncState(ctx)
			if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
				t.Fatal(err)
			}
			payload := syncCompletedPayload("thread-1", "old-turn", "downtime result")
			if status == "running" {
				payload = syncRunningPayload("thread-1", "old-turn")
			}
			if status == "superseded" {
				payload = syncRunningPayload("thread-1", "new-turn")
			}
			poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": payload, "thread-2": syncRunningPayload("thread-2", "other-turn")}}
			service.poll, service.pollConnected = poll, true
			// Ambiguous discovery must not stop existing-thread reads.
			if err := service.store.SetState(ctx, syncTopicCreationKey("s"), "unknown-thread"); err != nil {
				t.Fatal(err)
			}
			service.reconcileSync(ctx)
			service.reconcileSync(ctx)
			after, _ := service.store.GetSyncState(ctx)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("session changed: %#v %#v", before, after)
			}
			topic, _ = service.store.GetActiveSyncTopic(ctx, -1001, 11)
			if topic == nil || topic.ActiveTurnState != model.SyncTurnTerminal || topic.WriterGeneration != 0 || topic.ActiveTurnID != "" {
				t.Fatalf("detached ownership=%#v", topic)
			}
			snapshot, _ := service.store.GetSnapshot(ctx, "thread-1")
			want := "completed"
			if status != "completed" {
				want = "inProgress"
			}
			if !strings.EqualFold(snapshot.LastSeenTurnStatus, want) {
				t.Fatalf("snapshot status=%s want=%s", snapshot.LastSeenTurnStatus, want)
			}
			if len(poll.turnStartCalls)+len(poll.turnSteerCalls)+len(poll.turnInterruptCalls) != 0 || len(forum.creates)+len(forum.deletes) != 0 {
				t.Fatal("recovery mutated remote work or topics")
			}
			if status == "completed" {
				if topic.StatusMessageID != 90 {
					t.Fatalf("Status anchor changed: %#v", topic)
				}
				finals := 0
				for _, msg := range forum.sends {
					if strings.HasPrefix(msg.text, syncFinalHeader) {
						finals++
					}
				}
				if finals != 1 {
					t.Fatalf("finals=%d want one downtime Final", finals)
				}
			}
		})
	}
}

func TestSyncRecoveryFailedReadProtectsTopicFromPrune(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	old := time.Now().Add(-25 * time.Hour).Unix()
	if err := service.store.UpsertThread(ctx, model.Thread{ID: "thread-1", UpdatedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReadErr: errors.New("read unavailable")}
	service.poll, service.pollConnected = poll, true
	service.reconcileSync(ctx)
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if topic == nil || len(forum.deletes) != 0 || !service.syncRecoveryPending["thread-1"] {
		t.Fatalf("failed read lost topic: %#v", topic)
	}
	payload := syncCompletedPayload("thread-1", "turn", "done")
	payload["thread"].(map[string]any)["updatedAt"] = old
	poll.threadReadErr = nil
	poll.threadReads = map[string]map[string]any{"thread-1": payload}
	service.reconcileSync(ctx)
	topics, _ := service.store.ListSyncTopics(ctx, "s")
	if topics[0].TelegramState != model.SyncTopicCleanup || service.syncRecoveryPending["thread-1"] {
		t.Fatalf("retention did not resume: %#v", topics)
	}
}

func TestSyncRecoveryGateRequiresTelegramAndCorrectGroup(t *testing.T) {
	for _, failure := range []string{"not-ready", "group", "control", "changed-group"} {
		t.Run(failure, func(t *testing.T) {
			service := activeSyncService(t)
			ctx := context.Background()
			forum := &fakeSyncForum{}
			service.SetSyncForum(forum)
			service.started = true
			service.syncBotReady = failure != "not-ready"
			defer func() { service.started = false }()
			if failure == "group" {
				forum.validateErr = errors.New("invalid group")
			}
			if failure == "control" {
				forum.prepareErr = errors.New("Control unavailable")
			}
			if failure == "changed-group" {
				service.cfg.SyncGroupID = -1002
			}
			poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncRunningPayload("thread-1", "turn")}}
			service.poll, service.pollConnected = poll, true
			if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := service.reconcileSyncLocked(ctx); err == nil {
				t.Fatal("gate allowed reconciliation")
			}
			response, err := service.handleSyncMessage(ctx, 11, 501, 123456789, "do work")
			if err != nil || response == nil {
				t.Fatalf("response=%#v err=%v", response, err)
			}
			service.runSyncDelivery(ctx, "thread-1")
			if poll.threadReadID != "" || len(poll.turnStartCalls) != 0 || len(forum.sends) != 0 || len(forum.deletes) != 0 {
				t.Fatal("unverified session reached remote IO")
			}
			state, _ := service.store.GetSyncState(ctx)
			if state.State != model.SyncStateActive {
				t.Fatal("gate disabled Sync")
			}
		})
	}
}

func TestSyncRecoveryResumesDrainingWithoutInterruptReplay(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	receipt, _, _ := service.store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err := service.store.MarkSyncStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncDispatchState(ctx, receipt, model.SyncReceiptDispatched, "turn", model.SyncTurnActive, 7); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncDraining(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncRunningPayload("thread-1", "turn"), "thread-2": syncCompletedPayload("thread-2", "other", "done")}}
	service.poll, service.pollConnected = poll, true
	service.reconcileSync(ctx)
	state, _ := service.store.GetSyncState(ctx)
	if state.State != model.SyncStateDraining || service.syncWriter.Snapshot().Accepting {
		t.Fatalf("draining=%#v", state)
	}
	poll.threadReads["thread-1"] = syncCompletedPayload("thread-1", "turn", "done")
	service.reconcileSync(ctx)
	state, _ = service.store.GetSyncState(ctx)
	if state.State != model.SyncStateOff || len(poll.turnInterruptCalls) != 0 {
		t.Fatalf("drain=%#v interrupts=%v", state, poll.turnInterruptCalls)
	}
}

func TestSyncRecoveryOldCallbackCannotReuseGeneration(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	lease, err := service.syncWriter.Reserve(ctx, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	receipt, _, _ := service.store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err := service.store.MarkSyncStarting(ctx, receipt, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncDispatchState(ctx, receipt, model.SyncReceiptDispatched, "turn", model.SyncTurnActive, lease.Generation); err != nil {
		t.Fatal(err)
	}
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	route, _ := service.newSyncCallbackRoute(ctx, *topic, model.PendingApproval{RequestID: "request", Question: "approve?"}, "sync_approval", map[string]any{"decision": "accept"})
	if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.syncWriter.ForceClose(); err != nil {
		t.Fatal(err)
	}
	service.syncWriter = appserver.NewWriterManager("sync", service.threadClaims, func() (Session, error) { return writer, nil })
	fresh, err := service.syncWriter.Reserve(ctx, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Generation != lease.Generation {
		t.Fatal("fixture did not repeat generation")
	}
	service.syncLeases["thread-1"] = fresh
	if err := service.store.ResolveSyncSharedDaemonUnknown(ctx, "s", 11, "thread-1", 0); err != nil {
		t.Fatal(err)
	}
	receipt, _, _ = service.store.AcceptSyncMessage(ctx, -1001, 11, 502)
	if err := service.store.MarkSyncStarting(ctx, receipt, fresh.Generation); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncDispatchState(ctx, receipt, model.SyncReceiptDispatched, "turn", model.SyncTurnActive, fresh.Generation); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleCallback(ctx, -1001, 11, 90, 123456789, route.Token)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "stale") {
		t.Fatalf("old callback revived: %#v %v", response, err)
	}
}

type failingCloseSyncSession struct {
	*stubSession
	closeErr error
}

func (s *failingCloseSyncSession) Close() error { return s.closeErr }

func TestSyncUnknownLiveLeaseStillProjectsFinal(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	lease, err := service.syncWriter.Reserve(ctx, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	service.syncLeases["thread-1"] = lease
	receipt, _, _ := service.store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err := service.store.MarkSyncStarting(ctx, receipt, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation); err != nil {
		t.Fatal(err)
	}
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	state, _ := service.store.GetSyncState(ctx)
	forum := &fakeSyncForum{}
	service.processSyncSnapshotLocked(ctx, state, forum, *topic, appserver.SnapshotFromThreadRead(syncCompletedPayload("thread-1", "turn", "authoritative result")), "sync_poll")
	snapshot, _ := service.store.GetSnapshot(ctx, "thread-1")
	if snapshot == nil || snapshot.LastFinalFP == "" {
		t.Fatal("live uncertainty suppressed passive projection")
	}
	finals := 0
	for _, msg := range forum.sends {
		if strings.HasPrefix(msg.text, syncFinalHeader) {
			finals++
		}
	}
	if finals != 1 {
		t.Fatalf("finals=%d", finals)
	}
	topic, _ = service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if topic.ActiveTurnState != model.SyncTurnUnknown {
		t.Fatal("live unknown was incorrectly treated as detached")
	}
}

func TestSyncUnconfirmedWriterCloseBlocksStopAndRetriesWithoutLosingTopics(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = "websocket"
	ctx := context.Background()
	writer := &failingCloseSyncSession{stubSession: &stubSession{}, closeErr: errors.New("close uncertain")}
	service.liveFactory = func() Session { return writer }
	lease, err := service.syncWriter.Reserve(ctx, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	service.syncLeases["thread-1"] = lease
	receipt, _, _ := service.store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err := service.store.MarkSyncStarting(ctx, receipt, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncDispatchState(ctx, receipt, model.SyncReceiptDispatched, "turn", model.SyncTurnActive, lease.Generation); err != nil {
		t.Fatal(err)
	}
	service.softResetSyncAfterTransportLoss(ctx, errors.New("transport lost"))
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncRunningPayload("thread-1", "turn"), "thread-2": syncRunningPayload("thread-2", "other")}}
	service.poll, service.pollConnected = poll, true
	response, err := service.stopSyncTurn(ctx, 11)
	if err != nil || response == nil || !strings.Contains(response.Text, "unconfirmed") || len(poll.turnInterruptCalls) != 0 {
		t.Fatalf("Stop bypassed gate: %#v %v", response, err)
	}
	service.reconcileSync(ctx)
	state, _ := service.store.GetSyncState(ctx)
	snapshot, _ := service.store.GetSnapshot(ctx, "thread-2")
	if state.State != model.SyncStateActive || snapshot == nil {
		t.Fatal("writer close failure disabled passive topics")
	}
	repair, err := service.handleSyncMessage(ctx, 1, 700, 123456789, "/repair@test_bot")
	if err != nil || repair == nil || !strings.Contains(repair.Text, "Repair requested") {
		t.Fatalf("repair blocked: %#v %v", repair, err)
	}
	writer.closeErr = nil
	service.reconcileSync(ctx)
	if service.syncWriter.Snapshot().State != appserver.WriterStopped || !service.syncWriter.Snapshot().Accepting {
		t.Fatal("confirmed close did not reenable active writer")
	}
}

func TestSyncResumedDrainingRepeatedOffWaitsForAuthoritativeTerminal(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	service.cfg.RequestTimeout = 25 * time.Millisecond
	if err := service.store.MarkSyncDraining(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncRunningPayload("thread-1", "turn"), "thread-2": syncCompletedPayload("thread-2", "other", "done")}}
	service.poll, service.pollConnected = poll, true
	service.reconcileSync(ctx)
	response, err := service.forceDeactivateSync(ctx)
	if err != nil || response == nil || !strings.Contains(response.Text, "remains draining") {
		t.Fatalf("force off=%#v %v", response, err)
	}
	response, err = service.deactivateSync(ctx)
	if err != nil || response == nil || !strings.Contains(response.Text, "refused") {
		t.Fatalf("off=%#v %v", response, err)
	}
	state, _ := service.store.GetSyncState(ctx)
	if state.State != model.SyncStateDraining || len(poll.turnInterruptCalls) != 0 {
		t.Fatalf("state=%#v interrupts=%v", state, poll.turnInterruptCalls)
	}
}

func TestSyncRuntimeRecoveryExpiresExistingCardsInValidatedSession(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	service.started = true
	service.syncBotReady = true
	service.syncValidatedSession = "s"
	defer func() { service.started = false }()
	route := model.CallbackRoute{Token: "old", Action: "sync_approval", TelegramMessageID: 90, Status: model.CallbackStatusActive, PayloadJSON: `{"session_id":"s","topic_id":11}`, CreatedAt: model.NowString()}
	if err := service.store.PutCallbackRoute(ctx, route); err != nil {
		t.Fatal(err)
	}
	if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.validateSavedSyncLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.validateSavedSyncLocked(ctx); err != nil {
		t.Fatal(err)
	}
	if len(forum.edits) != 1 || forum.edits[0].messageID != 90 || !strings.Contains(forum.edits[0].text, "Expired") {
		t.Fatalf("card edits=%#v", forum.edits)
	}
	stored, _ := service.store.GetCallbackRoute(ctx, "old")
	if stored.Status != model.CallbackStatusExpired {
		t.Fatal("route remained active")
	}
}

type isolatedReadFailureSyncSession struct{ *stubSession }

func (s *isolatedReadFailureSyncSession) ThreadRead(ctx context.Context, threadID string, turns bool) (map[string]any, error) {
	if threadID == "thread-1" {
		return nil, errors.New("one thread unavailable")
	}
	return s.stubSession.ThreadRead(ctx, threadID, turns)
}

func TestSyncRecoveryOneFailedReadDoesNotBlockOtherTopicFinal(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	receipt, _, _ := service.store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err := service.store.MarkSyncStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := service.recoverSyncRuntimeLocked(ctx); err != nil {
		t.Fatal(err)
	}
	poll := &isolatedReadFailureSyncSession{stubSession: &stubSession{threadReads: map[string]map[string]any{"thread-2": syncCompletedPayload("thread-2", "other", "other topic result")}}}
	service.poll, service.pollConnected = poll, true
	service.reconcileSync(ctx)
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if topic.ActiveTurnState != model.SyncTurnUnknown || !service.syncRecoveryPending["thread-1"] {
		t.Fatal("failed-read topic lost uncertainty")
	}
	finals := 0
	for _, message := range forum.sends {
		if message.topicID == 12 && strings.HasPrefix(message.text, syncFinalHeader) {
			finals++
		}
	}
	if finals != 1 || service.syncRecoveryPending["thread-2"] {
		t.Fatalf("unrelated Final blocked: finals=%d", finals)
	}
}

func TestSyncStartupPendingDoesNotCreateHealthIncident(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	service.started = true
	defer func() { service.started = false }()
	if _, err := service.reconcileSyncLocked(ctx); err == nil {
		t.Fatal("pending startup admitted")
	}
	episode := service.loadHealthEpisode(ctx, "health.sync.resume")
	if episode.Open {
		t.Fatal("ordinary Telegram startup raised a degraded incident")
	}
}
