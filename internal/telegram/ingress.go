package telegram

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/daemon"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func (b *Bot) admittedIngress(update Update) bool {
	if b.service == nil || b.me == nil {
		return false
	}
	if c := update.CallbackQuery; c != nil {
		return c.From != nil && c.Message != nil && b.service.IsAllowed(c.From.ID, c.Message.Chat.ID)
	}
	m := update.Message
	if m == nil {
		m = update.EditedMessage
	}
	return m != nil && m.From != nil && b.service.IsAllowed(m.From.ID, m.Chat.ID)
}
func isIngressOff(update Update) bool {
	m := update.Message
	if m == nil || m.MessageThreadID > 1 {
		return false
	}
	f := strings.Fields(strings.ToLower(strings.TrimSpace(m.Text)))
	if len(f) < 2 {
		return false
	}
	f[0], _, _ = strings.Cut(f[0], "@")
	return f[0] == "/sync" && f[1] == "off" && (len(f) == 2 || len(f) == 3 && f[2] == "--force")
}
func (b *Bot) persistIngress(ctx context.Context, update Update) error {
	if !b.admittedIngress(update) {
		return nil
	}
	m := update.Message
	if m == nil {
		m = update.EditedMessage
	}
	if m != nil {
		if err := b.service.RejectUnboundTelegramIngress(ctx, m.Chat.ID, m.MessageThreadID, m.MessageID, m.From.ID); err != nil {
			return err
		}
	}
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	return b.service.RecordTelegramIngress(ctx, storage.TelegramIngress{BotID: b.me.ID, UpdateID: update.UpdateID, Payload: string(payload)}, isIngressOff(update))
}
func (b *Bot) executeIngress(ctx context.Context, item *storage.TelegramIngress) error {
	var update Update
	ctx = daemon.TelegramIngressContext(ctx, item.BotID, item.UpdateID)
	if err := json.Unmarshal([]byte(item.Payload), &update); err != nil {
		return err
	}
	if isIngressOff(update) {
		ctx = daemon.TelegramOffSession(ctx, item.SessionID)
	}
	// Side effects have their own durable intents/receipts. A delivery failure
	// never authorizes replay of the accepted command.
	err := b.handleUpdate(ctx, update)
	if err != nil {
		b.logger.Printf("telegram update %d failed: %s", update.UpdateID, sanitizeTelegramLogError(err))
	}
	return b.service.FinishTelegramIngress(ctx, item.BotID, item.UpdateID)
}
func (b *Bot) ingressLoop(ctx context.Context, wake <-chan struct{}) {
	for {
		if ctx.Err() != nil {
			return
		}
		item, err := b.service.ClaimTelegramIngress(ctx, b.me.ID, 0)
		if err == nil && item != nil {
			if err = b.executeIngress(ctx, item); err != nil {
				// Retry local completion; do not repeat side effects in this process.
				for ctx.Err() == nil {
					if b.service.FinishTelegramIngress(ctx, item.BotID, item.UpdateID) == nil {
						break
					}
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Second):
					}
				}
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-wake:
		case <-time.After(time.Second):
		}
	}
}
