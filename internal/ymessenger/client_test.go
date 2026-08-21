package ymessenger

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientGetUpdatesUsesOAuthTeamAndDecodesWireFormat(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "OAuthTeam secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.URL.Path != "/messages/getUpdates" || r.URL.Query().Get("offset") != "8" || r.URL.Query().Get("limit") != "100" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"ok":true,"updates":[{"update_id":8,"message_id":101,"timestamp":123,"from":{"login":"alice","display_name":"Alice","robot":false},"chat":{"id":"0/0/chat","title":"Alerts","type":"group"},"text":"@robot-example do work","reply_to_message":{"message_id":99,"timestamp":120,"from":{"login":"alert-robot","display_name":"Alert Robot","robot":true},"text":"Alert fired"},"mentioned_users":[{"login":"robot-example","robot":true}]}]}`))
	}))
	defer server.Close()

	client := NewClient("secret", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	updates, err := client.GetUpdates(context.Background(), 8, 100)
	if err != nil {
		t.Fatalf("GetUpdates failed: %v", err)
	}
	if len(updates) != 1 || updates[0].From.Login != "alice" || updates[0].Chat.ID != "0/0/chat" || len(updates[0].MentionedUsers) != 1 {
		t.Fatalf("updates = %#v", updates)
	}
	if updates[0].ReplyToMessage == nil || updates[0].ReplyToMessage.MessageID != 99 || updates[0].ReplyToMessage.Text != "Alert fired" {
		t.Fatalf("reply_to_message = %#v", updates[0].ReplyToMessage)
	}
}

func TestClientGetThreadRootUsesRobotOAuthAndExactTimestampWindow(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "OAuth secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.URL.Path != "/history" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["ChatId"] != "0/0/chat" || body["MinTimestamp"] != float64(98) || body["MaxTimestamp"] != float64(100) || body["Limit"] != float64(10) {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"Chats":[{"Messages":[{"ServerMessage":{"ClientMessage":{"Plain":{"Text":{"MessageText":"Root alert"}}},"ServerMessageInfo":{"Timestamp":99,"From":{"Login":"alert-robot","DisplayName":"Alert Robot","IsRobot":true}}}},{"ServerMessage":{"ClientMessage":{"Plain":{"Text":{"MessageText":"Adjacent"}}},"ServerMessageInfo":{"Timestamp":100,"From":{"DisplayName":"Someone"}}}}]}]}`))
	}))
	defer server.Close()

	client := NewClient("secret", WithHistoryBaseURL(server.URL), WithHTTPClient(server.Client()))
	root, err := client.GetThreadRoot(context.Background(), "0/0/chat", 99)
	if err != nil {
		t.Fatalf("GetThreadRoot failed: %v", err)
	}
	if root == nil || root.MessageID != 99 || root.Timestamp != 99 || root.Text != "Root alert" || root.From.Login != "alert-robot" || root.From.DisplayName != "Alert Robot" || !root.From.Robot {
		t.Fatalf("root = %#v", root)
	}
}

func TestClientGetThreadRootReturnsNilWhenExactMessageIsAbsent(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Chats":[{"Messages":[{"ServerMessage":{"ClientMessage":{"Plain":{"Text":{"MessageText":"Adjacent"}}},"ServerMessageInfo":{"Timestamp":100,"From":{"DisplayName":"Someone"}}}}]}]}`))
	}))
	defer server.Close()

	client := NewClient("secret", WithHistoryBaseURL(server.URL), WithHTTPClient(server.Client()))
	root, err := client.GetThreadRoot(context.Background(), "0/0/chat", 99)
	if err != nil || root != nil {
		t.Fatalf("root=%#v err=%v, want nil root without error", root, err)
	}
}

func TestClientGetThreadRootDoesNotTreatHistoryAPIErrorAsMissingRoot(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"Error":{"Message":"temporary failure"}}`))
	}))
	defer server.Close()

	client := NewClient("secret", WithHistoryBaseURL(server.URL), WithHTTPClient(server.Client()))
	root, err := client.GetThreadRoot(context.Background(), "0/0/chat", 99)
	if err == nil || root != nil {
		t.Fatalf("root=%#v err=%v, want retryable error", root, err)
	}
}

func TestClientSendExternalReplyTargetsInvocation(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "OAuthTeam secret" {
			t.Fatalf("Authorization = %q", got)
		}
		if r.URL.Path != "/messages/sendText" || r.Method != http.MethodPost {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["chat_id"] != "0/0/chat" || body["text"] != "Investigated." || body["reply_message_id"] != float64(101) || body["thread_id"] != float64(99) {
			t.Fatalf("body = %#v", body)
		}
		_, _ = w.Write([]byte(`{"ok":true,"message_id":202}`))
	}))
	defer server.Close()

	client := NewClient("secret", WithBaseURL(server.URL), WithHTTPClient(server.Client()))
	messageID, err := client.SendExternalReply(context.Background(), "0/0/chat", 101, 99, "Investigated.")
	if err != nil || messageID != 202 {
		t.Fatalf("SendExternalReply messageID=%d err=%v", messageID, err)
	}
}
