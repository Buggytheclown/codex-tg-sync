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

func TestDaemonHeartbeatFailureTruthfullyResetsAFCWithoutReplay(t *testing.T) {
	service := activeAFCService(t)
	service.cfg.AppServerMode = "daemon"
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
