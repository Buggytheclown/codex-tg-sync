package arcanumreview

import (
	"fmt"
	"testing"
	"time"
)

func TestMetadataPresenceVisibilityLeaseAndOrdering(t *testing.T) {
	p := NewMetadataPresence()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	state := func(active bool, since time.Time, at time.Time) {
		t.Helper()
		got, stamp := p.State(at)
		if got != active || !stamp.Equal(since) {
			t.Fatalf("state=(%v,%v), want (%v,%v)", got, stamp, active, since)
		}
	}
	state(false, time.Time{}, now)
	update := func(id string, seq int64, active bool, at time.Time) {
		t.Helper()
		if err := p.Update(id, seq, active, false, at); err != nil {
			t.Fatal(err)
		}
	}
	update("page-a", 1, true, now)
	state(true, now, now)
	update("page-b", 1, true, now.Add(time.Second))
	update("page-a", 2, false, now.Add(2*time.Second))
	update("page-a", 1, true, now.Add(3*time.Second)) // late heartbeat cannot undo hide
	state(true, now, now.Add(3*time.Second))
	update("page-b", 2, false, now.Add(4*time.Second))
	state(false, now, now.Add(4*time.Second))
	update("page-a", 3, true, now.Add(5*time.Second))
	state(true, now.Add(5*time.Second), now.Add(5*time.Second))
	update("page-a", 4, true, now.Add(10*time.Second)) // heartbeat does not reactivate
	state(true, now.Add(5*time.Second), now.Add(54*time.Second))
	state(false, now.Add(5*time.Second), now.Add(55*time.Second))
	update("page-a", 5, true, now.Add(56*time.Second))
	state(true, now.Add(56*time.Second), now.Add(56*time.Second))
}

func TestMetadataPresenceRejectsInvalidAndBoundsClients(t *testing.T) {
	p := NewMetadataPresence()
	now := time.Now()
	for _, id := range []string{"", "has spaces", string(make([]byte, 65))} {
		if p.Update(id, 1, true, false, now) == nil {
			t.Fatalf("accepted id %q", id)
		}
	}
	if p.Update("page", 0, true, false, now) == nil {
		t.Fatal("accepted invalid sequence")
	}
	for i := 0; i < 1024; i++ {
		if err := p.Update(fmt.Sprintf("page-%d", i), 1, true, false, now); err != nil {
			t.Fatal(err)
		}
	}
	if p.Update("overflow", 1, true, false, now) == nil {
		t.Fatal("unbounded clients")
	}
	if err := p.Update("after-expiry", 1, true, false, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataPresenceRecoveryRefreshIsSequenceFenced(t *testing.T) {
	p := NewMetadataPresence()
	now := time.Now().UTC()
	if err := p.Update("page", 1, true, false, now); err != nil {
		t.Fatal(err)
	}
	recovered := now.Add(time.Second)
	if err := p.Update("page", 2, true, true, recovered); err != nil {
		t.Fatal(err)
	}
	if active, since := p.State(recovered); !active || !since.Equal(recovered) {
		t.Fatalf("recovery did not refresh: %v %v", active, since)
	}
	if err := p.Update("page", 1, true, true, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if active, since := p.State(now.Add(2 * time.Second)); !active || !since.Equal(recovered) {
		t.Fatalf("old recovery replaced newer state: %v %v", active, since)
	}
	if err := p.Update("page", 3, false, true, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if active, _ := p.State(now.Add(3 * time.Second)); active {
		t.Fatal("refresh made a hidden page active")
	}
}
