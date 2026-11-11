package arcanumreview

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

type metadataFixture struct {
	mu       sync.Mutex
	attempts map[string]time.Time
	reads    chan int64
	records  chan string
	fail     bool
	idle     chan struct{}
}

func newMetadataFixture() *metadataFixture {
	return &metadataFixture{attempts: map[string]time.Time{}, reads: make(chan int64, 20), records: make(chan string, 20), idle: make(chan struct{}, 20)}
}
func (f *metadataFixture) ListDuePRMetadata(_ context.Context, now, since time.Time, _ int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := []string{}
	for _, id := range []string{"42", "43"} {
		at := f.attempts[id]
		if at.IsZero() || now.Sub(at) >= 5*time.Minute || (!since.IsZero() && at.Before(since)) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		select {
		case f.idle <- struct{}{}:
		default:
		}
	}
	return ids, nil
}
func (f *metadataFixture) RecordPRMetadata(_ context.Context, id, _, _ string, _ bool, now time.Time) error {
	f.mu.Lock()
	f.attempts[id] = now
	f.mu.Unlock()
	f.records <- id
	return nil
}
func (f *metadataFixture) ReadPRMetadata(_ context.Context, id int64) (PRSnapshot, error) {
	f.reads <- id
	if f.fail {
		return PRSnapshot{}, errors.New("unavailable")
	}
	return PRSnapshot{ID: id, Status: "merged"}, nil
}
func awaitMetadataRead(t *testing.T, f *metadataFixture, want int64) {
	t.Helper()
	select {
	case got := <-f.reads:
		if got != want {
			t.Fatalf("read=%d want=%d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("no metadata read")
	}
	select {
	case got := <-f.records:
		if got != strconv.FormatInt(want, 10) {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("no record")
	}
}
func assertNoMetadataRead(t *testing.T, f *metadataFixture) {
	t.Helper()
	select {
	case got := <-f.reads:
		t.Fatalf("unexpected read %d", got)
	case <-time.After(25 * time.Millisecond):
	}
}
func runMetadataTest(t *testing.T, f *metadataFixture, p *MetadataPresence, pause func(context.Context) bool) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runPRMetadata(ctx, f, f, p, nil, pause) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("worker shutdown blocked")
		}
	})
	return cancel
}
func setPresence(t *testing.T, p *MetadataPresence, id string, seq int64, active bool) {
	t.Helper()
	if err := p.Update(id, seq, active, false, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}
func TestMetadataWorkerSpacesReadsAndCancelsWithoutAnotherCall(t *testing.T) {
	f := newMetadataFixture()
	p := NewMetadataPresence()
	setPresence(t, p, "page", 1, true)
	paused := make(chan struct{})
	cancel := runMetadataTest(t, f, p, func(ctx context.Context) bool { close(paused); <-ctx.Done(); return false })
	<-paused
	awaitMetadataRead(t, f, 42)
	assertNoMetadataRead(t, f)
	cancel()
}
func TestMetadataWorkerVisibilityStopsBatchAndResumesOneQueue(t *testing.T) {
	f := newMetadataFixture()
	p := NewMetadataPresence()
	paused := make(chan struct{}, 10)
	resume := make(chan struct{})
	runMetadataTest(t, f, p, func(ctx context.Context) bool {
		paused <- struct{}{}
		select {
		case <-resume:
			return true
		case <-ctx.Done():
			return false
		}
	})
	assertNoMetadataRead(t, f)
	setPresence(t, p, "page", 1, true)
	awaitMetadataRead(t, f, 42)
	<-paused
	setPresence(t, p, "page", 2, false)
	resume <- struct{}{}
	assertNoMetadataRead(t, f)
	setPresence(t, p, "page", 3, true)
	awaitMetadataRead(t, f, 43)
	<-paused
	setPresence(t, p, "second", 1, true)
	setPresence(t, p, "page", 4, false)
	resume <- struct{}{}
	awaitMetadataIdle(t, f)
	assertNoMetadataRead(t, f)
	// After a completed pass, returning must refresh even recently read PRs.
	setPresence(t, p, "second", 2, false)
	setPresence(t, p, "page", 5, true)
	awaitMetadataRead(t, f, 42)
}
func TestMetadataWorkerFailedReadDoesNotBlockOtherPRs(t *testing.T) {
	f := newMetadataFixture()
	f.fail = true
	p := NewMetadataPresence()
	setPresence(t, p, "page", 1, true)
	runMetadataTest(t, f, p, func(context.Context) bool { return true })
	awaitMetadataRead(t, f, 42)
	awaitMetadataRead(t, f, 43)
	assertNoMetadataRead(t, f)
}

func TestMetadataWorkerRecoveryCoalescesPendingPassAndRefreshesCompletedPass(t *testing.T) {
	f := newMetadataFixture()
	p := NewMetadataPresence()
	setPresence(t, p, "page", 1, true)
	paused := make(chan struct{}, 10)
	resume := make(chan struct{})
	runMetadataTest(t, f, p, func(ctx context.Context) bool {
		paused <- struct{}{}
		select {
		case <-resume:
			return true
		case <-ctx.Done():
			return false
		}
	})
	awaitMetadataRead(t, f, 42)
	<-paused
	if err := p.Update("page", 2, true, true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	resume <- struct{}{}
	awaitMetadataRead(t, f, 43)
	<-paused
	resume <- struct{}{}
	awaitMetadataIdle(t, f)
	assertNoMetadataRead(t, f)
	if err := p.Update("page", 3, true, true, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	awaitMetadataRead(t, f, 42)
}

func awaitMetadataIdle(t *testing.T, f *metadataFixture) {
	t.Helper()
	select {
	case <-f.idle:
	case <-time.After(time.Second):
		t.Fatal("metadata pass did not finish")
	}
}
