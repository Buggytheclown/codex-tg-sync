package arcanumreview

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

type fakeAssignedClient struct {
	requests []PullRequest
	err      error
	calls    int
}

func (c *fakeAssignedClient) ListAssigned(context.Context, string) ([]PullRequest, error) {
	c.calls++
	return append([]PullRequest(nil), c.requests...), c.err
}

type dedupeRequestSink struct {
	requests map[string]model.ExternalLaunchRequest
	calls    int
	err      error
}

func (*dedupeRequestSink) NoteExternalPollResult(context.Context, string, error) {}

func (s *dedupeRequestSink) EnqueueExternalRequests(_ context.Context, source string, requests []model.ExternalLaunchRequest) (int, error) {
	s.calls++
	if s.err != nil {
		return 0, s.err
	}
	if s.requests == nil {
		s.requests = make(map[string]model.ExternalLaunchRequest)
	}
	created := 0
	for _, request := range requests {
		key := source + "|" + request.ExternalID
		if _, ok := s.requests[key]; ok {
			continue
		}
		s.requests[key] = request
		created++
	}
	return created, nil
}

func TestPollerMarksSeenOnlyAfterSuccessfulEnqueue(t *testing.T) {
	t.Parallel()
	client := &fakeAssignedClient{requests: []PullRequest{{ID: 12345678, Author: "alice", Summary: "Title"}}}
	sink := &dedupeRequestSink{err: errors.New("storage unavailable")}
	poller := NewPoller(client, sink, Config{Login: "reviewer-example", CWD: "/project", TelegramTopicID: 77})

	if _, err := poller.PollOnce(context.Background()); err == nil {
		t.Fatal("PollOnce succeeded while storage failed")
	}
	sink.err = nil
	if created, err := poller.PollOnce(context.Background()); err != nil || created != 1 || sink.calls != 2 {
		t.Fatalf("retry created=%d err=%v sink calls=%d", created, err, sink.calls)
	}
}

func TestPollOnceCreatesDisplayMetadataAndExactReviewPrompt(t *testing.T) {
	t.Parallel()
	client := &fakeAssignedClient{requests: []PullRequest{{
		ID: 12345678, Author: "alice", Summary: "Example pull request",
	}}}
	sink := &dedupeRequestSink{}
	poller := NewPoller(client, sink, Config{Login: "reviewer-example", CWD: "/projects", TelegramTopicID: 77})

	created, err := poller.PollOnce(context.Background())
	if err != nil || created != 1 {
		t.Fatalf("PollOnce created=%d err=%v", created, err)
	}
	request := sink.requests[Source+"|12345678"]
	wantURL := "https://a.yandex-team.ru/review/12345678"
	wantPrompt := "$arc-pr-review [" + wantURL + "](" + wantURL + ")"
	if request.ID != Source+":12345678" || request.Source != Source || request.ExternalID != "12345678" {
		t.Fatalf("identity = %#v", request)
	}
	if request.Sender != "alice" || request.Title != client.requests[0].Summary || request.SourceURL != wantURL {
		t.Fatalf("display metadata = %#v", request)
	}
	if request.SafePreview != "Review Arcadia PR #12345678" || request.Prompt != wantPrompt {
		t.Fatalf("preview/prompt = %q / %q", request.SafePreview, request.Prompt)
	}
	if request.CWD != "/projects" || request.TelegramTopicID != 77 || request.AutoStart {
		t.Fatalf("dispatch fields = %#v", request)
	}
}

func TestPollerSeenAvoidsWritesDuringProcessLifetime(t *testing.T) {
	t.Parallel()
	client := &fakeAssignedClient{requests: []PullRequest{{ID: 12345678, Author: "alice", Summary: "Title"}}}
	sink := &dedupeRequestSink{}
	cfg := Config{Login: "reviewer-example", CWD: "/project", TelegramTopicID: 77}
	poller := NewPoller(client, sink, cfg)

	if created, err := poller.PollOnce(context.Background()); err != nil || created != 1 {
		t.Fatalf("first poll created=%d err=%v", created, err)
	}
	if created, err := poller.PollOnce(context.Background()); err != nil || created != 0 || sink.calls != 1 {
		t.Fatalf("repeat poll created=%d err=%v sink calls=%d, want in-memory skip", created, err, sink.calls)
	}

}

type storageRequestSink struct{ store *storage.Store }

func (s storageRequestSink) EnqueueExternalRequests(ctx context.Context, source string, requests []model.ExternalLaunchRequest) (int, error) {
	return s.store.EnqueueExternalLaunchRequests(ctx, source, requests)
}

func (storageRequestSink) NoteExternalPollResult(context.Context, string, error) {}

func TestPollerRestartUsesSQLiteDedupe(t *testing.T) {
	t.Parallel()
	store, err := storage.Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	client := &fakeAssignedClient{requests: []PullRequest{{ID: 12345678, Author: "alice", Summary: "Title"}}}
	sink := storageRequestSink{store: store}
	cfg := Config{Login: "reviewer-example", CWD: "/project", TelegramTopicID: 77}

	if created, err := NewPoller(client, sink, cfg).PollOnce(context.Background()); err != nil || created != 1 {
		t.Fatalf("first process created=%d err=%v", created, err)
	}
	if created, err := NewPoller(client, sink, cfg).PollOnce(context.Background()); err != nil || created != 0 {
		t.Fatalf("restarted process created=%d err=%v, want SQLite dedupe", created, err)
	}
}
