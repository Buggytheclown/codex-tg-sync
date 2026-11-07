package daemon

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

type telegramOffSessionKey struct{}

func (s *Service) RecordTelegramIngress(ctx context.Context, item storage.TelegramIngress, off bool) error {
	return s.store.RecordTelegramIngress(ctx, item, off)
}
func (s *Service) ClaimTelegramIngress(ctx context.Context, botID, updateID int64) (*storage.TelegramIngress, error) {
	return s.store.ClaimTelegramIngress(ctx, botID, updateID)
}
func (s *Service) FinishTelegramIngress(ctx context.Context, botID, updateID int64) error {
	return s.store.FinishTelegramIngress(ctx, botID, updateID)
}
func (s *Service) RecoverTelegramIngress(ctx context.Context, botID int64) error {
	return s.store.RecoverTelegramIngress(ctx, botID)
}
func TelegramOffSession(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, telegramOffSessionKey{}, sessionID)
}

// Freeze rejection at receipt time so an unbound message cannot start work if
// a late creation event establishes the topic while its inbox row is waiting.
func (s *Service) RejectUnboundTelegramIngress(ctx context.Context, chatID, topicID, messageID, userID int64) error {
	if !s.IsAllowed(userID, chatID) || isSyncControlTopic(topicID) {
		return nil
	}
	topic, err := s.store.GetActiveSyncTopic(ctx, chatID, topicID)
	if err != nil || topic != nil {
		return err
	}
	draft, err := s.store.GetActiveSyncTopicDraft(ctx, chatID, topicID)
	if err != nil || draft != nil && draft.State != "external" {
		return err
	}
	return s.store.RejectUnboundSyncMessage(ctx, chatID, topicID, messageID)
}

type telegramIngressIdentityKey struct{}

func TelegramIngressContext(ctx context.Context, botID, updateID int64) context.Context {
	return context.WithValue(ctx, telegramIngressIdentityKey{}, []int64{botID, updateID})
}
