package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

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
