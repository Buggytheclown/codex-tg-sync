package storage

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestMarkStaleSyncTopicsForCleanupSelectsOldestSafeWorkToReachLimit(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 11; index++ {
		topicID := int64(11 + index)
		state := model.SyncTurnTerminal
		if topicID == 14 {
			state = model.SyncTurnActive
		}
		if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001,
			TopicID: topicID, ThreadID: "thread-" + string(rune('a'+index)), Rank: index + 1,
			Title: "Topic", TelegramState: model.SyncTopicConnected, ActiveTurnState: state}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSyncTopicDraft(ctx, model.SyncTopicDraft{SessionID: "session-1", ChatID: -1001,
		TopicID: 30, Rank: 12, Title: "Draft", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	old := map[int64]time.Duration{30: -96 * time.Hour, 11: -72 * time.Hour, 12: -48 * time.Hour, 14: -120 * time.Hour}
	for topicID, offset := range old {
		table := "sync_topics"
		if topicID == 30 {
			table = "sync_topic_drafts"
		}
		if _, err := store.db.ExecContext(ctx, "UPDATE "+table+" SET updated_at=? WHERE session_id=? AND topic_id=?",
			base.Add(offset).Format(time.RFC3339Nano), "session-1", topicID); err != nil {
			t.Fatal(err)
		}
	}

	targets, err := store.MarkStaleSyncTopicsForCleanup(ctx, "session-1",
		model.TimeString(base.Add(-24*time.Hour).Format(time.RFC3339Nano)), 10)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, 0, len(targets))
	for _, target := range targets {
		got = append(got, target.TopicID)
	}
	if want := []int64{30, 11}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup targets=%v, want %v", got, want)
	}
	topics, err := store.ListSyncTopics(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	states := map[int64]string{}
	for _, topic := range topics {
		states[topic.TopicID] = topic.TelegramState
	}
	if states[11] != model.SyncTopicCleanup || states[12] != model.SyncTopicConnected || states[14] != model.SyncTopicConnected {
		t.Fatalf("topic states=%v", states)
	}
	drafts, err := store.ListSyncTopicDrafts(ctx, "session-1")
	if err != nil || len(drafts) != 1 || drafts[0].State != model.SyncDraftCleanup {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
}

func TestMarkStaleSyncTopicsForCleanupLeavesSessionOverLimitWithoutEnoughSafeWork(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	for index := 0; index < 12; index++ {
		state := model.SyncTurnActive
		if index == 0 {
			state = model.SyncTurnTerminal
		}
		if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001,
			TopicID: int64(11 + index), ThreadID: "thread-" + string(rune('a'+index)),
			TelegramState: model.SyncTopicConnected, ActiveTurnState: state}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if _, err := store.db.ExecContext(ctx, `UPDATE sync_topics SET updated_at=?`, base.Add(-48*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	targets, err := store.MarkStaleSyncTopicsForCleanup(ctx, "session-1",
		model.TimeString(base.Add(-24*time.Hour).Format(time.RFC3339Nano)), 10)
	if err != nil || len(targets) != 1 || targets[0].TopicID != 11 {
		t.Fatalf("targets=%#v err=%v", targets, err)
	}
}

func TestListAllSyncCleanupTargetsIncludesOlderSessionsAndDrafts(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "old-session", ChatID: -1001,
		TopicID: 11, ThreadID: "old-thread", TelegramState: model.SyncTopicCleanup}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "current-session", ChatID: -1001,
		TopicID: 12, ThreadID: "current-thread", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "other-chat-session", ChatID: -2002,
		TopicID: 14, ThreadID: "other-chat-thread", TelegramState: model.SyncTopicCleanup}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO sync_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, "older-draft-session", -1001, 13, 0, "Draft", "/tmp/project", model.SyncDraftCleanup,
		model.NowString(), model.NowString()); err != nil {
		t.Fatal(err)
	}
	targets, err := store.ListAllSyncCleanupTargets(ctx, -1001)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int64, 0, len(targets))
	for _, target := range targets {
		got = append(got, target.TopicID)
	}
	if want := []int64{11, 13}; !reflect.DeepEqual(got, want) {
		t.Fatalf("cleanup targets=%v, want %v", got, want)
	}
}

func TestSyncStatusTurnMigrationAdoptsExistingRenderedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", Title: "One", TelegramState: model.SyncTopicConnected, StatusMessageID: 777}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.UpsertSnapshot(ctx, "thread-1", model.ThreadSnapshotState{
		LastSeenTurnID: "turn-existing",
		CompactJSON:    []byte(`{"LatestUserMessageFP":"user-existing"}`),
	}); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	topics, err := store.ListSyncTopics(context.Background(), "session-1")
	if err != nil || len(topics) != 1 {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	if topics[0].StatusMessageID != 777 || topics[0].StatusTurnID != "turn-existing" || topics[0].LastUserFP != "user-existing" {
		t.Fatalf("migrated topic=%#v", topics[0])
	}
}

func TestSyncStatusUpdateDoesNotOverwriteConcurrentFinalFingerprint(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSyncTopicFinalDelivery(ctx, "session-1", 11, "final-new"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateSyncTopicStatusDelivery(ctx, "session-1", 11, 100, "turn-1", "render-new"); err != nil {
		t.Fatal(err)
	}
	topic, err := store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	if topic.LastFinalFP != "final-new" || topic.LastRenderFP != "render-new" {
		t.Fatalf("topic=%#v", topic)
	}
}

func TestSyncDispatchPersistsPendingTelegramUserFingerprint(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSyncStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSyncDispatchStateWithTelegramUser(ctx, receipt, model.SyncReceiptDispatched, "turn-1", model.SyncTurnActive, 7, "pending-user-fp"); err != nil {
		t.Fatal(err)
	}
	topic, err := store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	if topic.PendingTelegramTurnID != "turn-1" || topic.PendingTelegramUserFP != "pending-user-fp" {
		t.Fatalf("pending Telegram user state=%#v", topic)
	}
	if err := store.UpdateSyncTopicUserDelivery(ctx, "session-1", 11, "user-item-fp", "", ""); err != nil {
		t.Fatal(err)
	}
	topic, err = store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.LastUserFP != "user-item-fp" || topic.PendingTelegramTurnID != "" || topic.PendingTelegramUserFP != "" {
		t.Fatalf("resolved Telegram user state=%#v err=%v", topic, err)
	}
}

func TestSyncActivationPersistsCurrentState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", Rank: 1, Title: "One", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{"created":1}`, true); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetSyncState(ctx)
	if err != nil || state.State != model.SyncStateActive {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
}

func TestSyncActivationPersistsStateAndControlDeliveryTogether(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	delivery := model.DeliveryQueueItem{
		EventID: "sync-activation:session-1", ChatKey: model.ChatKey(-1001, 0),
		ChatID: -1001, Kind: "sync_activation", Status: model.DeliveryStatusPending,
		PayloadJSON: `{"text":"Sync complete"}`,
	}
	if err := store.FinishSyncActivationWithDelivery(ctx, "session-1", `{"created":1}`, true, delivery); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetSyncState(ctx)
	if err != nil || state.State != model.SyncStateActive || state.ActivationSummaryJSON != `{"created":1}` {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	status, err := store.DeliveryStatusForEvent(ctx, delivery.EventID, delivery.ChatKey)
	if err != nil || status != model.DeliveryStatusPending {
		t.Fatalf("delivery status=%q err=%v", status, err)
	}
}

func TestSyncOffIsLogicalBeforeCleanup(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSyncTopicDraft(ctx, model.SyncTopicDraft{SessionID: "session-1", ChatID: -1001, TopicID: 12, Rank: 2, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	topics, err := store.MarkSyncOff(ctx, "session-1")
	if err != nil || len(topics) != 2 || topics[0].TelegramState != model.SyncTopicCleanup || topics[1].TopicID != 12 || topics[1].TelegramState != model.SyncTopicCleanup {
		t.Fatalf("topics = %#v, err = %v", topics, err)
	}
	state, err := store.GetSyncState(ctx)
	if err != nil || state.State != model.SyncStateOff {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
}

func TestSyncTopicDraftMaterializesExactlyOnce(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	draft := model.SyncTopicDraft{SessionID: "session-1", ChatID: -1001, TopicID: 21, Rank: 1, Title: "New task",
		CWD: "/tmp/project", ProjectName: "Project", DirectoryName: "project"}
	if err := store.CreateSyncTopicDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	claimed, receipt, created, err := store.ClaimSyncTopicDraftMessage(ctx, -1001, 21, 901)
	if err != nil || !created || claimed.State != model.SyncDraftStarting || receipt.State != model.SyncReceiptAccepted || receipt.ThreadID != "" {
		t.Fatalf("claimed=%#v receipt=%#v created=%v err=%v", claimed, receipt, created, err)
	}
	_, duplicate, created, err := store.ClaimSyncTopicDraftMessage(ctx, -1001, 21, 901)
	if err != nil || created || duplicate.State != model.SyncReceiptAccepted {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	if err := store.MaterializeSyncTopicDraft(ctx, claimed, receipt, "thread-new", "First prompt", 7); err != nil {
		t.Fatal(err)
	}
	drafts, err := store.ListSyncTopicDrafts(ctx, "session-1")
	if err != nil || len(drafts) != 0 {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	topic, err := store.GetActiveSyncTopic(ctx, -1001, 21)
	if err != nil || topic == nil || topic.ThreadID != "thread-new" || topic.ActiveTurnState != model.SyncTurnStarting || topic.WriterGeneration != 7 {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	storedReceipt, err := store.GetSyncReceipt(ctx, 21, 901)
	if err != nil || storedReceipt == nil || storedReceipt.ThreadID != "thread-new" {
		t.Fatalf("receipt=%#v err=%v", storedReceipt, err)
	}
}

func TestRecoverSyncWriterMarksStartingDraftUnknown(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSyncTopicDraft(ctx, model.SyncTopicDraft{SessionID: "session-1", ChatID: -1001, TopicID: 21, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ClaimSyncTopicDraftMessage(ctx, -1001, 21, 901); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverSyncWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	drafts, err := store.ListSyncTopicDrafts(ctx, "session-1")
	if err != nil || len(drafts) != 1 || drafts[0].State != model.SyncDraftUnknown {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
}

func TestRecoverSyncStateMakesInterruptedActivationCleanupOnly(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverSyncState(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := store.GetSyncState(ctx)
	if state.State != model.SyncStateOff {
		t.Fatalf("state = %#v", state)
	}
	topics, _ := store.ListSyncTopics(ctx, "session-1")
	if len(topics) != 1 || topics[0].TelegramState != model.SyncTopicCleanup {
		t.Fatalf("topics = %#v", topics)
	}
}

func TestResetSyncOnStartupMakesSessionCleanupOnlyAndPreservesOtherState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.SetState(ctx, "test.non_sync_state", "preserved"); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11,
		ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateSyncTopicDraft(ctx, model.SyncTopicDraft{SessionID: "session-1", ChatID: -1001,
		TopicID: 12, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSyncStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}

	sessionID, err := store.ResetSyncOnStartup(ctx)
	if err != nil || sessionID != "session-1" {
		t.Fatalf("sessionID=%q err=%v", sessionID, err)
	}
	state, err := store.GetSyncState(ctx)
	if err != nil || state.State != model.SyncStateOff {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	topics, err := store.ListSyncTopics(ctx, sessionID)
	if err != nil || len(topics) != 1 || topics[0].TelegramState != model.SyncTopicCleanup ||
		topics[0].ActiveTurnID != "" || topics[0].ActiveTurnState != model.SyncTurnTerminal || topics[0].WriterGeneration != 0 {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	drafts, err := store.ListSyncTopicDrafts(ctx, sessionID)
	if err != nil || len(drafts) != 1 || drafts[0].State != model.SyncDraftCleanup {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	storedReceipt, err := store.GetSyncReceipt(ctx, 11, 501)
	if err != nil || storedReceipt == nil || storedReceipt.State != model.SyncReceiptUnknown {
		t.Fatalf("receipt=%#v err=%v", storedReceipt, err)
	}
	value, err := store.GetState(ctx, "test.non_sync_state")
	if err != nil || value != "preserved" {
		t.Fatalf("non-Sync state=%q err=%v", value, err)
	}
}

func TestSyncMessageReceiptIsUniqueAndCarriesNoPromptBody(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}

	receipt, created, err := store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err != nil || !created || receipt.State != model.SyncReceiptAccepted || receipt.ThreadID != "thread-1" {
		t.Fatalf("receipt=%#v created=%v err=%v", receipt, created, err)
	}
	duplicate, created, err := store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err != nil || created || duplicate.State != model.SyncReceiptAccepted {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	var bodyColumns int
	rows, err := store.db.QueryContext(ctx, `PRAGMA table_info(sync_message_receipts)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &pk); err != nil {
			t.Fatal(err)
		}
		if name == "text" || name == "body" || name == "prompt" {
			bodyColumns++
		}
	}
	if bodyColumns != 0 {
		t.Fatalf("receipt schema stores prompt body in %d column(s)", bodyColumns)
	}
}

func TestSyncDispatchStateUpdateIsGenerationGuarded(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcceptSyncMessage(ctx, -1001, 11, 501); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSyncDispatched(ctx, "session-1", 11, 501, "turn-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSyncTerminal(ctx, "session-1", "thread-1", "turn-1", 6); err == nil {
		t.Fatal("stale generation marked current turn terminal")
	}
	if err := store.MarkSyncTerminal(ctx, "session-1", "thread-1", "turn-1", 7); err != nil {
		t.Fatal(err)
	}
	topic, err := store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.ActiveTurnState != model.SyncTurnTerminal {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}

func TestRecoverSyncWriterStateMarksUnfinishedInputUnknownWithoutReplay(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginSyncActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishSyncActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkSyncStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverSyncWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetSyncReceipt(ctx, 11, 501)
	if err != nil || loaded == nil || loaded.State != model.SyncReceiptUnknown {
		t.Fatalf("receipt=%#v err=%v", loaded, err)
	}
	topic, err := store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.ActiveTurnState != model.SyncTurnUnknown {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}
