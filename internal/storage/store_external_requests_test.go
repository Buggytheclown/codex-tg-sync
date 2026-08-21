package storage

import (
	"context"
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
	if err != nil || got == nil || got.Prompt != "do work" {
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

func TestIngestExternalBatchPersistsThreadRootAndRequestWithCursor(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	now := model.NowString()
	message := model.ExternalSourceMessage{Source: "yandex_messenger", ChatID: "chat", MessageID: 9, Sender: "alert-robot", Timestamp: 123, Text: "Root alert", CreatedAt: now, UpdatedAt: now}
	request := model.ExternalLaunchRequest{
		ID: "yandex_messenger:chat:10", Source: "yandex_messenger", ExternalID: "chat:10", Sender: "alice", Title: "Request from alice",
		Prompt: "inspect", CWD: "/project", Status: model.ExternalLaunchPendingApproval, AutoStart: true,
		SourceChatID: "chat", SourceMessageID: 10, SourceThreadID: 9, CreatedAt: now, UpdatedAt: now,
	}
	created, err := store.IngestExternalBatch(ctx, "yandex_messenger", 5, []model.ExternalSourceMessage{message}, []model.ExternalLaunchRequest{request})
	if err != nil || created != 1 {
		t.Fatalf("IngestExternalBatch created=%d err=%v", created, err)
	}
	root, err := store.ExternalSourceMessage(ctx, "yandex_messenger", "chat", 9)
	if err != nil || root == nil || root.Text != "Root alert" {
		t.Fatalf("root=%#v err=%v", root, err)
	}
	stored, err := store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || stored == nil || !stored.AutoStart || stored.SourceMessageID != 10 || stored.SourceThreadID != 9 {
		t.Fatalf("request=%#v err=%v", stored, err)
	}
	cursor, _ := store.GetExternalSourceCursor(ctx, "yandex_messenger")
	if cursor != 5 {
		t.Fatalf("cursor=%d, want 5", cursor)
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
