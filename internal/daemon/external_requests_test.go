package daemon

import (
	"context"
	"errors"
	"strings"
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

func TestExternalLaunchApprovalRendersOnceAndDismissEditsSameMessage(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	service.cfg.ExternalRequestsTopicID = 77
	sender := &recordingSender{}
	service.SetSender(sender)
	request := daemonExternalRequest("test:approval:1", "Please inspect the full request text")
	if _, err := service.IngestExternalRequests(context.Background(), "test", 1, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}

	service.processExternalLaunchRequests(context.Background())
	service.processExternalLaunchRequests(context.Background())
	if len(sender.messages) != 1 {
		t.Fatalf("messages=%#v, want one durable approval", sender.messages)
	}
	message := sender.messages[0]
	if message.chatID != -1001 || message.topicID != 77 || !strings.Contains(message.text, request.Prompt) || !strings.Contains(message.text, request.Sender) {
		t.Fatalf("approval message=%#v", message)
	}
	dismissToken := callbackTokenForButton(message.buttons, "Dismiss")
	if callbackTokenForButton(message.buttons, "Start") == "" || dismissToken == "" {
		t.Fatalf("approval buttons=%#v", message.buttons)
	}

	response, err := service.HandleCallback(context.Background(), -1001, 77, message.messageID, 123456789, dismissToken)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "Dismissed") {
		t.Fatalf("dismiss response=%#v err=%v", response, err)
	}
	if len(sender.edits) != 1 || sender.edits[0].messageID != message.messageID || len(sender.edits[0].buttons) != 0 || !strings.Contains(sender.edits[0].text, "Dismissed") {
		t.Fatalf("dismiss edits=%#v", sender.edits)
	}
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored == nil || stored.Status != model.ExternalLaunchDismissed {
		t.Fatalf("stored request=%#v", stored)
	}
}

func TestExternalLaunchApprovalCallbackFailsClosedAndStartClaimsOnce(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	service.cfg.ExternalRequestsTopicID = 77
	sender := &recordingSender{}
	service.SetSender(sender)
	request := daemonExternalRequest("test:approval:2", "do work")
	if _, err := service.IngestExternalRequests(context.Background(), "test", 2, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	service.processExternalLaunchRequests(context.Background())
	token := callbackTokenForButton(sender.messages[0].buttons, "Start")

	for _, call := range []struct{ chatID, topicID, messageID, userID int64 }{
		{-1001, 77, 1, 999}, {-1002, 77, 1, 123456789}, {-1001, 78, 1, 123456789}, {-1001, 77, 2, 123456789},
	} {
		_, _ = service.HandleCallback(context.Background(), call.chatID, call.topicID, call.messageID, call.userID, token)
	}
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.Status != model.ExternalLaunchPendingApproval {
		t.Fatalf("fail-closed callbacks changed status to %q", stored.Status)
	}

	first, err := service.HandleCallback(context.Background(), -1001, 77, 1, 123456789, token)
	if err != nil || first == nil || !strings.Contains(first.CallbackText, "Starting") {
		t.Fatalf("first start=%#v err=%v", first, err)
	}
	second, err := service.HandleCallback(context.Background(), -1001, 77, 1, 123456789, token)
	if err != nil || second == nil || !strings.Contains(second.CallbackText, "stale") {
		t.Fatalf("duplicate start=%#v err=%v", second, err)
	}
	stored, _ = service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.Status != model.ExternalLaunchStarting {
		t.Fatalf("status=%q, want starting", stored.Status)
	}
}

func TestExternalLaunchApprovalSendAndEditFailuresRemainRetryable(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	service.cfg.ExternalRequestsTopicID = 77
	sender := &recordingSender{sendErr: errors.New("send unavailable")}
	service.SetSender(sender)
	request := daemonExternalRequest("test:approval:3", "do work")
	if _, err := service.IngestExternalRequests(context.Background(), "test", 3, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}

	service.processExternalLaunchRequests(context.Background())
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.TelegramMessageID != 0 {
		t.Fatalf("failed send persisted message id %d", stored.TelegramMessageID)
	}
	sender.sendErr = nil
	service.processExternalLaunchRequests(context.Background())
	if len(sender.messages) != 1 {
		t.Fatalf("retry messages=%#v", sender.messages)
	}
	token := callbackTokenForButton(sender.messages[0].buttons, "Dismiss")
	sender.editErr = errors.New("edit unavailable")
	response, err := service.HandleCallback(context.Background(), -1001, 77, 1, 123456789, token)
	if err != nil || response == nil {
		t.Fatalf("dismiss response=%#v err=%v", response, err)
	}
	stored, _ = service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.TelegramRenderedStatus == stored.Status {
		t.Fatalf("failed edit was marked rendered: %#v", stored)
	}
	sender.editErr = nil
	service.processExternalLaunchRequests(context.Background())
	if len(sender.edits) != 1 || sender.edits[0].messageID != 1 {
		t.Fatalf("retry edits=%#v", sender.edits)
	}
}

func daemonExternalRequest(id, prompt string) model.ExternalLaunchRequest {
	now := model.NowString()
	return model.ExternalLaunchRequest{
		ID: id, Source: "test", ExternalID: id, Sender: "alice", Title: "Request from alice",
		SafePreview: prompt, Prompt: prompt, CWD: "/project", Status: model.ExternalLaunchPendingApproval,
		TelegramTopicID: 77, CreatedAt: now, UpdatedAt: now,
	}
}
