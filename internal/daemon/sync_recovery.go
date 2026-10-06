package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// All runtime recovery fields are guarded by syncMu. SQLite remains the source
// of user enabled state; these fields only gate access and initial catch-up.
func (s *Service) recoverSyncRuntimeLocked(ctx context.Context) error {
	cards, err := s.store.ListSyncActionCards(ctx)
	if err != nil {
		return err
	}
	if err := s.store.RecoverSyncWriterState(ctx); err != nil {
		return err
	}
	s.syncExpiredCards = append(s.syncExpiredCards, cards...)
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return err
	}
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		return err
	}
	s.syncRecoveryPending = map[string]bool{}
	s.syncDrainActive = map[string]bool{}
	s.syncResumeDraining = state.State == model.SyncStateDraining
	for _, topic := range topics {
		if topic.TelegramState == model.SyncTopicConnected {
			s.syncRecoveryPending[topic.ThreadID] = true
		}
	}
	if state.State != model.SyncStateActive {
		_ = s.syncWriter.BeginDrain()
	}
	return nil
}

func (s *Service) syncAccessErrorLocked(ctx context.Context) error {
	s.mu.RLock()
	started := s.started
	s.mu.RUnlock()
	if started && !s.syncBotReady {
		return errors.New("Sync Telegram startup is pending")
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return err
	}
	if state.SessionID != "" && state.ChatID != s.cfg.SyncGroupID {
		return errors.New("saved Sync session belongs to a different group")
	}
	if started && s.syncValidatedSession != state.SessionID {
		return errors.New("Sync group and Control verification is pending")
	}
	return nil
}

func (s *Service) syncMutationErrorLocked(ctx context.Context) error {
	if err := s.syncAccessErrorLocked(ctx); err != nil {
		return err
	}
	if s.syncWriter.Snapshot().State == appserver.WriterClosing {
		return errors.New("previous Sync writer close is unconfirmed; mutations are unavailable")
	}
	return nil
}

func (s *Service) validateSavedSyncLocked(ctx context.Context) error {
	s.mu.RLock()
	started := s.started
	s.mu.RUnlock()
	if !started {
		return s.syncAccessErrorLocked(ctx)
	}
	if !s.syncBotReady {
		return errors.New("Sync Telegram startup is pending")
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return err
	}
	if state.SessionID != "" && state.ChatID != s.cfg.SyncGroupID {
		return errors.New("saved Sync session belongs to a different group")
	}
	if s.syncValidatedSession == state.SessionID {
		s.expireSyncCardsLocked(ctx)
		return nil
	}
	forum := s.getSyncForum()
	if forum == nil || len(s.cfg.AllowedUserIDs) != 1 {
		return errors.New("Sync forum admission is unavailable")
	}
	if err := forum.ValidateSyncGroup(ctx, s.cfg.AllowedUserIDs[0]); err != nil {
		return fmt.Errorf("validate Sync group: %w", err)
	}
	if err := forum.PrepareSyncControl(ctx); err != nil {
		return fmt.Errorf("prepare Sync Control: %w", err)
	}
	s.syncValidatedSession = state.SessionID
	s.expireSyncCardsLocked(ctx)
	return nil
}

func (s *Service) expireSyncCardsLocked(ctx context.Context) {
	forum := s.getSyncForum()
	if forum == nil {
		return
	}
	seen := map[int64]bool{}
	for _, card := range s.syncExpiredCards {
		var payload map[string]any
		if json.Unmarshal([]byte(card.PayloadJSON), &payload) != nil || syncPayloadString(payload, "session_id") != s.syncValidatedSession || seen[card.TelegramMessageID] {
			continue
		}
		seen[card.TelegramMessageID] = true
		header := syncApprovalHeader
		if card.Action == "sync_user_input" {
			header = syncInputHeader
		}
		if err := forum.EditSyncMessage(ctx, syncPayloadInt64(payload, "topic_id"), card.TelegramMessageID, model.RenderedMessage{Text: header + "\nExpired after reconnect. Use Codex to handle any remaining request."}, model.SendOptions{}); err != nil {
			s.logLifecycle("sync_expired_card_edit_failed", lifecycleFields{"error": err})
		}
	}
	s.syncExpiredCards = nil
}

func (s *Service) finishResumedSyncDrainLocked(ctx context.Context, state model.SyncState) {
	if !s.syncResumeDraining || state.State != model.SyncStateDraining || len(s.syncRecoveryPending) > 0 || len(s.syncDrainActive) > 0 {
		return
	}
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil || hasUnfinishedSyncTopics(topics) {
		return
	}
	drafts, err := s.store.ListSyncTopicDrafts(ctx, state.SessionID)
	if err != nil || hasUnfinishedSyncDrafts(drafts) {
		return
	}
	writer := s.syncWriter.Snapshot()
	if writer.Starting+writer.Active+writer.Unknown > 0 {
		return
	}
	if _, err := s.store.MarkSyncOff(ctx, state.SessionID); err == nil {
		s.wakeSyncCleanup()
	}
}

func (s *Service) resumedSyncDrainPendingLocked() bool {
	return s.syncResumeDraining && (len(s.syncRecoveryPending) > 0 || len(s.syncDrainActive) > 0)
}
