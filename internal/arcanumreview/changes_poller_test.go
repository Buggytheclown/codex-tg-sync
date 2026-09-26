package arcanumreview

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/storage"
)

type fakeWaitingChangesClient struct {
	requests []PullRequest
}

func TestChangesPollerRetriesAfterEnqueueFailure(t *testing.T) {
	t.Parallel()
	client := &fakeWaitingChangesClient{requests: []PullRequest{{ID: 12345678, Author: "tidjei", Summary: "Title"}}}
	sink := &dedupeRequestSink{err: errors.New("storage unavailable")}
	poller := NewChangesPoller(client, sink, Config{Login: "tidjei", CWD: "/project", TelegramTopicID: 77})

	if _, err := poller.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce succeeded while storage failed")
	}
	sink.err = nil
	if created, err := poller.PollOnce(context.Background()); err != nil || created != 1 || sink.calls != 2 {
		t.Fatalf("retry created=%d err=%v sink calls=%d", created, err, sink.calls)
	}
}

func (c *fakeWaitingChangesClient) ListAuthoredWaitingForChanges(context.Context, string) ([]PullRequest, error) {
	return append([]PullRequest(nil), c.requests...), nil
}

func TestChangesPollerCreatesOneAutomaticSummaryRequest(t *testing.T) {
	t.Parallel()
	client := &fakeWaitingChangesClient{requests: []PullRequest{
		{ID: 12345678, Author: "Tidjei", Summary: "Own PR with feedback"},
		{ID: 12345679, Author: "alice", Summary: "Unexpected PR from search"},
	}}
	sink := &dedupeRequestSink{}
	poller := NewChangesPoller(client, sink, Config{Login: "tidjei", CWD: "/project", TelegramTopicID: 77})

	if created, err := poller.PollOnce(context.Background()); err != nil || created != 1 {
		t.Fatalf("PollOnce created=%d err=%v", created, err)
	}
	request := sink.requests[ChangesSource+"|12345678"]
	wantURL := "https://a.yandex-team.ru/review/12345678"
	if request.ID != ChangesSource+":12345678" || request.Source != ChangesSource || request.ExternalID != "12345678" || request.SourceURL != wantURL {
		t.Fatalf("identity/source = %#v", request)
	}
	if request.Prompt != wantURL+" кратко расскажи суть замечаний" || !request.AutoStart {
		t.Fatalf("prompt/auto-start = %#v", request)
	}
	if request.Sender != "Tidjei" || request.Title != "Own PR with feedback" || request.CWD != "/project" || request.TelegramTopicID != 77 {
		t.Fatalf("metadata = %#v", request)
	}
	if _, exists := sink.requests[ChangesSource+"|12345679"]; exists {
		t.Fatal("another author's PR was queued")
	}
	if created, err := poller.PollOnce(context.Background()); err != nil || created != 0 || sink.calls != 1 {
		t.Fatalf("second poll created=%d err=%v sink calls=%d", created, err, sink.calls)
	}
}

func TestChangesPollerRestartUsesSQLiteDedupe(t *testing.T) {
	t.Parallel()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := &fakeWaitingChangesClient{requests: []PullRequest{{ID: 12345678, Author: "tidjei", Summary: "Title"}}}
	sink := storageRequestSink{store: store}
	cfg := Config{Login: "tidjei", CWD: "/project", TelegramTopicID: 77}

	if created, err := NewChangesPoller(client, sink, cfg).PollOnce(context.Background()); err != nil || created != 1 {
		t.Fatalf("first process created=%d err=%v", created, err)
	}
	if created, err := NewChangesPoller(client, sink, cfg).PollOnce(context.Background()); err != nil || created != 0 {
		t.Fatalf("restarted process created=%d err=%v, want SQLite dedupe", created, err)
	}
}
