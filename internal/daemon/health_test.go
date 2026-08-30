package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestHealthEpisodeQueuesOneWarningAndOneRecovery(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	service.reportHealthFailure(ctx, "ymessenger.poll", "YMessenger polling failed", "token expired", "Check token.")
	service.reportHealthFailure(ctx, "ymessenger.poll", "YMessenger polling failed", "token still expired", "Check token.")

	items, err := service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Kind != "health" || !strings.Contains(items[0].PayloadJSON, "token expired") {
		t.Fatalf("warning items=%#v err=%v", items, err)
	}
	if items[0].TopicID != 0 {
		t.Fatalf("health warning topic = %d, want General topic 0", items[0].TopicID)
	}
	if err := service.store.CompleteDelivery(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}
	service.reportHealthRecovered(ctx, "ymessenger.poll", "YMessenger polling recovered")
	service.reportHealthRecovered(ctx, "ymessenger.poll", "YMessenger polling recovered")
	items, err = service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].PayloadJSON, "recovered") {
		t.Fatalf("recovery items=%#v err=%v", items, err)
	}
}

func TestExternalPollHealthIgnoresResumeFlappingUntilContinuouslyAwake(t *testing.T) {
	service := activeAFCService(t)
	service.cfg.ExternalRequestsTopicID = 77
	ctx := context.Background()
	base := time.Date(2026, 8, 26, 7, 0, 0, 0, time.UTC)

	for elapsed := time.Duration(0); elapsed < externalPollResumeGrace+externalPollFailureDelay; elapsed += 30 * time.Second {
		service.now = func() time.Time { return base.Add(elapsed) }
		service.NoteExternalPollResult(ctx, "yandex_messenger", errors.New("dial tcp: no route to host"))
	}
	items, err := service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("resume warm-up queued alerts=%#v err=%v", items, err)
	}

	service.now = func() time.Time { return base.Add(externalPollResumeGrace + externalPollFailureDelay) }
	service.NoteExternalPollResult(ctx, "yandex_messenger", errors.New("dial tcp: no route to host"))
	items, err = service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].PayloadJSON, "network connectivity") {
		t.Fatalf("stable failure alert=%#v err=%v", items, err)
	}
	if items[0].TopicID != service.cfg.ExternalRequestsTopicID {
		t.Fatalf("stable failure topic=%d, want Requests topic %d", items[0].TopicID, service.cfg.ExternalRequestsTopicID)
	}
	if err := service.store.CompleteDelivery(ctx, items[0].ID); err != nil {
		t.Fatal(err)
	}

	for elapsed := time.Duration(0); elapsed < externalPollRecoveryDelay; elapsed += 10 * time.Second {
		service.now = func() time.Time { return base.Add(externalPollResumeGrace + externalPollFailureDelay + elapsed) }
		service.NoteExternalPollResult(ctx, "yandex_messenger", nil)
	}
	items, err = service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("unstable recovery queued alerts=%#v err=%v", items, err)
	}
	service.now = func() time.Time {
		return base.Add(externalPollResumeGrace + externalPollFailureDelay + externalPollRecoveryDelay)
	}
	service.NoteExternalPollResult(ctx, "yandex_messenger", nil)
	items, err = service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].PayloadJSON, "polling recovered") {
		t.Fatalf("stable recovery=%#v err=%v", items, err)
	}
	if items[0].TopicID != service.cfg.ExternalRequestsTopicID {
		t.Fatalf("stable recovery topic=%d, want Requests topic %d", items[0].TopicID, service.cfg.ExternalRequestsTopicID)
	}
}

func TestExternalPollHealthReportsSlowTimeoutCyclesToRequests(t *testing.T) {
	service := activeAFCService(t)
	service.cfg.ExternalRequestsTopicID = 77
	ctx := context.Background()
	base := time.Date(2026, 8, 29, 7, 0, 0, 0, time.UTC)
	step := 91 * time.Second

	for elapsed := time.Duration(0); elapsed <= externalPollResumeGrace+externalPollFailureDelay+2*step; elapsed += step {
		service.now = func() time.Time { return base.Add(elapsed) }
		service.NoteExternalPollResult(ctx, "arcanum_review", context.DeadlineExceeded)
	}

	items, err := service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("slow timeout alert=%#v err=%v, want one warning", items, err)
	}
	if items[0].TopicID != service.cfg.ExternalRequestsTopicID || !strings.Contains(items[0].PayloadJSON, "arcanum_review") {
		t.Fatalf("slow timeout alert=%#v, want Arcanum warning in Requests", items[0])
	}
}

func TestHealthRecoveryDoesNotOvertakeUndeliveredWarning(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	sender := &recordingSender{}
	service.SetSender(sender)

	service.reportHealthFailure(ctx, "ymessenger.poll", "YMessenger polling failed", "network unavailable", "wait")
	service.reportHealthRecovered(ctx, "ymessenger.poll", "YMessenger polling recovered")
	service.processDeliveryBatch(ctx)

	if len(sender.messages) != 0 {
		t.Fatalf("messages=%#v, want closed undelivered episode suppressed", sender.messages)
	}
	backlog, err := service.store.DeliveryQueueBacklog(ctx)
	if err != nil || backlog != 0 {
		t.Fatalf("DeliveryQueueBacklog=%d err=%v, want 0", backlog, err)
	}
}

func TestOlderHealthRecoveryIsSupersededByNewEpisode(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	sender := &recordingSender{}
	service.SetSender(sender)

	service.reportHealthFailure(ctx, "ymessenger.poll", "failed", "first", "wait")
	service.processDeliveryBatch(ctx)
	if len(sender.messages) != 1 {
		t.Fatalf("first warning messages=%#v", sender.messages)
	}
	service.reportHealthRecovered(ctx, "ymessenger.poll", "recovered")
	service.reportHealthFailure(ctx, "ymessenger.poll", "failed", "second", "wait")
	service.processDeliveryBatch(ctx)

	if len(sender.messages) != 2 || !strings.Contains(sender.messages[1].text, "second") {
		t.Fatalf("messages=%#v, want first warning then new warning only", sender.messages)
	}
}

func TestHealthDeliveryFallsBackToGeneralForInvalidTopic(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	sender := &recordingSender{sendErrs: []error{errors.New("telegram sendMessage: 400 Bad Request: message thread not found"), nil}}
	service.SetSender(sender)
	if err := service.store.EnqueueDelivery(ctx, model.DeliveryQueueItem{
		EventID:     "health:test:open",
		ChatKey:     model.ChatKey(-1001, 99),
		ChatID:      -1001,
		TopicID:     99,
		Kind:        "health",
		Status:      model.DeliveryStatusPending,
		AvailableAt: model.NowString(),
		PayloadJSON: `{"text":"health warning"}`,
		CreatedAt:   model.NowString(),
		UpdatedAt:   model.NowString(),
	}); err != nil {
		t.Fatalf("EnqueueDelivery failed: %v", err)
	}

	service.processDeliveryBatch(ctx)

	if len(sender.messages) != 1 || sender.messages[0].topicID != 0 {
		t.Fatalf("fallback messages = %#v, want one successful General delivery", sender.messages)
	}
	backlog, err := service.store.DeliveryQueueBacklog(ctx)
	if err != nil || backlog != 0 {
		t.Fatalf("DeliveryQueueBacklog = %d, err=%v, want 0", backlog, err)
	}
}

func TestStatusShowsHeartbeatDeadLettersAndOpenHealthIncidents(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, "appserver.poll.last_heartbeat_at", time.Now().UTC().Add(-5*time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatalf("SetState(last heartbeat) failed: %v", err)
	}
	service.reportHealthFailure(ctx, "appserver.transport", "transport failed", "socket closed", "run /repair")
	items, err := service.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("ClaimDeliveryBatch = %#v, err=%v", items, err)
	}
	if err := service.store.FailDelivery(ctx, items[0].ID, 5, time.Now().UTC(), "send failed", true); err != nil {
		t.Fatalf("FailDelivery(dead) failed: %v", err)
	}

	status, err := service.StatusSnapshot(ctx, -1001, 0)
	if err != nil {
		t.Fatalf("StatusSnapshot failed: %v", err)
	}
	for _, want := range []string{
		"Dead deliveries: 1",
		"App-server heartbeat:",
		"Open health incidents: appserver.transport",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
}

func TestPollersCommandShowsDurableHealthyFailingStaleAndDisabledState(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	base := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return base }
	if err := service.RegisterExternalPoller(ctx, "stale", 10*time.Second, true); err != nil {
		t.Fatal(err)
	}
	service.NoteExternalPollStarted(ctx, "stale")
	service.NoteExternalPollResult(ctx, "stale", nil)
	service.now = func() time.Time { return base.Add(time.Minute) }
	if err := service.RegisterExternalPoller(ctx, "healthy", 15*time.Second, true); err != nil {
		t.Fatal(err)
	}
	service.NoteExternalPollStarted(ctx, "healthy")
	service.NoteExternalPollResult(ctx, "healthy", nil)
	if err := service.RegisterExternalPoller(ctx, "failing", time.Minute, true); err != nil {
		t.Fatal(err)
	}
	service.NoteExternalPollStarted(ctx, "failing")
	service.NoteExternalPollResult(ctx, "failing", errors.New("token expired"))
	if err := service.RegisterExternalPoller(ctx, "disabled", time.Minute, false); err != nil {
		t.Fatal(err)
	}

	response, err := service.handleCommand(ctx, 123456789, 0, "/pollers", 0)
	if err != nil || response == nil {
		t.Fatalf("pollers response=%#v err=%v", response, err)
	}
	for _, want := range []string{"🟢 healthy · healthy", "🔴 failing · failing", "⚪ disabled · disabled", "🟠 stale · stale", "token expired"} {
		if !strings.Contains(strings.ToLower(response.Text), want) {
			t.Fatalf("pollers missing %q:\n%s", want, response.Text)
		}
	}
}

func TestSharedAppServerHeartbeatFailureTruthfullyResetsAFCWithoutReplay(t *testing.T) {
	service := activeAFCService(t)
	service.cfg.AppServerMode = "websocket"
	service.lastPollHeartbeat = time.Time{}
	poll := &stubSession{threadListErr: errors.New("websocket is half-open")}
	service.mu.Lock()
	service.poll = poll
	service.pollConnected = true
	service.mu.Unlock()

	service.heartbeatPollSession(context.Background())
	state, err := service.store.GetAFCState(context.Background())
	if err != nil || state.State != model.AFCStateOff {
		t.Fatalf("AFC state=%#v err=%v, want off", state, err)
	}
	service.mu.RLock()
	connected := service.pollConnected
	service.mu.RUnlock()
	if connected {
		t.Fatal("poll session still reported connected after failed heartbeat")
	}
	for _, topic := range []int64{11, 12} {
		stored, getErr := service.store.GetActiveAFCTopic(context.Background(), -1001, topic)
		if getErr != nil {
			t.Fatal(getErr)
		}
		if stored != nil {
			t.Fatalf("topic %d remained active after transport reset: %#v", topic, stored)
		}
	}
	items, err := service.store.ClaimDeliveryBatch(context.Background(), 10)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].PayloadJSON, "AFC was reset to off") {
		t.Fatalf("health warning=%#v err=%v", items, err)
	}
}
