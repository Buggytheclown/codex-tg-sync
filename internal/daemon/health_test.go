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
