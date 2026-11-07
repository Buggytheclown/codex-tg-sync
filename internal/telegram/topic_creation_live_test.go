package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

type lostCreateResponseTransport struct {
	base    http.RoundTripper
	topicID int64
}

func (t *lostCreateResponseTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if err != nil || !strings.HasSuffix(r.URL.Path, "/createForumTopic") {
		return response, err
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	var envelope struct {
		OK     bool       `json:"ok"`
		Result ForumTopic `json:"result"`
	}
	if err = json.Unmarshal(data, &envelope); err != nil {
		return nil, err
	}
	if !envelope.OK {
		return nil, errors.New("live probe create rejected")
	}
	// Retain the ID solely for cleanup/assertion. The production client receives
	// no response or ID and must recover exclusively through its update stream.
	t.topicID = envelope.Result.MessageThreadID
	return nil, errors.New("intentionally lost create response")
}

func TestLiveLostTopicCreateResponse(t *testing.T) {
	if os.Getenv("CODEX_TG_PROBE_EXCLUSIVE_BOT") != "1" {
		t.Skip("requires a dedicated bot with no other update consumer")
	}
	token := os.Getenv("CODEX_TG_PROBE_BOT_TOKEN")
	group, err := strconv.ParseInt(os.Getenv("CODEX_TG_PROBE_GROUP_ID"), 10, 64)
	if token == "" || err != nil {
		t.Fatal("configure probe bot token and group id")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	client := NewClient(token)
	me, err := client.GetMe(ctx)
	if err != nil {
		t.Fatal(SanitizeLogError(err))
	}
	updates, err := client.GetUpdates(ctx, 0, 0)
	if err != nil {
		t.Fatal(SanitizeLogError(err))
	}
	if len(updates) != 0 {
		t.Fatal("probe bot has pending updates; refusing to consume another workflow")
	}
	base := client.http.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &lostCreateResponseTransport{base: base}
	client.http.Transport = transport
	defer func() {
		if transport.topicID > 0 {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if err := client.DeleteForumTopic(cleanupCtx, group, transport.topicID); err != nil {
				t.Errorf("probe cleanup: %s", SanitizeLogError(err))
			}
		}
	}()
	title := "Sync recovery probe " + time.Now().UTC().Format("20060102 150405.000000000")
	if _, err = client.CreateForumTopic(ctx, group, title); err == nil || transport.topicID <= 0 {
		t.Fatal("failed to inject a lost successful response")
	}
	var offset int64
	for ctx.Err() == nil {
		updates, err = client.GetUpdates(ctx, offset, 5)
		if err != nil {
			t.Fatal(SanitizeLogError(err))
		}
		for _, u := range updates {
			offset = u.UpdateID + 1
			m := u.Message
			if m != nil && m.Chat.ID == group && m.From != nil && m.From.ID == me.ID && m.ForumTopicCreated != nil && m.ForumTopicCreated.Name == title {
				if m.MessageThreadID != transport.topicID {
					t.Fatal("event recovered a different topic")
				}
				return
			}
		}
	}
	t.Fatal("own topic creation event was not received after response loss")
}
