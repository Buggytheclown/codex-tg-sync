package daemon

import (
	"context"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestServiceImplementsExternalRequestSink(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	now := model.NowString()
	request := model.ExternalLaunchRequest{
		ID: "test:chat:1", Source: "test", ExternalID: "chat:1", Sender: "alice",
		Title: "Request from alice", Prompt: "do work", Status: model.ExternalLaunchPendingApproval,
		TelegramTopicID: 77, CreatedAt: now, UpdatedAt: now,
	}
	created, err := service.IngestExternalRequests(context.Background(), "test", 4, []model.ExternalLaunchRequest{request})
	if err != nil || created != 1 {
		t.Fatalf("IngestExternalRequests created=%d err=%v", created, err)
	}
	cursor, err := service.ExternalSourceCursor(context.Background(), "test")
	if err != nil || cursor != 4 {
		t.Fatalf("ExternalSourceCursor=%d err=%v, want 4", cursor, err)
	}
}
