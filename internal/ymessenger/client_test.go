package ymessenger

import (
	"context"
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
		_, _ = w.Write([]byte(`{"ok":true,"updates":[{"update_id":8,"message_id":101,"timestamp":123,"from":{"login":"alice","robot":false},"chat":{"id":"0/0/chat"},"text":"@robot-example do work","mentioned_users":[{"login":"robot-example","robot":true}]}]}`))
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
}
