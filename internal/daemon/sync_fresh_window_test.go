package daemon

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestSyncFreshWindowDiscoversAllPagesInSmallBatches(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncInitialTopicLimit = 5
	ctx := context.Background()
	recent := time.Now().Add(-time.Hour).Unix()
	first, second := []any{}, []any{}
	for index := 0; index < 100; index++ {
		row := map[string]any{"id": fmt.Sprintf("fresh-%03d", index), "title": fmt.Sprintf("Fresh %03d", index),
			"status": "completed", "updatedAt": recent - int64(index)}
		if index < 50 {
			first = append(first, row)
		} else {
			second = append(second, row)
		}
	}
	poll := &pagedThreadListSession{stubSession: &stubSession{}, pages: map[string]map[string]any{
		"": {"data": first, "nextCursor": "page-2"}, "page-2": {"data": second},
	}}
	service.poll, service.pollConnected = poll, true
	forum := &fakeSyncForum{nextTopicID: 10}
	service.SetSyncForum(forum)
	if _, err := service.activateSync(ctx, 123456789); err != nil {
		t.Fatal(err)
	}
	state, _ := service.store.GetSyncState(ctx)
	for pass := 0; pass < 20; pass++ {
		topics, _ := service.store.ListSyncTopics(ctx, state.SessionID)
		created, failures, err := service.discoverSyncThreadsLocked(ctx, state, forum, poll, topics)
		if err != nil || failures != 0 || created > 5 {
			t.Fatalf("pass %d: created=%d failures=%d err=%v", pass, created, failures, err)
		}
	}
	topics, err := service.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil || len(topics) != 100 || len(forum.creates) != 100 {
		t.Fatalf("topics=%d creates=%d err=%v, want all 100 exactly once", len(topics), len(forum.creates), err)
	}
	for index := 0; index < len(poll.cursors); index += 2 {
		if index+1 >= len(poll.cursors) || !reflect.DeepEqual(poll.cursors[index:index+2], []string{"", "page-2"}) {
			t.Fatalf("cursors=%v, want every pass to paginate", poll.cursors)
		}
	}
}

func TestSyncFreshWindowRetriesStatusAfterSkippingOldFinal(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	topic.LastUserFP = ""
	if err := service.store.UpsertSyncTopic(ctx, *topic); err != nil {
		t.Fatal(err)
	}
	payload := syncCompletedPayload("thread-1", "turn-1", "old result")
	payload["thread"].(map[string]any)["updatedAt"] = time.Now().Add(-time.Hour).Unix()
	poll := &stubSession{threadListResult: map[string]any{"data": []any{}},
		threadReads: map[string]map[string]any{"thread-1": payload}}
	service.poll, service.pollConnected = poll, true
	forum := &fakeSyncForum{sendErrAt: 2, sendErr: errors.New("temporary Status failure")}
	service.SetSyncForum(forum)
	service.reconcileSync(ctx)
	forum.sendErrAt, forum.sendErr = 0, nil
	service.reconcileSync(ctx)
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusMessageID == 0 {
		t.Fatalf("topic=%#v err=%v, want retried compact Status", topic, err)
	}
	if len(forum.sends) != 3 || !strings.HasPrefix(forum.sends[2].text, syncStatusHeader) {
		t.Fatalf("sends=%#v, want User, failed Status, retried Status without old Final", forum.sends)
	}
}

func TestSyncFreshWindowIgnoresQueuedSnapshotFromPreviousSession(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	old := appserver.SnapshotFromThreadRead(syncCompletedPayload("thread-1", "turn-1", "old result"))
	if err := service.store.UpsertSnapshot(ctx, "thread-1", appserver.CompactSnapshot(nil, old, time.Now().Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	service.enqueueSyncDelivery("thread-1")
	if !service.takeSyncDelivery("thread-1") {
		t.Fatal("delivery wake was not pending")
	}
	service.runSyncDelivery(ctx, "thread-1")
	if len(forum.sends) != 0 {
		t.Fatalf("sends=%#v, want no replay from a previous Sync session", forum.sends)
	}
	current := appserver.SnapshotFromThreadRead(syncRunningPayload("thread-1", "turn-2"))
	if err := service.store.UpsertSnapshot(ctx, "thread-1", appserver.CompactSnapshot(nil, current, time.Now())); err != nil {
		t.Fatal(err)
	}
	service.runSyncDelivery(ctx, "thread-1")
	if len(forum.sends) == 0 || !strings.HasPrefix(forum.sends[0].text, syncStatusHeader) {
		t.Fatalf("sends=%#v, want current-session snapshot delivered", forum.sends)
	}
}

func TestSyncFreshWindowRecreatesExpiredPassiveTopicOnActivity(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	old := time.Now().Add(-25 * time.Hour).Unix()
	payload := syncRunningPayload("thread-1", "turn-1")
	payload["thread"].(map[string]any)["updatedAt"] = old
	poll := &stubSession{threadListResult: map[string]any{"data": []any{}},
		threadReads: map[string]map[string]any{"thread-1": payload}}
	service.poll, service.pollConnected = poll, true
	forum := &fakeSyncForum{nextTopicID: 20}
	service.SetSyncForum(forum)
	service.reconcileSync(ctx)
	service.cleanupSyncTopics(ctx)
	if !reflect.DeepEqual(forum.deletes, []int64{11}) {
		t.Fatalf("deletes=%v, want expired passive presentation removed", forum.deletes)
	}
	payload["thread"].(map[string]any)["updatedAt"] = time.Now().Unix()
	poll.threadListResult = map[string]any{"data": []any{map[string]any{
		"id": "thread-1", "title": "Fresh again", "updatedAt": time.Now().Unix(), "status": "inProgress",
	}}}
	service.reconcileSync(ctx)
	service.reconcileSync(ctx)
	topic, err := service.store.GetActiveSyncTopicByThread(ctx, "s", "thread-1")
	if err != nil || topic == nil || topic.TopicID != 21 || len(forum.creates) != 1 || topic.StatusMessageID == 0 {
		t.Fatalf("topic=%#v creates=%v err=%v, want one restored presentation", topic, forum.creates, err)
	}
	if len(poll.turnStartCalls) != 0 || len(poll.turnInterruptCalls) != 0 {
		t.Fatal("presentation cleanup or recovery mutated Codex work")
	}
}

func TestSyncFreshWindowUnknownActivationBlocksAutomaticCreateRetry(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	poll := &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "fresh-1", "title": "First", "updatedAt": time.Now().Unix()},
		map[string]any{"id": "fresh-2", "title": "Second", "updatedAt": time.Now().Add(-time.Minute).Unix()},
	}}}
	service.poll, service.pollConnected = poll, true
	forum := &fakeSyncForum{nextTopicID: 10, createErrAt: 1,
		createErr: NewSyncForumFailure(SyncForumFailureUnknown, context.DeadlineExceeded)}
	service.SetSyncForum(forum)
	if _, err := service.activateSync(ctx, 123456789); err != nil {
		t.Fatal(err)
	}
	// Reopen the durable store without starting a new process/Sync session.
	reloaded, err := New(service.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	reloaded.SetSyncForum(forum)
	state, _ := reloaded.store.GetSyncState(ctx)
	for pass := 0; pass < 2; pass++ {
		created, _, err := reloaded.discoverSyncThreadsLocked(ctx, state, forum, poll, nil)
		if err == nil || created != 0 {
			t.Fatalf("created=%d err=%v, want durable pause", created, err)
		}
	}
	if !reflect.DeepEqual(forum.creates, []string{"First"}) {
		t.Fatalf("creates=%v, want no automatic retry after unknown outcome", forum.creates)
	}
	status, err := reloaded.syncStatus(ctx)
	if err != nil || !strings.Contains(status.Text, "Topic creation paused") {
		t.Fatalf("status=%#v err=%v, want visible pause", status, err)
	}
}

func TestSyncFreshWindowDiscoveryStopsAfterUnknownCreate(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	state, _ := service.store.GetSyncState(ctx)
	poll := &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "fresh-1", "title": "First", "updatedAt": time.Now().Unix()},
		map[string]any{"id": "fresh-2", "title": "Second", "updatedAt": time.Now().Add(-time.Minute).Unix()},
	}}}
	forum := &fakeSyncForum{createErrAt: 1, createErr: NewSyncForumFailure(SyncForumFailureUnknown, errors.New("EOF"))}
	for pass := 0; pass < 2; pass++ {
		topics, _ := service.store.ListSyncTopics(ctx, "s")
		created, _, err := service.discoverSyncThreadsLocked(ctx, state, forum, poll, topics)
		if err == nil || created != 0 {
			t.Fatalf("created=%d err=%v, want pause after unknown create", created, err)
		}
	}
	if len(forum.creates) != 1 {
		t.Fatalf("creates=%v, want exactly one attempt", forum.creates)
	}
}

func TestSyncFreshWindowDefinitiveFailuresDoNotStarveLaterChats(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	state, _ := service.store.GetSyncState(ctx)
	rows := []any{}
	for index := 0; index < 6; index++ {
		rows = append(rows, map[string]any{"id": fmt.Sprintf("candidate-%d", index),
			"title": fmt.Sprintf("Candidate %d", index), "updatedAt": time.Now().Unix()})
	}
	poll := &stubSession{threadListResult: map[string]any{"data": rows}}
	forum := &fakeSyncForum{nextTopicID: 20,
		createErr: NewSyncForumFailure(SyncForumFailureDefinitive, errors.New("rejected title"))}
	forum.onCreate = func(title string) {
		forum.createErrAt = 0
		if title != "Candidate 5" {
			forum.createErrAt = len(forum.creates)
		}
	}
	for pass := 0; pass < 2; pass++ {
		topics, _ := service.store.ListSyncTopics(ctx, "s")
		if _, _, err := service.discoverSyncThreadsLocked(ctx, state, forum, poll, topics); err != nil {
			t.Fatal(err)
		}
	}
	if topic, err := service.store.GetActiveSyncTopicByThread(ctx, "s", "candidate-5"); err != nil || topic == nil {
		t.Fatalf("topic=%#v err=%v, want candidates after failed batch to progress", topic, err)
	}
}

func TestSyncFreshWindowSkipsPreActivationFinalButDeliversNewResult(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.BeginSyncActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	if err := service.store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "s", ChatID: -1001,
		TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.FinishSyncActivation(ctx, "s", "{}", true); err != nil {
		t.Fatal(err)
	}
	payload := syncCompletedPayload("thread-1", "turn-1", "old result")
	payload["thread"].(map[string]any)["updatedAt"] = time.Now().Add(-time.Hour).Unix()
	poll := &stubSession{threadListResult: map[string]any{"data": []any{}},
		threadReads: map[string]map[string]any{"thread-1": payload}}
	service.poll, service.pollConnected = poll, true
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	service.reconcileSync(ctx)
	service.reconcileSync(ctx)
	if len(forum.sends) != 2 || !strings.HasPrefix(forum.sends[0].text, syncUserHeader) ||
		!strings.HasPrefix(forum.sends[1].text, syncStatusHeader) {
		t.Fatalf("sends=%#v, want User and compact Status without old Final", forum.sends)
	}
	payload = syncCompletedPayload("thread-1", "turn-2", "new result")
	payload["thread"].(map[string]any)["updatedAt"] = time.Now().Add(time.Second).Unix()
	poll.threadReads["thread-1"] = payload
	service.reconcileSync(ctx)
	service.reconcileSync(ctx)
	finals := 0
	for _, message := range forum.sends {
		if strings.HasPrefix(message.text, syncFinalHeader) {
			finals++
			if !strings.Contains(message.text, "new result") {
				t.Fatalf("unexpected historical Final: %q", message.text)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("finals=%d, want new result delivered once", finals)
	}
}
