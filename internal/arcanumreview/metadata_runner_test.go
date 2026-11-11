package arcanumreview

import (
	"context"
	"testing"
	"time"
)

type metadataFixture struct {
	reads   chan int64
	records chan string
}

func (f *metadataFixture) ListDuePRMetadata(context.Context, time.Time, int) ([]string, error) {
	return []string{"42", "43"}, nil
}
func (f *metadataFixture) RecordPRMetadata(_ context.Context, id, _, _ string, _ bool, _ time.Time) error {
	f.records <- id
	return nil
}
func (f *metadataFixture) ReadPRMetadata(_ context.Context, id int64) (PRSnapshot, error) {
	f.reads <- id
	return PRSnapshot{ID: id, Status: "merged"}, nil
}

func TestMetadataWorkerSpacesReadsAndCancelsWithoutAnotherCall(t *testing.T) {
	fixture := &metadataFixture{reads: make(chan int64, 2), records: make(chan string, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	paused := make(chan struct{})
	finished := make(chan struct{})
	pause := func(ctx context.Context) bool { close(paused); <-ctx.Done(); return false }
	go func() { defer close(finished); runPRMetadata(ctx, fixture, fixture, nil, pause) }()
	select {
	case <-paused:
	case <-time.After(time.Second):
		t.Fatal("metadata worker did not reach its rate limit")
	}
	if got := <-fixture.reads; got != 42 {
		t.Fatalf("first PR=%d", got)
	}
	if got := <-fixture.records; got != "42" {
		t.Fatalf("first record=%s", got)
	}
	if len(fixture.reads) != 0 || len(fixture.records) != 0 {
		t.Fatal("metadata calls crossed the pacing boundary")
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("metadata pacing blocked shutdown")
	}
}
