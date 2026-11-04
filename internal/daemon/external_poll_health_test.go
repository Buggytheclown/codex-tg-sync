package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type pollHealthFixture struct {
	t       *testing.T
	service *Service
	sender  *recordingSender
	now     time.Time
}

func newPollHealthFixture(t *testing.T, sources ...string) *pollHealthFixture {
	t.Helper()
	f := &pollHealthFixture{t: t, service: newTestService(t), sender: &recordingSender{}}
	f.service.cfg.ExternalRequestsTopicID = 77
	f.service.now = func() time.Time { return f.now }
	f.service.SetSender(f.sender)
	for _, source := range sources {
		if err := f.service.RegisterExternalPoller(context.Background(), source, time.Minute, true); err != nil {
			t.Fatal(err)
		}
	}
	for seconds := 0; seconds <= 240; seconds += 30 {
		for _, source := range sources {
			f.note(seconds, source, nil)
		}
	}
	return f
}

func (f *pollHealthFixture) note(seconds int, source string, err error) {
	f.now = time.Date(2026, 10, 13, 0, 0, seconds, 0, time.UTC)
	f.service.NoteExternalPollResult(context.Background(), source, err)
	f.service.processDeliveryBatch(context.Background())
}

func (f *pollHealthFixture) wantMessages(count int) {
	f.t.Helper()
	if len(f.sender.messages) != count {
		states, _ := f.service.store.ListState(context.Background())
		for key, value := range states {
			if strings.HasPrefix(key, "health") || strings.HasPrefix(key, "poller.") {
				f.t.Logf("%s=%s", key, value)
			}
		}
		f.t.Fatalf("messages=%#v, want %d", f.sender.messages, count)
	}
	for _, message := range f.sender.messages {
		if message.topicID != 77 {
			f.t.Fatalf("topic=%d, want Requests topic", message.topicID)
		}
	}
}

// pollFor supplies fresh observations throughout a phase, including both endpoints.
// Omitted sources deliberately receive no observations (for stale-source tests).
func (f *pollHealthFixture) pollFor(duration time.Duration, results map[string]error) {
	f.t.Helper()
	start := f.now
	for elapsed := time.Duration(0); ; {
		f.now = start.Add(elapsed)
		for source, err := range results {
			f.service.NoteExternalPollResult(context.Background(), source, err)
		}
		f.service.processDeliveryBatch(context.Background())
		if elapsed == duration {
			break
		}
		elapsed += 30 * time.Second
		if elapsed > duration {
			elapsed = duration
		}
	}
}

func TestExternalPollHealthNotificationThresholds(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	err := errors.New("network timeout")
	f.pollFor(599*time.Second, map[string]error{"a": err})
	f.wantMessages(0)
	f.pollFor(time.Second, map[string]error{"a": err})
	f.wantMessages(1)
	f.pollFor(299*time.Second, map[string]error{"a": nil})
	f.wantMessages(1)
	f.pollFor(time.Second, map[string]error{"a": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthShortFailureAndInterruptedRecoveryStaySilent(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	err := errors.New("network timeout")
	f.pollFor(9*time.Minute, map[string]error{"a": err})
	f.pollFor(5*time.Minute, map[string]error{"a": nil})
	f.wantMessages(0)
	f.pollFor(10*time.Minute, map[string]error{"a": err})
	f.wantMessages(1)
	f.pollFor(4*time.Minute, map[string]error{"a": nil})
	f.pollFor(0, map[string]error{"a": err})
	f.pollFor(299*time.Second, map[string]error{"a": nil})
	f.wantMessages(1)
	f.pollFor(time.Second, map[string]error{"a": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthGroupsFailuresAndPartialRecoveries(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.pollFor(10*time.Minute, map[string]error{"a": err, "b": err})
	f.wantMessages(1)
	f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": err})
	f.wantMessages(1)
	f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": nil})
	f.wantMessages(2)
	if !strings.Contains(f.sender.messages[0].text, "/pollers") || !strings.Contains(f.sender.messages[1].text, "recovered") {
		t.Fatalf("warning/recovery=%#v", f.sender.messages)
	}
	f.pollFor(10*time.Minute, map[string]error{"a": err, "b": nil})
	f.wantMessages(3)
}

func TestExternalPollHealthNewUnconfirmedFailureBlocksRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.pollFor(10*time.Minute, map[string]error{"a": err, "b": nil})
	f.pollFor(4*time.Minute, map[string]error{"a": nil, "b": nil})
	f.pollFor(time.Minute, map[string]error{"a": nil, "b": err})
	f.wantMessages(1)
	f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthPersistsSharedIncidentAcrossRestart(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.pollFor(10*time.Minute, map[string]error{"a": err, "b": nil})
	cfg := f.service.cfg
	if err := f.service.Close(); err != nil {
		t.Fatal(err)
	}
	service, openErr := New(cfg)
	if openErr != nil {
		t.Fatal(openErr)
	}
	t.Cleanup(func() { _ = service.Close() })
	f.service = service
	service.now = func() time.Time { return f.now }
	service.SetSender(f.sender)
	f.pollFor(10*time.Minute, map[string]error{"a": err, "b": err})
	f.wantMessages(1)
	f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthConcurrentFailuresQueueOneWarning(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.pollFor(599*time.Second, map[string]error{"a": err, "b": err})
	f.wantMessages(0)
	f.now = f.now.Add(time.Second)
	var wg sync.WaitGroup
	for _, source := range []string{"a", "b", "a", "b"} {
		wg.Go(func() { f.service.NoteExternalPollResult(context.Background(), source, err) })
	}
	wg.Wait()
	f.service.processDeliveryBatch(context.Background())
	f.wantMessages(1)
}

func TestExternalPollHealthDoesNotRecoverWithoutFreshObservations(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout"), "b": nil})
	f.pollFor(5*time.Minute, map[string]error{"a": nil}) // b is stale.
	f.wantMessages(1)
	f.pollFor(0, map[string]error{"b": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthDisablingDoesNotAnnounceRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	err := errors.New("network timeout")
	f.pollFor(10*time.Minute, map[string]error{"a": err})
	if err := f.service.RegisterExternalPoller(context.Background(), "a", time.Minute, false); err != nil {
		t.Fatal(err)
	}
	f.pollFor(0, map[string]error{"a": nil}) // Late result from a disabled worker.
	f.wantMessages(1)
	if err := f.service.RegisterExternalPoller(context.Background(), "a", time.Minute, true); err != nil {
		t.Fatal(err)
	}
	f.pollFor(10*time.Minute, map[string]error{"a": err})
	f.wantMessages(2)
}

func TestExternalPollHealthSuppressesUndeliveredIncident(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	f.service.SetSender(nil)
	f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout")})
	f.pollFor(5*time.Minute, map[string]error{"a": nil})
	f.service.SetSender(f.sender)
	f.service.processDeliveryBatch(context.Background())
	f.wantMessages(0)
}

func TestExternalPollHealthUpgradeConsolidatesLegacyIncidents(t *testing.T) {
	for _, delivered := range []bool{false, true} {
		name := "pending warnings"
		if delivered {
			name = "delivered warnings"
		}
		t.Run(name, func(t *testing.T) {
			f := newPollHealthFixture(t, "a", "b")
			f.service.SetSender(nil)
			ctx := context.Background()
			for _, source := range []string{"a", "b"} {
				f.pollFor(0, map[string]error{source: errors.New("network timeout")})
				f.service.healthMu.Lock()
				f.service.reportHealthFailureLockedToTopic(ctx, source+".poll", "Legacy warning", "timeout", "", 77)
				f.service.healthMu.Unlock()
			}
			f.service.SetSender(f.sender)
			before := 0
			if delivered {
				f.service.processDeliveryBatch(ctx)
				before = 2
			}
			for range 2 { // Startup recovery is idempotent.
				if err := f.service.migrateExternalPollHealth(ctx); err != nil {
					t.Fatal(err)
				}
			}
			f.pollFor(0, map[string]error{"a": errors.New("network timeout")})
			warnings := max(before, 1)
			f.wantMessages(warnings)
			f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": errors.New("network timeout")})
			f.wantMessages(warnings)
			f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": nil})
			f.wantMessages(warnings + 1)
			f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout"), "b": nil})
			f.wantMessages(warnings + 2)
			f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": nil})
			f.wantMessages(warnings + 3)
			states, err := f.service.store.ListState(ctx)
			if err != nil || len(openHealthIncidentNames(states)) != 0 {
				t.Fatalf("open incidents=%v err=%v", openHealthIncidentNames(states), err)
			}
		})
	}
}

func TestExternalPollHealthDisablingSuppressesQueuedRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout")})
	f.service.SetSender(nil)
	f.pollFor(5*time.Minute, map[string]error{"a": nil})
	if err := f.service.RegisterExternalPoller(context.Background(), "a", time.Minute, false); err != nil {
		t.Fatal(err)
	}
	f.service.SetSender(f.sender)
	f.service.processDeliveryBatch(context.Background())
	f.wantMessages(1)
}

func TestExternalPollHealthReenabledSourceMustRecoverStably(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout"), "b": nil})
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		if err := f.service.RegisterExternalPoller(ctx, "a", time.Minute, enabled); err != nil {
			t.Fatal(err)
		}
	}
	f.pollFor(299*time.Second, map[string]error{"a": nil, "b": nil})
	f.wantMessages(1)
	f.pollFor(time.Second, map[string]error{"a": nil, "b": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthQueuedRecoveryWaitsForCurrentHealth(t *testing.T) {
	for _, blocker := range []string{"new error", "stale source", "unknown source"} {
		t.Run(blocker, func(t *testing.T) {
			f := newPollHealthFixture(t, "a", "b")
			ctx := context.Background()
			f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout"), "b": nil})
			f.service.SetSender(nil)
			f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": nil})
			items, err := f.service.store.ClaimDeliveryBatch(ctx, 10)
			if err != nil || len(items) != 1 || !strings.Contains(items[0].PayloadJSON, "recovered") {
				t.Fatalf("queued recovery=%#v err=%v", items, err)
			}
			item := items[0]
			release := func() {
				t.Helper()
				if err := f.service.store.FailDelivery(ctx, item.ID, item.RetryCount, time.Now().UTC(), "", false); err != nil {
					t.Fatal(err)
				}
			}
			switch blocker {
			case "new error":
				f.pollFor(0, map[string]error{"a": errors.New("temporary network error")})
			case "stale source":
				f.pollFor(150*time.Second, map[string]error{"a": nil})
			case "unknown source":
				if err := f.service.RegisterExternalPoller(ctx, "c", time.Minute, true); err != nil {
					t.Fatal(err)
				}
			}
			release()
			f.service.SetSender(f.sender)
			f.service.processDeliveryBatch(ctx)
			f.wantMessages(1)
			if status, err := f.service.store.DeliveryStatusForEvent(ctx, item.EventID, item.ChatKey); err != nil || status != "retry" {
				t.Fatalf("blocked recovery status=%q err=%v", status, err)
			}
			switch blocker {
			case "new error":
				f.pollFor(5*time.Minute, map[string]error{"a": nil, "b": nil})
			case "stale source":
				f.pollFor(0, map[string]error{"b": nil})
			case "unknown source":
				f.pollFor(0, map[string]error{"c": nil})
			}
			release()
			f.service.processDeliveryBatch(ctx)
			f.wantMessages(2)
			f.service.processDeliveryBatch(ctx)
			f.wantMessages(2)
		})
	}
}

func TestExternalPollHealthRegistrationCannotWarnBeforeConfigurationIsApplied(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	f.service.SetSender(nil)
	ctx := context.Background()
	for _, source := range []string{"a", "b"} {
		f.pollFor(0, map[string]error{source: errors.New("network timeout")})
		f.service.healthMu.Lock()
		f.service.reportHealthFailureLockedToTopic(ctx, source+".poll", "Legacy warning", "timeout", "", 77)
		f.service.healthMu.Unlock()
	}
	if err := f.service.migrateExternalPollHealth(ctx); err != nil {
		t.Fatal(err)
	}
	f.service.SetSender(f.sender)
	for _, source := range []string{"a", "b"} {
		if err := f.service.RegisterExternalPoller(ctx, source, time.Minute, false); err != nil {
			t.Fatal(err)
		}
		f.service.processDeliveryBatch(ctx)
		f.wantMessages(0)
	}
}

func TestExternalPollHealthUpgradeRetainsUnconfirmedErrors(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	ctx := context.Background()
	f.pollFor(0, map[string]error{"a": errors.New("network timeout")})
	f.service.healthMu.Lock()
	f.service.reportHealthFailureLockedToTopic(ctx, "a.poll", "Legacy warning", "timeout", "", 77)
	f.service.healthMu.Unlock()
	f.pollFor(0, map[string]error{"b": errors.New("network timeout")})
	oldTracker := map[string]string{
		"last_observed_at": f.now.Format(time.RFC3339Nano),
		"warmup_until":     f.now.Add(-time.Minute).Format(time.RFC3339Nano),
		"failure_since":    f.now.Format(time.RFC3339Nano),
	}
	raw, _ := json.Marshal(oldTracker) // Old format without the recovery flags.
	if err := f.service.store.SetState(ctx, "health_tracker.b.poll", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.migrateExternalPollHealth(ctx); err != nil {
		t.Fatal(err)
	}
	f.pollFor(time.Minute, map[string]error{"a": nil})
	f.pollFor(299*time.Second, map[string]error{"a": nil, "b": nil})
	f.wantMessages(1)
	f.pollFor(time.Second, map[string]error{"a": nil, "b": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthUpgradeSuppressesLegacyQueuedRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	ctx := context.Background()
	f.service.healthMu.Lock()
	f.service.reportHealthFailureLockedToTopic(ctx, "a.poll", "Legacy warning", "timeout", "", 77)
	f.service.healthMu.Unlock()
	f.service.processDeliveryBatch(ctx)
	f.service.healthMu.Lock()
	f.service.reportHealthRecoveredLockedToTopic(ctx, "a.poll", "Legacy recovery", "", 77)
	f.service.healthMu.Unlock()
	if err := f.service.migrateExternalPollHealth(ctx); err != nil {
		t.Fatal(err)
	}
	f.service.processDeliveryBatch(ctx)
	f.wantMessages(1)
}

func TestExternalPollHealthResumeRequiresStableRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout")})
	f.now = f.now.Add(3 * time.Minute) // Wake starts the existing four-minute grace.
	f.pollFor(0, map[string]error{"a": nil})
	f.pollFor(0, map[string]error{"a": errors.New("network timeout")})
	f.pollFor(4*time.Minute+299*time.Second, map[string]error{"a": nil})
	f.wantMessages(1)
	f.pollFor(time.Second, map[string]error{"a": nil})
	f.wantMessages(2)
}

func TestExternalPollHealthUnknownPollerCannotConfirmRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	f.pollFor(10*time.Minute, map[string]error{"a": errors.New("network timeout")})
	if err := f.service.RegisterExternalPoller(context.Background(), "b", time.Minute, true); err != nil {
		t.Fatal(err)
	}
	f.pollFor(5*time.Minute, map[string]error{"a": nil})
	f.wantMessages(1)
	f.pollFor(0, map[string]error{"b": nil})
	f.wantMessages(2)
}
