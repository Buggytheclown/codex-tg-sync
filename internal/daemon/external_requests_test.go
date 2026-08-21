package daemon

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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
	if len(message.buttons) != 1 || len(message.buttons[0]) != 2 || message.buttons[0][0].Text != "Dismiss" || message.buttons[0][1].Text != "Start" {
		t.Fatalf("approval button order=%#v, want Dismiss then Start", message.buttons)
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
	service := activeAFCService(t)
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

func TestDispatchExternalLaunchRequestCreatesAFCThreadTurnAndTopic(t *testing.T) {
	t.Parallel()
	service := activeAFCService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "external-thread", "cwd": "/project"}}}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{nextTopicID: 20}
	service.SetAFCForum(forum)
	request := prepareStartingExternalRequest(t, service, "test:dispatch:1", "run requested task")

	service.dispatchExternalLaunchRequest(context.Background(), request.ID)
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.Status != model.ExternalLaunchSessionStarted || stored.ThreadID != "external-thread" || stored.TurnID != "started-turn" {
		t.Fatalf("dispatched request=%#v", stored)
	}
	if len(writer.threadStartCalls) != 1 || writer.threadStartCalls[0] != "/project" || len(writer.turnStartCalls) != 1 || writer.turnStartCalls[0].message != request.Prompt {
		t.Fatalf("thread starts=%#v turn starts=%#v", writer.threadStartCalls, writer.turnStartCalls)
	}
	userMessages := 0
	for _, sent := range forum.sends {
		if sent.topicID == 21 && sent.text == afcUserHeader+"\n"+request.Prompt {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("initial AFC user messages=%d sends=%#v, want exactly one", userMessages, forum.sends)
	}
	topics, err := service.store.ListAFCTopics(context.Background(), "s")
	if err != nil || len(topics) != 3 || topics[2].ThreadID != "external-thread" || topics[2].TopicID != 21 {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}

	service.dispatchExternalLaunchRequest(context.Background(), request.ID)
	if len(writer.threadStartCalls) != 1 || len(writer.turnStartCalls) != 1 {
		t.Fatalf("duplicate dispatch mutated App Server: starts=%d turns=%d", len(writer.threadStartCalls), len(writer.turnStartCalls))
	}
	userMessages = 0
	for _, sent := range forum.sends {
		if sent.topicID == 21 && sent.text == afcUserHeader+"\n"+request.Prompt {
			userMessages++
		}
	}
	if userMessages != 1 {
		t.Fatalf("duplicate dispatch sent %d initial user messages", userMessages)
	}
}

func TestDispatchExternalLaunchRequestLeavesPendingWhileAFCInactive(t *testing.T) {
	t.Parallel()
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "must-not-start"}}}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	request := prepareStartingExternalRequest(t, service, "test:dispatch:2", "wait for AFC")

	service.dispatchExternalLaunchRequest(context.Background(), request.ID)
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.Status != model.ExternalLaunchPendingApproval || !strings.Contains(stored.ErrorSummary, "AFC") {
		t.Fatalf("inactive request=%#v", stored)
	}
	if len(writer.threadStartCalls) != 0 || len(forum.creates) != 0 {
		t.Fatalf("inactive dispatch mutated state: starts=%#v topics=%#v", writer.threadStartCalls, forum.creates)
	}
}

func TestDispatchExternalLaunchRequestDoesNotReplayAmbiguousThreadStart(t *testing.T) {
	t.Parallel()
	service := activeAFCService(t)
	writer := &stubSession{threadStartErr: io.EOF}
	service.liveFactory = func() Session { return writer }
	service.SetAFCForum(&fakeAFCForum{nextTopicID: 20})
	request := prepareStartingExternalRequest(t, service, "test:dispatch:3", "ambiguous task")

	service.dispatchExternalLaunchRequest(context.Background(), request.ID)
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.Status != model.ExternalLaunchOutcomeUnknown {
		t.Fatalf("ambiguous request=%#v", stored)
	}
	service.dispatchExternalLaunchRequest(context.Background(), request.ID)
	if len(writer.threadStartCalls) != 1 {
		t.Fatalf("ambiguous request replayed %d times", len(writer.threadStartCalls))
	}
}

func TestDispatchExternalLaunchRequestTopicFailureCreatesNoCodexState(t *testing.T) {
	t.Parallel()
	service := activeAFCService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "must-not-start"}}}
	service.liveFactory = func() Session { return writer }
	service.SetAFCForum(&fakeAFCForum{createErrAt: 1})
	request := prepareStartingExternalRequest(t, service, "test:dispatch:4", "topic failure")

	service.dispatchExternalLaunchRequest(context.Background(), request.ID)
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.Status != model.ExternalLaunchFailed || len(writer.threadStartCalls) != 0 {
		t.Fatalf("topic failure request=%#v starts=%#v", stored, writer.threadStartCalls)
	}
}

func TestDispatchExternalLaunchRequestPromptSendFailureCreatesNoCodexState(t *testing.T) {
	t.Parallel()
	service := activeAFCService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "must-not-start"}}}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{nextTopicID: 20, sendErrAt: 1}
	service.SetAFCForum(forum)
	request := prepareStartingExternalRequest(t, service, "test:dispatch:5", "prompt send failure")

	service.dispatchExternalLaunchRequest(context.Background(), request.ID)
	stored, _ := service.store.GetExternalLaunchRequest(context.Background(), request.ID)
	if stored.Status != model.ExternalLaunchFailed || len(writer.threadStartCalls) != 0 {
		t.Fatalf("prompt send failure request=%#v starts=%#v", stored, writer.threadStartCalls)
	}
	if len(forum.deletes) != 1 || forum.deletes[0] != 21 {
		t.Fatalf("deleted topics=%#v, want [21]", forum.deletes)
	}
	drafts, err := service.store.ListAFCTopicDrafts(context.Background(), "s")
	if err != nil || len(drafts) != 0 {
		t.Fatalf("drafts=%#v err=%v, want cleanup", drafts, err)
	}
}

func prepareStartingExternalRequest(t *testing.T, service *Service, id, prompt string) model.ExternalLaunchRequest {
	t.Helper()
	request := daemonExternalRequest(id, prompt)
	ctx := context.Background()
	if _, err := service.IngestExternalRequests(ctx, "test", time.Now().UnixNano(), []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkExternalLaunchRequestTelegramSent(ctx, request.ID, 501, model.ExternalLaunchPendingApproval); err != nil {
		t.Fatal(err)
	}
	if claimed, err := service.store.ClaimExternalLaunchRequest(ctx, request.ID); err != nil || !claimed {
		t.Fatalf("claim=%t err=%v", claimed, err)
	}
	return request
}

func daemonExternalRequest(id, prompt string) model.ExternalLaunchRequest {
	now := model.NowString()
	return model.ExternalLaunchRequest{
		ID: id, Source: "test", ExternalID: id, Sender: "alice", Title: "Request from alice",
		SafePreview: prompt, Prompt: prompt, CWD: "/project", Status: model.ExternalLaunchPendingApproval,
		TelegramTopicID: 77, CreatedAt: now, UpdatedAt: now,
	}
}
