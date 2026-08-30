package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestTelegramSurfaceRejectsOutOfScopeMessagesAndCallbacksBeforeStorage(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001

	if err := service.store.Close(); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		chatID int64
		userID int64
	}{
		{name: "wrong_chat", chatID: 123456789, userID: 123456789},
		{name: "wrong_user", chatID: -1001, userID: 987654321},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response, err := service.HandleMessageWithID(context.Background(), test.chatID, 1, 1, test.userID, "retired prompt", 0)
			if err != nil || response != nil {
				t.Fatalf("message response=%#v err=%v, want ignored without storage", response, err)
			}
			response, err = service.HandleCallback(context.Background(), test.chatID, 1, 1, test.userID, "retired-token")
			if err != nil || response != nil {
				t.Fatalf("callback response=%#v err=%v, want ignored before route lookup", response, err)
			}
		})
	}
}

func TestServiceStartRetiresUnsupportedTelegramDeliveries(t *testing.T) {
	service := newTestService(t)
	service.pollFactory = func() Session { return &stubSession{} }
	service.cfg.IndexRefreshInterval = time.Hour
	ctx := context.Background()
	if err := service.store.EnqueueDelivery(ctx, model.DeliveryQueueItem{
		EventID: "retired-on-start", ChatKey: "retired-on-start", ChatID: -1001,
		Kind: "observer", Status: model.DeliveryStatusPending, PayloadJSON: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := service.store.DeliveryStatusForEvent(ctx, "retired-on-start", "retired-on-start")
	if err != nil {
		t.Fatal(err)
	}
	if status != model.DeliveryStatusSuperseded {
		t.Fatalf("delivery status = %q, want %q", status, model.DeliveryStatusSuperseded)
	}
}

func TestAFCTurnStartIgnoresRetiredTelegramSettings(t *testing.T) {
	service := newTestService(t)
	ctx := context.Background()
	if err := service.store.SetState(ctx, "codex.model", "retired-model"); err != nil {
		t.Fatal(err)
	}
	if err := service.store.SetState(ctx, "codex.reasoning_effort", "high"); err != nil {
		t.Fatal(err)
	}

	options := service.turnStartOptions(ctx, collaborationModeDefault, &model.Thread{PreferredModel: "thread-model"})
	if options.Model != "thread-model" {
		t.Fatalf("Model = %q, want thread preferred model", options.Model)
	}
	if options.ReasoningEffort != "" {
		t.Fatalf("ReasoningEffort = %q, want App Server default", options.ReasoningEffort)
	}
	if options.ApprovalPolicy != telegramApprovalPolicy || options.ApprovalsReviewer != telegramApprovalsReviewer || options.SandboxMode != telegramSandboxMode {
		t.Fatalf("permissions = %#v, want safe Telegram permissions", options)
	}
}
