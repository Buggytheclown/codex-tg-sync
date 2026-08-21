package ymessenger

import "testing"

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

func TestRequestsFromUpdatesRejectsUnauthorizedMissingMentionAndRobotSender(t *testing.T) {
	t.Parallel()
	cfg := FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}}
	updates := []Update{
		{UpdateID: 1, MessageID: 10, From: User{Login: "mallory"}, Chat: Chat{ID: "chat"}, Text: "task", MentionedUsers: []User{{Login: "robot-example"}}},
		{UpdateID: 2, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "chat"}, Text: "task"},
		{UpdateID: 3, MessageID: 12, From: User{Login: "alice", Robot: true}, Chat: Chat{ID: "chat"}, Text: "task", MentionedUsers: []User{{Login: "robot-example"}}},
		{UpdateID: 4, MessageID: 13, From: User{Login: "alice"}, Chat: Chat{ID: "chat"}, Text: "   ", MentionedUsers: []User{{Login: "robot-example"}}},
	}
	if got := RequestsFromUpdates(updates, cfg); len(got) != 0 {
		t.Fatalf("requests = %#v, want none", got)
	}
}
