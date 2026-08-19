package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestAFCStatusTurnMigrationAdoptsExistingRenderedTurn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", Title: "One", TelegramState: model.AFCTopicConnected, StatusMessageID: 777}); err != nil {
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
	topics, err := store.ListAFCTopics(context.Background(), "session-1")
	if err != nil || len(topics) != 1 {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	if topics[0].StatusMessageID != 777 || topics[0].StatusTurnID != "turn-existing" || topics[0].LastUserFP != "user-existing" {
		t.Fatalf("migrated topic=%#v", topics[0])
	}
}

func TestAFCDispatchPersistsPendingTelegramUserFingerprint(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := store.AcceptAFCMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAFCStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAFCDispatchStateWithTelegramUser(ctx, receipt, model.AFCReceiptDispatched, "turn-1", model.AFCTurnActive, 7, "pending-user-fp"); err != nil {
		t.Fatal(err)
	}
	topic, err := store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || topic == nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	if topic.PendingTelegramTurnID != "turn-1" || topic.PendingTelegramUserFP != "pending-user-fp" {
		t.Fatalf("pending Telegram user state=%#v", topic)
	}
	if err := store.UpdateAFCTopicUserDelivery(ctx, "session-1", 11, "user-item-fp", "", ""); err != nil {
		t.Fatal(err)
	}
	topic, err = store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.LastUserFP != "user-item-fp" || topic.PendingTelegramTurnID != "" || topic.PendingTelegramUserFP != "" {
		t.Fatalf("resolved Telegram user state=%#v err=%v", topic, err)
	}
}

func TestAFCActivationIsIsolatedAndDisablesObserverOnlyOnSuccess(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.SetGlobalObserverTarget(ctx, 10, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", Rank: 1, Title: "One", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{"created":1}`, true); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAFCState(ctx)
	if err != nil || state.State != model.AFCStateActive {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
	_, configured, err := store.GetGlobalObserverTarget(ctx)
	if err != nil || !configured {
		t.Fatalf("observer configured = %v, err = %v", configured, err)
	}
	enabled, _ := store.GetState(ctx, "observer.global_enabled")
	if enabled != "false" {
		t.Fatalf("observer.global_enabled = %q, want false", enabled)
	}
	bound, err := store.ListBoundThreadIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 0 {
		t.Fatalf("legacy bound threads = %v, want AFC isolation", bound)
	}
}

func TestAFCOffIsLogicalBeforeCleanupAndDoesNotRestoreObserver(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAFCTopicDraft(ctx, model.AFCTopicDraft{SessionID: "session-1", ChatID: -1001, TopicID: 12, Rank: 2, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	topics, err := store.MarkAFCOff(ctx, "session-1")
	if err != nil || len(topics) != 2 || topics[0].TelegramState != model.AFCTopicCleanup || topics[1].TopicID != 12 || topics[1].TelegramState != model.AFCTopicCleanup {
		t.Fatalf("topics = %#v, err = %v", topics, err)
	}
	state, err := store.GetAFCState(ctx)
	if err != nil || state.State != model.AFCStateOff {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
	enabled, _ := store.GetState(ctx, "observer.global_enabled")
	if enabled != "false" {
		t.Fatalf("observer.global_enabled = %q, want false", enabled)
	}
}

func TestAFCTopicDraftMaterializesExactlyOnce(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	draft := model.AFCTopicDraft{SessionID: "session-1", ChatID: -1001, TopicID: 21, Rank: 1, Title: "New task",
		CWD: "/tmp/project", ProjectName: "Project", DirectoryName: "project"}
	if err := store.CreateAFCTopicDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	claimed, receipt, created, err := store.ClaimAFCTopicDraftMessage(ctx, -1001, 21, 901)
	if err != nil || !created || claimed.State != model.AFCDraftStarting || receipt.State != model.AFCReceiptAccepted || receipt.ThreadID != "" {
		t.Fatalf("claimed=%#v receipt=%#v created=%v err=%v", claimed, receipt, created, err)
	}
	_, duplicate, created, err := store.ClaimAFCTopicDraftMessage(ctx, -1001, 21, 901)
	if err != nil || created || duplicate.State != model.AFCReceiptAccepted {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	if err := store.MaterializeAFCTopicDraft(ctx, claimed, receipt, "thread-new", "First prompt", 7); err != nil {
		t.Fatal(err)
	}
	drafts, err := store.ListAFCTopicDrafts(ctx, "session-1")
	if err != nil || len(drafts) != 0 {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	topic, err := store.GetActiveAFCTopic(ctx, -1001, 21)
	if err != nil || topic == nil || topic.ThreadID != "thread-new" || topic.ActiveTurnState != model.AFCTurnStarting || topic.WriterGeneration != 7 {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	storedReceipt, err := store.GetAFCReceipt(ctx, 21, 901)
	if err != nil || storedReceipt == nil || storedReceipt.ThreadID != "thread-new" {
		t.Fatalf("receipt=%#v err=%v", storedReceipt, err)
	}
}

func TestRecoverAFCWriterMarksStartingDraftUnknown(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAFCTopicDraft(ctx, model.AFCTopicDraft{SessionID: "session-1", ChatID: -1001, TopicID: 21, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.ClaimAFCTopicDraftMessage(ctx, -1001, 21, 901); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverAFCWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	drafts, err := store.ListAFCTopicDrafts(ctx, "session-1")
	if err != nil || len(drafts) != 1 || drafts[0].State != model.AFCDraftUnknown {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
}

func TestRecoverAFCStateMakesInterruptedActivationCleanupOnly(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverAFCState(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := store.GetAFCState(ctx)
	if state.State != model.AFCStateOff {
		t.Fatalf("state = %#v", state)
	}
	topics, _ := store.ListAFCTopics(ctx, "session-1")
	if len(topics) != 1 || topics[0].TelegramState != model.AFCTopicCleanup {
		t.Fatalf("topics = %#v", topics)
	}
}

func TestResetAFCOnStartupMakesSessionCleanupOnlyAndPreservesOtherState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.SetState(ctx, "test.non_afc_state", "preserved"); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11,
		ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAFCTopicDraft(ctx, model.AFCTopicDraft{SessionID: "session-1", ChatID: -1001,
		TopicID: 12, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := store.AcceptAFCMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAFCStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}

	sessionID, err := store.ResetAFCOnStartup(ctx)
	if err != nil || sessionID != "session-1" {
		t.Fatalf("sessionID=%q err=%v", sessionID, err)
	}
	state, err := store.GetAFCState(ctx)
	if err != nil || state.State != model.AFCStateOff {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	topics, err := store.ListAFCTopics(ctx, sessionID)
	if err != nil || len(topics) != 1 || topics[0].TelegramState != model.AFCTopicCleanup ||
		topics[0].ActiveTurnID != "" || topics[0].ActiveTurnState != model.AFCTurnTerminal || topics[0].WriterGeneration != 0 {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	drafts, err := store.ListAFCTopicDrafts(ctx, sessionID)
	if err != nil || len(drafts) != 1 || drafts[0].State != model.AFCDraftCleanup {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	storedReceipt, err := store.GetAFCReceipt(ctx, 11, 501)
	if err != nil || storedReceipt == nil || storedReceipt.State != model.AFCReceiptUnknown {
		t.Fatalf("receipt=%#v err=%v", storedReceipt, err)
	}
	value, err := store.GetState(ctx, "test.non_afc_state")
	if err != nil || value != "preserved" {
		t.Fatalf("non-AFC state=%q err=%v", value, err)
	}
}

func TestAFCMessageReceiptIsUniqueAndCarriesNoPromptBody(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}

	receipt, created, err := store.AcceptAFCMessage(ctx, -1001, 11, 501)
	if err != nil || !created || receipt.State != model.AFCReceiptAccepted || receipt.ThreadID != "thread-1" {
		t.Fatalf("receipt=%#v created=%v err=%v", receipt, created, err)
	}
	duplicate, created, err := store.AcceptAFCMessage(ctx, -1001, 11, 501)
	if err != nil || created || duplicate.State != model.AFCReceiptAccepted {
		t.Fatalf("duplicate=%#v created=%v err=%v", duplicate, created, err)
	}
	var bodyColumns int
	rows, err := store.db.QueryContext(ctx, `PRAGMA table_info(afc_message_receipts)`)
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

func TestAFCDispatchStateUpdateIsGenerationGuarded(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AcceptAFCMessage(ctx, -1001, 11, 501); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAFCDispatched(ctx, "session-1", 11, 501, "turn-1", 7); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAFCTerminal(ctx, "session-1", "thread-1", "turn-1", 6); err == nil {
		t.Fatal("stale generation marked current turn terminal")
	}
	if err := store.MarkAFCTerminal(ctx, "session-1", "thread-1", "turn-1", 7); err != nil {
		t.Fatal(err)
	}
	topic, err := store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.ActiveTurnState != model.AFCTurnTerminal {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}

func TestRecoverAFCWriterStateMarksUnfinishedInputUnknownWithoutReplay(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := store.AcceptAFCMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAFCStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverAFCWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetAFCReceipt(ctx, 11, 501)
	if err != nil || loaded == nil || loaded.State != model.AFCReceiptUnknown {
		t.Fatalf("receipt=%#v err=%v", loaded, err)
	}
	topic, err := store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.ActiveTurnState != model.AFCTurnUnknown {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}
