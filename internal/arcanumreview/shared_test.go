package arcanumreview

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

type memoryReadStore struct {
	sync.Mutex
	state   map[string]string
	records []PRObservation
}

func (s *memoryReadStore) GetState(_ context.Context, k string) (string, error) {
	s.Lock()
	defer s.Unlock()
	return s.state[k], nil
}
func (s *memoryReadStore) SetState(_ context.Context, k, v string) error {
	s.Lock()
	defer s.Unlock()
	s.state[k] = v
	return nil
}
func (s *memoryReadStore) PRIsMerged(context.Context, int64) (bool, error) { return false, nil }
func (s *memoryReadStore) PublishPRObservation(_ context.Context, id int64, lifecycle, common string, started, finished time.Time, ok bool) error {
	s.Lock()
	defer s.Unlock()
	o := PRObservation{Snapshot: PRSnapshot{ID: id, Status: lifecycle, CommonStatus: common}, StartedAt: started, FinishedAt: finished}
	if !ok {
		o.Err = fmt.Errorf("unavailable")
	}
	s.records = append(s.records, o)
	return nil
}
func TestSharedSearchAndPersistentCooldown(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &memoryReadStore{state: map[string]string{}}
	runner := &pageRunner{pages: []string{`{"review_requests":[{"id":1,"author":{"name":"alice"},"summary":"one","full_status":"open","common_status":"waiting_for_review"}],"total_count":1}`}}
	reader := NewSharedReader(NewCLIClient("ya", runner), store)
	reader.now = func() time.Time { return now }
	reader.SetDemand(ctx, Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}})
	reader.SearchOnce(ctx)
	reader.SearchOnce(ctx)
	if len(runner.calls) != 1 {
		t.Fatalf("calls %d", len(runner.calls))
	}
	restarted := NewSharedReader(NewCLIClient("ya", runner), store)
	restarted.now = reader.now
	restarted.SetDemand(ctx, Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}})
	restarted.SearchOnce(ctx)
	if len(runner.calls) != 1 {
		t.Fatal("restart bypassed cooldown")
	}
	s, err := reader.ReadPRSnapshot(ctx, 1)
	if err != nil || s.Status != "open" {
		t.Fatalf("%+v %v", s, err)
	}
	reader.SetDemand(ctx, Demand{})
	now = now.Add(time.Minute)
	reader.SearchOnce(ctx)
	if len(runner.calls) != 1 {
		t.Fatal("no demand still read")
	}
}

// Each actual command start has its own persisted limit, even when comments IO
// delays the checks start. A new diff and a restart cannot reset that budget.
func TestSlowRoundUsesLatestActualStartAcrossRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &memoryReadStore{state: map[string]string{}}
	commands := &timedRunner{now: &now}
	reader := NewSharedReader(NewCLIClient("ya", commands), store)
	reader.now = func() time.Time { return now }
	demand := Demand{PRs: map[int64]PRNeed{1: {Author: "alice", Comments: true, Checks: true}}}
	if err := reader.SetDemand(ctx, demand); err != nil {
		t.Fatal(err)
	}
	reader.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_review", PublishedDiffID: 9}, StartedAt: now, FinishedAt: now})
	if err := reader.SlowOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(commands.starts) != 2 || !commands.starts[1].Equal(now) {
		t.Fatal(commands.starts)
	}
	restart := NewSharedReader(NewCLIClient("ya", commands), store)
	restart.now = reader.now
	_ = restart.SetDemand(ctx, demand)
	now = time.Date(2026, 1, 1, 0, 5, 0, 0, time.UTC)
	restart.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_review", PublishedDiffID: 10}, StartedAt: now, FinishedAt: now})
	_ = restart.SlowOnce(ctx)
	if len(commands.starts) != 2 {
		t.Fatal("checks started before 05:40", commands.starts)
	}
	now = now.Add(40 * time.Second)
	restart.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_review", PublishedDiffID: 10}, StartedAt: now, FinishedAt: now})
	_ = restart.SlowOnce(ctx)
	if len(commands.starts) != 4 {
		t.Fatal(commands.starts)
	}
}

type timedRunner struct {
	now    *time.Time
	starts []time.Time
}

func (r *timedRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.starts = append(r.starts, *r.now)
	if slices.Contains(args, "comments") {
		*r.now = r.now.Add(40 * time.Second)
		return []byte(`[]`), nil
	}
	return []byte(`{"checks":[]}`), nil
}
func TestSlowCoverageCannotReturnAfterStatusMovesAwayAndBack(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &memoryReadStore{state: map[string]string{}}
	r := NewSharedReader(NewCLIClient("ya", &pageRunner{}), store)
	r.now = func() time.Time { return now }
	_ = r.SetDemand(ctx, Demand{PRs: map[int64]PRNeed{1: {Checks: true}}})
	s := PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_changes", PublishedDiffID: 9}
	r.put(ctx, PRObservation{Snapshot: s, StartedAt: now, FinishedAt: now})
	r.slow[1] = slowObservation{base: s, revision: r.revisions[1], checksAt: now, checks: []PRCheck{}}
	now = now.Add(time.Second)
	changed := s
	changed.CommonStatus = "waiting_for_review"
	r.put(ctx, PRObservation{Snapshot: changed, StartedAt: now, FinishedAt: now})
	now = now.Add(time.Second)
	r.put(ctx, PRObservation{Snapshot: s, StartedAt: now, FinishedAt: now})
	got, err := r.ReadPRSnapshot(ctx, 1)
	if err != nil || got.Coverage.Checks {
		t.Fatalf("stale AND coverage: %+v %v", got, err)
	}
}

func TestFiveStatusCardsShare120SearchesPerHourAndNoOptionalIO(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	now := base
	store := &memoryReadStore{state: map[string]string{}}
	runner := &repeatedSearchRunner{}
	r := NewSharedReader(NewCLIClient("ya", runner), store)
	r.now = func() time.Time { return now }
	d := Demand{PRs: map[int64]PRNeed{}}
	for id := int64(1); id <= 5; id++ {
		d.PRs[id] = PRNeed{Author: "alice"}
	}
	for i := 0; i < 3600; i++ {
		now = base.Add(time.Duration(i) * time.Second)
		if err := r.SetDemand(ctx, d); err != nil {
			t.Fatal(err)
		}
		if err := r.SearchOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if err := r.SlowOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if runner.searches != 120 || runner.other != 0 {
		t.Fatalf("searches=%d optional=%d", runner.searches, runner.other)
	}
}

type repeatedSearchRunner struct{ searches, other int }

func (r *repeatedSearchRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	if !slices.Contains(args, "search") {
		r.other++
		return nil, fmt.Errorf("unexpected optional IO")
	}
	r.searches++
	items := []map[string]any{}
	for id := 1; id <= 5; id++ {
		items = append(items, map[string]any{"id": id, "author": map[string]string{"name": "alice"}, "summary": "change", "full_status": "open", "common_status": "waiting_for_review"})
	}
	b, _ := json.Marshal(map[string]any{"review_requests": items, "total_count": 5})
	return b, nil
}
func TestReservationFailureCannotStartIO(t *testing.T) {
	store := &failingReadStore{memoryReadStore: memoryReadStore{state: map[string]string{}}}
	runner := &pageRunner{}
	r := NewSharedReader(NewCLIClient("ya", runner), store)
	_ = r.SetDemand(context.Background(), Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}})
	if err := r.SearchOnce(context.Background()); err == nil || len(runner.calls) != 0 {
		t.Fatalf("reservation failure err=%v calls=%v", err, runner.calls)
	}
}

type failingReadStore struct{ memoryReadStore }

func (s *failingReadStore) SetState(context.Context, string, string) error {
	return fmt.Errorf("database unavailable")
}

func TestAuthorFallbackErrorIsObservedAndRateLimited(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &memoryReadStore{state: map[string]string{}}
	command := &snapshotRunner{err: fmt.Errorf("access denied")}
	r := NewSharedReader(NewCLIClient("ya", command), store)
	r.now = func() time.Time { return now }
	_ = r.SetDemand(ctx, Demand{PRs: map[int64]PRNeed{1: {}}})
	if err := r.SlowOnce(ctx); err == nil {
		t.Fatal("read failure lost")
	}
	_, err := r.ReadPRSnapshot(ctx, 1)
	pending, ok := err.(*PendingRead)
	if !ok || pending.Cause == nil || !pending.AttemptAt.Equal(now) || !pending.NextAt.Equal(now.Add(InfoInterval)) {
		t.Fatalf("missing fallback observation: %+v %v", pending, err)
	}
	now = now.Add(time.Minute)
	_ = r.SlowOnce(ctx)
	if len(command.calls) != 1 {
		t.Fatal("fallback ignored cooldown", command.calls)
	}
}
func TestOlderStartedMergedWinsAndCannotBeUndoneByDiscoveryReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &memoryReadStore{state: map[string]string{}}
	r := NewSharedReader(NewCLIClient("ya", &pageRunner{}), store)
	r.now = func() time.Time { return now }
	d := Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}, Queries: []string{"discovery"}}
	_ = r.SetDemand(ctx, d)
	open := PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_review"}
	r.put(ctx, PRObservation{Snapshot: open, StartedAt: now, FinishedAt: now})
	r.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1, Status: "merged"}, StartedAt: now.Add(-time.Second), FinishedAt: now})
	r.searches["discovery"] = searchObservation{at: now, result: SearchResult{Items: []SearchItem{{ID: 1, Snapshot: open}}}}
	_ = r.SetDemand(ctx, d)
	if !r.terminal[1] || r.facts[1].Snapshot.Status != "merged" || r.pendingGood[1].Snapshot.Status != "merged" {
		t.Fatal("terminal overwritten by time fence or replay")
	}
}

func TestPublisherKeepsLatestGoodAndLaterFailedAttempt(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &memoryReadStore{state: map[string]string{}}
	r := NewSharedReader(NewCLIClient("ya", &pageRunner{}), store)
	r.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_changes"}, StartedAt: now, FinishedAt: now})
	r.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1}, StartedAt: now.Add(time.Second), FinishedAt: now.Add(time.Second), Err: fmt.Errorf("network failure")})
	if err := r.PublishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 2 || store.records[0].Snapshot.CommonStatus != "waiting_for_changes" || store.records[1].Err == nil {
		t.Fatalf("good projection lost behind later failure: %+v", store.records)
	}
}

func TestPendingTerminalSurvivesDemandRemovalAndReopenBeforePublish(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &memoryReadStore{state: map[string]string{}}
	command := &pageRunner{}
	r := NewSharedReader(NewCLIClient("ya", command), store)
	d := Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}}
	_ = r.SetDemand(ctx, d)
	r.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1, Status: "merged"}, StartedAt: now, FinishedAt: now})
	_ = r.SetDemand(ctx, Demand{})
	_ = r.SetDemand(ctx, d)
	_ = r.SearchOnce(ctx)
	if !r.terminal[1] || len(command.calls) != 0 {
		t.Fatal("pending terminal was lost across a temporary scope removal")
	}
}

func TestLocalSlowSkipsDoNotRenewQueueDeadline(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	deadline := now.Add(-time.Minute)
	store := &memoryReadStore{state: map[string]string{"arc.cooldown.checks.1": deadline.Format(time.RFC3339Nano)}}
	command := &pageRunner{}
	r := NewSharedReader(NewCLIClient("ya", command), store)
	r.now = func() time.Time { return now }
	_ = r.SetDemand(ctx, Demand{PRs: map[int64]PRNeed{1: {Author: "alice", Checks: true}}})
	r.put(ctx, PRObservation{Snapshot: PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_review"}, StartedAt: now, FinishedAt: now})
	r.RefreshDemand = func(ctx context.Context) error {
		at, err := r.NextSlowAt(ctx, 1)
		if err == nil && !at.Equal(deadline) {
			t.Errorf("local skip renewed queue deadline: %v", at)
		}
		return err
	}
	if err := r.SlowOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(command.calls) != 0 {
		t.Fatal("unknown revision generated optional IO")
	}
}

func TestLongerServerBackoffSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	store := &memoryReadStore{state: map[string]string{}}
	command := &snapshotRunner{err: &readBackoffError{err: fmt.Errorf("HTTP 429"), delay: 15 * time.Minute}}
	r := NewSharedReader(NewCLIClient("ya", command), store)
	r.now = func() time.Time { return now }
	d := Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}}
	_ = r.SetDemand(ctx, d)
	if err := r.SearchOnce(ctx); err == nil {
		t.Fatal("failed search became success")
	}
	now = now.Add(10 * time.Minute)
	restart := NewSharedReader(NewCLIClient("ya", command), store)
	restart.now = r.now
	_ = restart.SetDemand(ctx, d)
	_ = restart.SearchOnce(ctx)
	if len(command.calls) != 1 {
		t.Fatal("restart bypassed server backoff")
	}
}

func TestHealthyDiscoveryCoverageWaitsForItsNextRoundInsteadOfAuthorFallback(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &memoryReadStore{state: map[string]string{}}
	command := &pageRunner{pages: []string{`{"review_requests":[{"id":1,"author":{"name":"alice"},"summary":"one","full_status":"open","common_status":"waiting_for_review"}],"total_count":1}`}}
	r := NewSharedReader(NewCLIClient("ya", command), store)
	r.now = func() time.Time { return now }
	q := AssignedQuery("reviewer")
	_ = r.SetDemand(ctx, Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}, Queries: []string{q}})
	if err := r.SearchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	// Executor/tick skew: the row reaches 30s just before its existing query is
	// allowed again. There is no need to create a second author query.
	store.state["arc.cooldown."+queryKey(q)] = now.Add(31 * time.Second).Format(time.RFC3339Nano)
	now = now.Add(30500 * time.Millisecond)
	if err := r.SearchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(command.calls) != 1 {
		t.Fatalf("unnecessary author fallback: %+v", command.calls)
	}
}

func TestMissingRowInNextDiscoveryUsesAuthorSearchToConfirmMerged(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := &memoryReadStore{state: map[string]string{}}
	command := &pageRunner{pages: []string{`{"review_requests":[{"id":1,"author":{"name":"alice"},"summary":"one","full_status":"open","common_status":"waiting_for_review"}],"total_count":1}`, `{"review_requests":[],"total_count":0}`, `{"review_requests":[{"id":1,"author":{"name":"alice"},"summary":"one","full_status":"merged","common_status":"merged"}],"total_count":1}`}}
	r := NewSharedReader(NewCLIClient("ya", command), store)
	r.now = func() time.Time { return now }
	_ = r.SetDemand(ctx, Demand{PRs: map[int64]PRNeed{1: {Author: "alice"}}, Queries: []string{AssignedQuery("reviewer")}})
	if err := r.SearchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	now = now.Add(31 * time.Second)
	if err := r.SearchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if len(command.calls) != 3 || !r.terminal[1] {
		t.Fatalf("needed author fallback suppressed: calls=%d terminal=%t", len(command.calls), r.terminal[1])
	}
}
