package ymessenger

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type fakeUpdatesClient struct {
	offsets     []int64
	updates     []Update
	err         error
	roots       map[string]ContextMessage
	rootErr     error
	rootLookups []string
}

func (f *fakeUpdatesClient) GetUpdates(_ context.Context, offset int64, _ int) ([]Update, error) {
	f.offsets = append(f.offsets, offset)
	return append([]Update(nil), f.updates...), f.err
}

func (f *fakeUpdatesClient) GetThreadRoot(_ context.Context, chatID string, messageID int64) (*ContextMessage, error) {
	key := SourceMessageKey(chatID, messageID)
	f.rootLookups = append(f.rootLookups, key)
	if f.rootErr != nil {
		return nil, f.rootErr
	}
	root, ok := f.roots[key]
	if !ok {
		return nil, nil
	}
	return &root, nil
}

type fakeRequestSink struct {
	cursor   int64
	requests []model.ExternalLaunchRequest
}

func (f *fakeRequestSink) NoteExternalPollResult(context.Context, string, error) {}

func (f *fakeRequestSink) ExternalSourceCursor(_ context.Context, _ string) (int64, error) {
	return f.cursor, nil
}

func (f *fakeRequestSink) IngestExternalRequests(_ context.Context, _ string, cursor int64, requests []model.ExternalLaunchRequest) (int, error) {
	f.cursor = cursor
	f.requests = append(f.requests, requests...)
	return len(requests), nil
}

func TestPollOnceUsesPersistedCursorAndAdvancesPastIgnoredUpdates(t *testing.T) {
	t.Parallel()
	client := &fakeUpdatesClient{updates: []Update{
		{UpdateID: 8, MessageID: 10, From: User{Login: "mallory"}, Chat: Chat{ID: "chat"}, Text: "ignored"},
		{UpdateID: 9, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "chat"}, Text: "task", MentionedUsers: []User{{Login: "robot-example"}}},
	}}
	sink := &fakeRequestSink{cursor: 7}
	poller := NewPoller(client, sink, FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}})

	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("PollOnce failed: %v", err)
	}
	if len(client.offsets) != 1 || client.offsets[0] != 8 {
		t.Fatalf("offsets = %#v, want [8]", client.offsets)
	}
	if sink.cursor != 9 || len(sink.requests) != 1 {
		t.Fatalf("sink cursor=%d requests=%#v", sink.cursor, sink.requests)
	}
}

func TestPollOnceResolvesActionableThreadRootFromHistory(t *testing.T) {
	t.Parallel()
	client := &fakeUpdatesClient{updates: []Update{
		{UpdateID: 8, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "chat", ThreadID: 9}, Text: "@robot-example inspect", MentionedUsers: []User{{Login: "robot-example"}}},
		{UpdateID: 9, MessageID: 12, From: User{Login: "mallory"}, Chat: Chat{ID: "other", ThreadID: 7}, Text: "untrusted"},
	}, roots: map[string]ContextMessage{
		SourceMessageKey("chat", 9): {MessageID: 9, From: User{Login: "alert-robot"}, Text: "Root alert"},
	}}
	sink := &fakeRequestSink{cursor: 7}
	poller := NewPoller(client, sink, FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}})
	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.requests) != 1 || !strings.Contains(sink.requests[0].Prompt, "Root alert") {
		t.Fatalf("requests = %#v", sink.requests)
	}
	if len(client.rootLookups) != 1 || client.rootLookups[0] != SourceMessageKey("chat", 9) {
		t.Fatalf("root lookups = %#v, want only actionable thread root", client.rootLookups)
	}
}

func TestPollOnceHistoryFailureDoesNotAdvanceCursor(t *testing.T) {
	t.Parallel()
	client := &fakeUpdatesClient{
		updates: []Update{{UpdateID: 8, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "chat", ThreadID: 9}, Text: "@robot-example inspect", MentionedUsers: []User{{Login: "robot-example"}}}},
		rootErr: errors.New("history unavailable"),
	}
	sink := &fakeRequestSink{cursor: 7}
	poller := NewPoller(client, sink, FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}})
	if err := poller.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce succeeded while History API failed")
	}
	if sink.cursor != 7 || len(sink.requests) != 0 {
		t.Fatalf("cursor=%d requests=%#v after history failure", sink.cursor, sink.requests)
	}
}

func TestPollOnceSkipsOneBrokenThreadRootAfterThreeAttempts(t *testing.T) {
	t.Parallel()
	client := &fakeUpdatesClient{
		updates: []Update{{UpdateID: 8, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "chat", ThreadID: 9}, Text: "@robot-example inspect", MentionedUsers: []User{{Login: "robot-example"}}}},
		rootErr: errors.New("history unavailable"),
	}
	sink := &fakeRequestSink{cursor: 7}
	poller := NewPoller(client, sink, FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}})
	for attempt := 1; attempt <= 3; attempt++ {
		err := poller.PollOnce(context.Background())
		if attempt < 3 && err == nil {
			t.Fatalf("attempt %d unexpectedly succeeded", attempt)
		}
		if attempt == 3 && err != nil {
			t.Fatalf("third attempt did not degrade missing context: %v", err)
		}
	}
	if sink.cursor != 8 || len(sink.requests) != 1 || !strings.Contains(sink.requests[0].Prompt, "Unavailable: History API") {
		t.Fatalf("cursor=%d requests=%#v", sink.cursor, sink.requests)
	}
}

func TestPollOnceNeverSkipsHistoryAuthorizationFailure(t *testing.T) {
	t.Parallel()
	client := &fakeUpdatesClient{
		updates: []Update{{UpdateID: 8, MessageID: 11, From: User{Login: "alice"}, Chat: Chat{ID: "chat", ThreadID: 9}, Text: "@robot-example inspect", MentionedUsers: []User{{Login: "robot-example"}}}},
		rootErr: &HTTPStatusError{Operation: "YMessenger history", StatusCode: 403},
	}
	sink := &fakeRequestSink{cursor: 7}
	poller := NewPoller(client, sink, FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}})
	for attempt := 0; attempt < 4; attempt++ {
		if err := poller.PollOnce(context.Background()); err == nil {
			t.Fatalf("authorization failure skipped on attempt %d", attempt+1)
		}
	}
	if sink.cursor != 7 || len(sink.requests) != 0 {
		t.Fatalf("authorization failure advanced cursor=%d requests=%#v", sink.cursor, sink.requests)
	}
}

func TestPollOnceFailureDoesNotAdvanceCursorAndNextCallRetriesSameOffset(t *testing.T) {
	t.Parallel()
	client := &fakeUpdatesClient{err: errors.New("temporary failure")}
	sink := &fakeRequestSink{cursor: 7}
	poller := NewPoller(client, sink, FilterConfig{})

	if err := poller.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce succeeded during temporary failure")
	}
	if sink.cursor != 7 {
		t.Fatalf("cursor advanced to %d after failed request", sink.cursor)
	}
	client.err = nil
	client.updates = []Update{{UpdateID: 8, MessageID: 1, From: User{Login: "alice"}, Chat: Chat{ID: "chat"}, Text: "task", MentionedUsers: []User{{Login: "robot-example"}}}}
	poller.filter = FilterConfig{RobotLogin: "robot-example", AllowedSenders: []string{"alice"}}
	if err := poller.PollOnce(context.Background()); err != nil {
		t.Fatalf("retry PollOnce failed: %v", err)
	}
	if len(client.offsets) != 2 || client.offsets[0] != 8 || client.offsets[1] != 8 || sink.cursor != 8 {
		t.Fatalf("offsets=%v cursor=%d, want [8 8] and 8", client.offsets, sink.cursor)
	}
}

func TestRetryDelayBacksOffFastPolling(t *testing.T) {
	t.Parallel()
	if got := retryDelay(2 * time.Second); got != 5*time.Second {
		t.Fatalf("retryDelay(2s)=%s, want 5s", got)
	}
	if got := retryDelay(10 * time.Second); got != 10*time.Second {
		t.Fatalf("retryDelay(10s)=%s, want 10s", got)
	}
}
