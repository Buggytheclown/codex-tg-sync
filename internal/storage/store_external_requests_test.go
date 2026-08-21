package storage

import (
	"context"
	"testing"

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

func testExternalLaunchRequest(id string) model.ExternalLaunchRequest {
	now := model.NowString()
	return model.ExternalLaunchRequest{
		ID: id, Source: "test", ExternalID: id, Sender: "alice", Title: "Request from alice",
		SafePreview: "do work", Prompt: "do work", CWD: "/project", Status: model.ExternalLaunchPendingApproval,
		TelegramTopicID: 77, CreatedAt: now, UpdatedAt: now,
	}
}
