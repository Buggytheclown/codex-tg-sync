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

func TestExternalPollHealthGroupsFailuresAndPartialRecoveries(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(280, "b", err)
	f.note(330, "a", err)
	f.wantMessages(1)
	f.note(340, "b", err)
	f.wantMessages(1)
	f.note(350, "a", nil)
	f.note(380, "a", nil)
	f.wantMessages(1)
	f.note(390, "b", nil)
	f.wantMessages(1)
	f.note(420, "b", nil)
	f.wantMessages(2)
	if !strings.Contains(f.sender.messages[0].text, "/pollers") || !strings.Contains(f.sender.messages[1].text, "recovered") {
		t.Fatalf("warning/recovery=%#v", f.sender.messages)
	}
	f.note(450, "a", err)
	f.note(510, "b", nil)
	f.note(510, "a", err)
	f.wantMessages(3)
}

func TestExternalPollHealthNewUnconfirmedFailureBlocksRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
	f.note(340, "a", nil)
	f.note(350, "b", err)
	f.note(370, "a", nil)
	f.wantMessages(1)
	f.note(380, "b", nil)
	f.wantMessages(1)
	f.note(410, "b", nil)
	f.wantMessages(2)
}

func TestExternalPollHealthPersistsSharedIncidentAcrossRestart(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
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
	f.note(340, "b", err)
	f.note(400, "b", err)
	f.wantMessages(1)
	f.note(410, "a", nil)
	f.note(440, "a", nil)
	f.note(450, "b", nil)
	f.note(480, "b", nil)
	f.wantMessages(2)
}

func TestExternalPollHealthConcurrentFailuresQueueOneWarning(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(270, "b", err)
	f.now = f.now.Add(time.Minute)
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
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
	f.note(340, "a", nil)
	f.note(370, "a", nil) // b has not completed a cycle for over two intervals.
	f.wantMessages(1)
	f.note(380, "b", nil)
	f.wantMessages(2)
}

func TestExternalPollHealthDisablingDoesNotAnnounceRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
	if err := f.service.RegisterExternalPoller(context.Background(), "a", time.Minute, false); err != nil {
		t.Fatal(err)
	}
	f.note(340, "a", nil) // A late result from an already disabled worker.
	f.wantMessages(1)
	if err := f.service.RegisterExternalPoller(context.Background(), "a", time.Minute, true); err != nil {
		t.Fatal(err)
	}
	f.note(350, "a", err)
	f.note(410, "a", err)
	f.wantMessages(2)
}

func TestExternalPollHealthSuppressesUndeliveredIncident(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	f.service.SetSender(nil)
	f.note(270, "a", errors.New("network timeout"))
	f.note(330, "a", errors.New("network timeout"))
	f.note(340, "a", nil)
	f.note(370, "a", nil)
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
				f.note(270, source, errors.New("network timeout"))
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
			f.note(275, "a", errors.New("network timeout"))
			f.service.processDeliveryBatch(ctx)
			warnings := max(before, 1)
			f.wantMessages(warnings)
			f.note(280, "a", nil)
			f.note(310, "a", nil)
			f.wantMessages(warnings)
			f.note(320, "b", nil)
			f.note(350, "b", nil)
			f.wantMessages(warnings + 1)
			f.note(380, "a", errors.New("network timeout"))
			f.note(440, "b", nil)
			f.note(440, "a", errors.New("network timeout"))
			f.wantMessages(warnings + 2)
			f.note(450, "a", nil)
			f.note(480, "a", nil)
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
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
	f.service.SetSender(nil)
	f.note(340, "a", nil)
	f.note(370, "a", nil)
	if err := f.service.RegisterExternalPoller(context.Background(), "a", time.Minute, false); err != nil {
		t.Fatal(err)
	}
	f.service.SetSender(f.sender)
	f.service.processDeliveryBatch(context.Background())
	f.wantMessages(1)
}

func TestExternalPollHealthReenabledSourceMustRecoverStably(t *testing.T) {
	f := newPollHealthFixture(t, "a", "b")
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
	ctx := context.Background()
	for _, enabled := range []bool{false, true} {
		if err := f.service.RegisterExternalPoller(ctx, "a", time.Minute, enabled); err != nil {
			t.Fatal(err)
		}
	}
	f.note(340, "b", nil)
	f.note(350, "a", nil)
	f.wantMessages(1)
	f.note(380, "a", nil)
	f.wantMessages(2)
}

func TestExternalPollHealthQueuedRecoveryWaitsForCurrentHealth(t *testing.T) {
	for _, blocker := range []string{"new error", "stale source", "unknown source"} {
		t.Run(blocker, func(t *testing.T) {
			f := newPollHealthFixture(t, "a", "b")
			ctx := context.Background()
			f.note(270, "a", errors.New("network timeout"))
			f.note(330, "a", errors.New("network timeout"))
			f.note(330, "b", nil)
			f.service.SetSender(nil)
			f.note(340, "a", nil)
			f.note(370, "a", nil)
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
				f.note(380, "a", errors.New("temporary network error"))
			case "stale source":
				f.note(480, "a", nil)
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
				f.note(390, "a", nil)
				f.note(420, "a", nil)
			case "stale source":
				f.note(490, "b", nil)
			case "unknown source":
				f.note(380, "c", nil)
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
		f.note(270, source, errors.New("network timeout"))
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
	f.note(270, "a", errors.New("network timeout"))
	f.service.healthMu.Lock()
	f.service.reportHealthFailureLockedToTopic(ctx, "a.poll", "Legacy warning", "timeout", "", 77)
	f.service.healthMu.Unlock()
	f.note(270, "b", errors.New("network timeout"))
	oldTracker := map[string]string{
		"last_observed_at": f.now.Format(time.RFC3339Nano),
		"warmup_until":     f.now.Add(-time.Minute).Format(time.RFC3339Nano),
		"failure_since":    f.now.Format(time.RFC3339Nano),
	}
	raw, _ := json.Marshal(oldTracker) // Real old format, without the new flags.
	if err := f.service.store.SetState(ctx, "health_tracker.b.poll", string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := f.service.migrateExternalPollHealth(ctx); err != nil {
		t.Fatal(err)
	}
	f.note(280, "a", nil)
	f.note(310, "a", nil)
	f.note(320, "b", nil)
	f.wantMessages(1)
	f.note(350, "b", nil)
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
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
	f.note(600, "a", nil) // Wake after a gap starts the existing four-minute grace.
	f.note(630, "a", err)
	for seconds := 660; seconds <= 840; seconds += 30 {
		f.note(seconds, "a", nil)
		f.wantMessages(1)
	}
	f.note(870, "a", nil)
	f.wantMessages(2)
}

func TestExternalPollHealthUnknownPollerCannotConfirmRecovery(t *testing.T) {
	f := newPollHealthFixture(t, "a")
	err := errors.New("network timeout")
	f.note(270, "a", err)
	f.note(330, "a", err)
	if err := f.service.RegisterExternalPoller(context.Background(), "b", time.Minute, true); err != nil {
		t.Fatal(err)
	}
	f.note(340, "a", nil)
	f.note(370, "a", nil)
	f.wantMessages(1)
	f.note(380, "b", nil)
	f.wantMessages(2)
}
