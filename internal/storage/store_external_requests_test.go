package storage

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestIngestExternalLaunchRequestsCommitsRequestsAndCursorTogether(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := model.ExternalLaunchRequest{
		ID: "yandex_messenger:chat:10", Source: "yandex_messenger", ExternalID: "chat:10", Sender: "alice",
		Title: "Request from alice", SafePreview: "do work", Prompt: "do work", CWD: "/project",
		Model: "gpt-5.6-luna", ReasoningEffort: "high",
		Status: model.ExternalLaunchPendingApproval, TelegramTopicID: 77,
		CreatedAt: model.NowString(), UpdatedAt: model.NowString(),
	}

	created, err := store.IngestExternalLaunchRequests(ctx, "yandex_messenger", 12, []model.ExternalLaunchRequest{request})
	if err != nil {
		t.Fatalf("IngestExternalLaunchRequests failed: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	cursor, err := store.GetExternalSourceCursor(ctx, "yandex_messenger")
	if err != nil || cursor != 12 {
		t.Fatalf("cursor = %d, err=%v, want 12", cursor, err)
	}
	got, err := store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || got == nil || got.Prompt != "do work" || got.Model != "gpt-5.6-luna" || got.ReasoningEffort != "high" {
		t.Fatalf("request = %#v, err=%v", got, err)
	}

	created, err = store.IngestExternalLaunchRequests(ctx, "yandex_messenger", 13, []model.ExternalLaunchRequest{request})
	if err != nil || created != 0 {
		t.Fatalf("duplicate ingest created=%d err=%v, want 0", created, err)
	}
	cursor, _ = store.GetExternalSourceCursor(ctx, "yandex_messenger")
	if cursor != 13 {
		t.Fatalf("duplicate cursor = %d, want 13", cursor)
	}
}

func TestOpenDropsLegacyExternalSourceMessageCacheAndPreservesRequests(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:legacy-cache")
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		CREATE TABLE external_source_messages (
			source TEXT NOT NULL, chat_id TEXT NOT NULL, message_id INTEGER NOT NULL,
			sender TEXT, timestamp INTEGER NOT NULL DEFAULT 0, text TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			PRIMARY KEY(source, chat_id, message_id)
		);
		INSERT INTO external_source_messages(source, chat_id, message_id, text, created_at, updated_at)
		VALUES ('yandex_messenger', 'chat', 9, 'Root alert', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	row := store.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='external_source_messages'`)
	var count int
	if err := row.Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy cache table count=%d err=%v, want absent", count, err)
	}
	stored, err := store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || stored == nil {
		t.Fatalf("launch request was not preserved: request=%#v err=%v", stored, err)
	}
}

func TestExternalReplyQueueIsIdempotentAndRetryable(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:reply")
	request.SourceChatID = "chat"
	request.SourceMessageID = 42
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimExternalLaunchRequest(ctx, request.ID); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	if changed, err := store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted, "thread", "turn", "", ""); err != nil || !changed {
		t.Fatalf("complete=%t err=%v", changed, err)
	}
	queued, err := store.QueueExternalReply(ctx, "thread", "turn", "Final answer")
	if err != nil || !queued {
		t.Fatalf("queue=%t err=%v", queued, err)
	}
	if queued, err = store.QueueExternalReply(ctx, "thread", "turn", "Duplicate"); err != nil || queued {
		t.Fatalf("duplicate queue=%t err=%v", queued, err)
	}
	batch, err := store.ClaimExternalReplyBatch(ctx, 10)
	if err != nil || len(batch) != 1 || batch[0].ReplyStatus != model.ExternalReplySending || batch[0].ReplyText != "Final answer" {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	if err := store.FailExternalReply(ctx, request.ID, 1, time.Now().UTC().Add(-time.Second), "temporary", false); err != nil {
		t.Fatal(err)
	}
	batch, err = store.ClaimExternalReplyBatch(ctx, 10)
	if err != nil || len(batch) != 1 || batch[0].ReplyAttempts != 1 {
		t.Fatalf("retry batch=%#v err=%v", batch, err)
	}
	if err := store.CompleteExternalReply(ctx, request.ID, 99); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.GetExternalLaunchRequest(ctx, request.ID)
	if stored.ReplyStatus != model.ExternalReplySent || stored.ReplyMessageID != 99 {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestExternalAckIsDurableRetryableAndRejectedRequestIsNotRendered(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	now := model.NowString()
	request := model.ExternalLaunchRequest{
		ID: "test:chat:rejected", Source: "test", ExternalID: "chat:rejected", Sender: "mallory",
		Title: "Rejected request", Status: model.ExternalLaunchRejectedSender,
		SourceChatID: "chat", SourceMessageID: 42,
		AckStatus: model.ExternalReplyPending, AckText: "owner only", AckAvailableAt: now,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	telegram, err := store.ListExternalLaunchRequestsForTelegram(ctx, 10)
	if err != nil || len(telegram) != 0 {
		t.Fatalf("rejected request leaked to Telegram approval: %#v err=%v", telegram, err)
	}
	auto, err := store.ListExternalLaunchRequestsForAutoStart(ctx, 10)
	if err != nil || len(auto) != 0 {
		t.Fatalf("rejected request leaked to auto-start: %#v err=%v", auto, err)
	}
	batch, err := store.ClaimExternalAckBatch(ctx, 10)
	if err != nil || len(batch) != 1 || batch[0].AckText != "owner only" {
		t.Fatalf("ack batch=%#v err=%v", batch, err)
	}
	if err := store.FailExternalAck(ctx, request.ID, 1, time.Now().UTC().Add(-time.Second), "temporary", false); err != nil {
		t.Fatal(err)
	}
	batch, err = store.ClaimExternalAckBatch(ctx, 10)
	if err != nil || len(batch) != 1 || batch[0].AckAttempts != 1 {
		t.Fatalf("retry ack batch=%#v err=%v", batch, err)
	}
	if err := store.CompleteExternalAck(ctx, request.ID, 99); err != nil {
		t.Fatal(err)
	}
	stored, _ := store.GetExternalLaunchRequest(ctx, request.ID)
	if stored.AckStatus != model.ExternalReplySent || stored.AckMessageID != 99 {
		t.Fatalf("stored=%#v", stored)
	}
}

func TestIngestExternalLaunchRequestsRejectsInvalidBatchWithoutCursorAdvance(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if _, err := store.IngestExternalLaunchRequests(ctx, "yandex_messenger", 5, nil); err != nil {
		t.Fatalf("initial cursor ingest failed: %v", err)
	}
	invalid := model.ExternalLaunchRequest{Source: "yandex_messenger", ExternalID: "chat:11"}
	if _, err := store.IngestExternalLaunchRequests(ctx, "yandex_messenger", 6, []model.ExternalLaunchRequest{invalid}); err == nil {
		t.Fatal("invalid batch succeeded")
	}
	cursor, _ := store.GetExternalSourceCursor(ctx, "yandex_messenger")
	if cursor != 5 {
		t.Fatalf("cursor advanced to %d after invalid batch, want 5", cursor)
	}
}

func TestExternalLaunchRequestTransitionsAreConditional(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:20")
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 20, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}

	claimed, err := store.ClaimExternalLaunchRequest(ctx, request.ID)
	if err != nil || !claimed {
		t.Fatalf("first claim=%t err=%v", claimed, err)
	}
	claimed, err = store.ClaimExternalLaunchRequest(ctx, request.ID)
	if err != nil || claimed {
		t.Fatalf("duplicate claim=%t err=%v, want false", claimed, err)
	}
	dismissed, err := store.DismissExternalLaunchRequest(ctx, request.ID)
	if err != nil || dismissed {
		t.Fatalf("dismiss after claim=%t err=%v, want false", dismissed, err)
	}
}

func TestExternalLaunchRecoveryTransitionsAreConditional(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:recover")
	request.SourceChatID = "chat"
	request.SourceMessageID = 42
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimExternalLaunchRequest(ctx, request.ID); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	if changed, err := store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "telegram_topic", "topic failed"); err != nil || !changed {
		t.Fatalf("fail=%t err=%v", changed, err)
	}

	retried, err := store.RetryExternalLaunchRequest(ctx, request.ID)
	if err != nil || !retried {
		t.Fatalf("retry=%t err=%v", retried, err)
	}
	if retried, err = store.RetryExternalLaunchRequest(ctx, request.ID); err != nil || retried {
		t.Fatalf("duplicate retry=%t err=%v, want false", retried, err)
	}
	stored, err := store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || stored == nil || stored.Status != model.ExternalLaunchStarting || stored.ErrorType != "" || stored.ErrorSummary != "" || stored.ReplyStatus != "" {
		t.Fatalf("retried request=%#v err=%v", stored, err)
	}

	if changed, err := store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchOutcomeUnknown, "thread-maybe", "", "dispatch_unknown", "unknown"); err != nil || !changed {
		t.Fatalf("unknown=%t err=%v", changed, err)
	}
	closed, err := store.CloseExternalLaunchRequest(ctx, request.ID)
	if err != nil || !closed {
		t.Fatalf("close=%t err=%v", closed, err)
	}
	if closed, err = store.CloseExternalLaunchRequest(ctx, request.ID); err != nil || closed {
		t.Fatalf("duplicate close=%t err=%v, want false", closed, err)
	}
	stored, _ = store.GetExternalLaunchRequest(ctx, request.ID)
	if stored.Status != model.ExternalLaunchDismissed {
		t.Fatalf("closed status=%q, want dismissed", stored.Status)
	}
}

func TestCompleteExternalTurnClosesActiveRequest(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:terminal")
	request.SourceChatID = "chat"
	request.SourceMessageID = 42
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := store.ClaimExternalLaunchRequest(ctx, request.ID); !claimed {
		t.Fatal("request was not claimed")
	}
	if changed, err := store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted, "thread", "turn", "", ""); err != nil || !changed {
		t.Fatalf("start=%t err=%v", changed, err)
	}

	changed, err := store.CompleteExternalTurn(ctx, "thread", "turn", model.ExternalLaunchSessionCompleted, "Done")
	if err != nil || !changed {
		t.Fatalf("complete turn=%t err=%v", changed, err)
	}
	if changed, err = store.CompleteExternalTurn(ctx, "thread", "turn", model.ExternalLaunchSessionCompleted, "Duplicate"); err != nil || changed {
		t.Fatalf("duplicate complete=%t err=%v, want false", changed, err)
	}
	stored, err := store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || stored == nil || stored.Status != model.ExternalLaunchSessionCompleted || stored.ReplyStatus != model.ExternalReplyPending || stored.ReplyText != "Done" {
		t.Fatalf("completed request=%#v err=%v", stored, err)
	}
}

func TestExternalDismissAtomicallyQueuesTerminalReply(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:dismiss-reply")
	request.SourceChatID = "chat"
	request.SourceMessageID = 42
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.DismissExternalLaunchRequest(ctx, request.ID); err != nil || !changed {
		t.Fatalf("dismiss=%t err=%v", changed, err)
	}
	stored, err := store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || stored == nil || stored.Status != model.ExternalLaunchDismissed || stored.ReplyStatus != model.ExternalReplyPending || !strings.Contains(stored.ReplyText, "dismissed") {
		t.Fatalf("dismissed request=%#v err=%v", stored, err)
	}
}

func TestExternalLaunchRequestTelegramDeliveryRemainsRetryable(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:21")
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 21, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListExternalLaunchRequestsForTelegram(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%#v err=%v", pending, err)
	}
	if err := store.MarkExternalLaunchRequestTelegramSent(ctx, request.ID, 501, model.ExternalLaunchPendingApproval); err != nil {
		t.Fatal(err)
	}
	pending, err = store.ListExternalLaunchRequestsForTelegram(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("delivered request remained pending: %#v err=%v", pending, err)
	}
	if _, err := store.DismissExternalLaunchRequest(ctx, request.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = store.ListExternalLaunchRequestsForTelegram(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].TelegramMessageID != 501 {
		t.Fatalf("status edit work=%#v err=%v", pending, err)
	}
}

func TestAutoStartTelegramVisibilityAppliesOnlyToNewMarkedRequests(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:auto-visible")
	request.AutoStart = true
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 22, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListExternalLaunchRequestsForTelegram(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("new auto-start visibility=%#v err=%v", pending, err)
	}
	auto, err := store.ListExternalLaunchRequestsForAutoStart(ctx, 10)
	if err != nil || len(auto) != 0 {
		t.Fatalf("auto-start became claimable before Telegram visibility: %#v err=%v", auto, err)
	}
	if err := store.MarkExternalLaunchRequestTelegramSent(ctx, request.ID, 501, model.ExternalLaunchPendingApproval); err != nil {
		t.Fatal(err)
	}
	auto, err = store.ListExternalLaunchRequestsForAutoStart(ctx, 10)
	if err != nil || len(auto) != 1 || auto[0].TelegramMessageID != 501 {
		t.Fatalf("visible auto-start request not claimable: %#v err=%v", auto, err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE external_launch_requests SET telegram_message_id=0, telegram_rendered_status='' WHERE id=?`, request.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = store.ListExternalLaunchRequestsForTelegram(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("historical unmarked auto-start was backfilled: %#v err=%v", pending, err)
	}
}

func TestRecoverStartingExternalLaunchRequestsMarksOutcomeUnknown(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:22")
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 22, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimExternalLaunchRequest(ctx, request.ID); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	changed, err := store.RecoverStartingExternalLaunchRequests(ctx)
	if err != nil || changed != 1 {
		t.Fatalf("recovered=%d err=%v", changed, err)
	}
	stored, _ := store.GetExternalLaunchRequest(ctx, request.ID)
	if stored.Status != model.ExternalLaunchOutcomeUnknown || !strings.Contains(stored.ErrorSummary, "restart") {
		t.Fatalf("recovered request=%#v", stored)
	}
}

func TestRefreshExternalLaunchActionCardsForStartup(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:card:refresh")
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkExternalLaunchRequestTelegramSent(ctx, request.ID, 501, model.ExternalLaunchPendingApproval); err != nil {
		t.Fatal(err)
	}
	if claimed, _ := store.ClaimExternalLaunchRequest(ctx, request.ID); !claimed {
		t.Fatal("request was not claimed")
	}
	if changed, err := store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "test", "failed"); err != nil || !changed {
		t.Fatalf("fail=%t err=%v", changed, err)
	}
	if err := store.MarkExternalLaunchRequestTelegramRendered(ctx, request.ID, 501, model.ExternalLaunchFailed); err != nil {
		t.Fatal(err)
	}

	changed, err := store.RefreshExternalLaunchActionCards(ctx)
	if err != nil || changed != 1 {
		t.Fatalf("refresh=%d err=%v", changed, err)
	}
	stored, _ := store.GetExternalLaunchRequest(ctx, request.ID)
	if stored.Status != model.ExternalLaunchFailed || stored.TelegramRenderedStatus != "" {
		t.Fatalf("refreshed request=%#v", stored)
	}
}

func TestCompleteExternalLaunchRequestRequiresStartingState(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	request := testExternalLaunchRequest("test:chat:23")
	if _, err := store.IngestExternalLaunchRequests(ctx, "test", 23, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted, "thread-1", "turn-1", "", ""); err != nil || changed {
		t.Fatalf("completion before claim=%t err=%v", changed, err)
	}
	if claimed, _ := store.ClaimExternalLaunchRequest(ctx, request.ID); !claimed {
		t.Fatal("claim failed")
	}
	if changed, err := store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted, "thread-1", "turn-1", "", ""); err != nil || !changed {
		t.Fatalf("completion=%t err=%v", changed, err)
	}
	stored, _ := store.GetExternalLaunchRequest(ctx, request.ID)
	if stored.Status != model.ExternalLaunchSessionStarted || stored.ThreadID != "thread-1" || stored.TurnID != "turn-1" {
		t.Fatalf("completed request=%#v", stored)
	}
}

func testExternalLaunchRequest(id string) model.ExternalLaunchRequest {
	now := model.NowString()
	return model.ExternalLaunchRequest{
		ID: id, Source: "test", ExternalID: id, Sender: "alice", Title: "Request from alice",
		SafePreview: "do work", Prompt: "do work", CWD: "/project", Status: model.ExternalLaunchPendingApproval,
		TelegramTopicID: 77, CreatedAt: now, UpdatedAt: now,
	}
}
