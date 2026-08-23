package ymessenger

import (
	"strings"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestRequestsFromUpdatesAcceptsAllowedMentionFromAnyChat(t *testing.T) {
	t.Parallel()
	cfg := FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}, DefaultCWD: "/project", TelegramTopicID: 77}
	updates := []Update{
		{UpdateID: 1, MessageID: 10, From: User{Login: "alice"}, Chat: Chat{ID: "0/0/first"}, Text: "first task", MentionedUsers: []User{{Login: "robot-example", Robot: true}}},
		{UpdateID: 2, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "0/0/second"}, Text: "second task", MentionedUsers: []User{{Login: "robot-example", Robot: true}}},
	}

	requests := RequestsFromUpdates(updates, cfg)
	if len(requests) != 2 {
		t.Fatalf("requests = %#v, want two chats accepted", requests)
	}
	if requests[0].ExternalID != "0/0/first:10" || requests[1].ExternalID != "0/0/second:11" {
		t.Fatalf("external ids = %q, %q", requests[0].ExternalID, requests[1].ExternalID)
	}
}

func TestRequestsFromUpdatesBuildsExplicitReplyContextAndReplyTarget(t *testing.T) {
	t.Parallel()
	cfg := FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}, DefaultCWD: "/project", TelegramTopicID: 77, RequireApproval: true}
	updates := []Update{{
		UpdateID: 1, MessageID: 10, Timestamp: 1_700_000_100,
		From: User{Login: "alice", DisplayName: "Alice"}, Chat: Chat{ID: "0/0/chat", Title: "Alerts", Type: "group"},
		Text: "@robot-example investigate", MentionedUsers: []User{{Login: "robot-example", Robot: true}},
		ReplyToMessage: &Update{MessageID: 9, Timestamp: 1_700_000_000, From: User{Login: "alert-robot", DisplayName: "Alert Robot", Robot: true}, Text: "Alert fired"},
	}}

	requests := RequestsFromUpdates(updates, cfg)
	if len(requests) != 1 {
		t.Fatalf("requests = %#v", requests)
	}
	request := requests[0]
	for _, want := range []string{"Source: Yandex Messenger", "Chat: Alerts", "CONTEXT MESSAGE", "Alert Robot (@alert-robot)", "Alert fired", "USER REQUEST TO BOT", "Alice (@alice)", "@robot-example investigate"} {
		if !strings.Contains(request.Prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, request.Prompt)
		}
	}
	if request.SourceChatID != "0/0/chat" || request.SourceMessageID != 10 || request.SourceThreadID != 0 || request.AutoStart {
		t.Fatalf("request routing = %#v", request)
	}
	if request.AckStatus != model.ExternalReplyPending || !strings.Contains(request.AckText, "Waiting for the owner") {
		t.Fatalf("request acknowledgement = %#v", request)
	}
}

func TestRequestsFromUpdatesUsesHistoryThreadRootAndCanAutoStart(t *testing.T) {
	t.Parallel()
	cfg := FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}, DefaultCWD: "/project", RequireApproval: false}
	update := Update{UpdateID: 2, MessageID: 11, Timestamp: 1_700_000_100, From: User{Login: "alice"}, Chat: Chat{ID: "chat", ThreadID: 9}, Text: "@robot-example investigate", MentionedUsers: []User{{Login: "robot-example"}}}
	roots := map[string]ContextMessage{
		SourceMessageKey("chat", 9): {MessageID: 9, From: User{Login: "alert-robot"}, Timestamp: 1_700_000_000, Text: "Root alert"},
	}
	requests := RequestsFromUpdatesWithRoots([]Update{update}, cfg, roots)
	if len(requests) != 1 || !requests[0].AutoStart || requests[0].SourceThreadID != 9 || !strings.Contains(requests[0].Prompt, "Root alert") {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestRequestsFromUpdatesRepliesToUnauthorizedMentionWithoutLaunch(t *testing.T) {
	t.Parallel()
	cfg := FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}}
	updates := []Update{
		{UpdateID: 1, MessageID: 10, From: User{Login: "mallory"}, Chat: Chat{ID: "chat"}, Text: "task", MentionedUsers: []User{{Login: "robot-example"}}},
		{UpdateID: 2, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "chat"}, Text: "task"},
		{UpdateID: 3, MessageID: 12, From: User{Login: "alice", Robot: true}, Chat: Chat{ID: "chat"}, Text: "task", MentionedUsers: []User{{Login: "robot-example"}}},
		{UpdateID: 4, MessageID: 13, From: User{Login: "alice"}, Chat: Chat{ID: "chat"}, Text: "   ", MentionedUsers: []User{{Login: "robot-example"}}},
	}
	got := RequestsFromUpdates(updates, cfg)
	if len(got) != 1 {
		t.Fatalf("requests = %#v, want one explicit rejection", got)
	}
	request := got[0]
	if request.Status != model.ExternalLaunchRejectedSender || request.Prompt != "" || request.TelegramTopicID != 0 || request.AutoStart {
		t.Fatalf("rejected request could launch Codex: %#v", request)
	}
	if request.AckStatus != model.ExternalReplyPending || !strings.Contains(request.AckText, "No Codex session was started") {
		t.Fatalf("rejected request reply = %#v", request)
	}
}
