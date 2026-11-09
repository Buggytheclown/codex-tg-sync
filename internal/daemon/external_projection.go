package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func (s *Service) externalTelegramProjection(ctx context.Context, r model.ExternalLaunchRequest) (storage.ThreadTelegramProjection, error) {
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return storage.ThreadTelegramProjection{}, err
	}
	return s.store.ThreadTelegramProjection(ctx, state.SessionID, r.ThreadID)
}
func (s *Service) handleExternalTelegramRetry(ctx context.Context, r model.ExternalLaunchRequest, route *model.CallbackRoute) (*DirectResponse, error) {
	var payload struct {
		Projection storage.ThreadTelegramProjection `json:"projection"`
	}
	if err := json.Unmarshal([]byte(route.PayloadJSON), &payload); err != nil {
		return nil, err
	}
	p := payload.Projection
	if p.ThreadID != r.ThreadID || p.ThreadID == "" {
		return &DirectResponse{CallbackText: "This recovery action is stale."}, nil
	}
	// Consume any already-arrived unambiguous confirmations before authorizing create.
	if err := s.reconcileTopicCreations(ctx); err != nil {
		return nil, err
	}
	op, changed, err := s.store.ClaimTelegramProjectionRetry(ctx, route.Token, randomToken(), p)
	if err != nil {
		return nil, err
	}
	if !changed {
		s.wakeExternalLaunchRenderer()
		return &DirectResponse{CallbackText: "This recovery action is stale or already resolved."}, nil
	}
	_ = s.store.InvalidateExternalProjection(ctx, r.ThreadID)
	s.wakeExternalLaunchRenderer()
	if op != nil {
		s.spawn(ctx, func(ctx context.Context) {
			s.runExternalTopicRetry(ctx, *op)
			s.enqueueSyncDelivery(r.ThreadID)
			s.wakeExternalLaunchRenderer()
		})
	} else {
		s.enqueueSyncDelivery(r.ThreadID)
	}
	return &DirectResponse{CallbackText: "Retrying Telegram; Codex work is unchanged."}, nil
}
func (s *Service) runExternalTopicRetry(ctx context.Context, op storage.TopicCreation) {
	defer s.wakeExternalLaunchRenderer()
	defer s.wakeSyncReconcile()
	forum := s.getSyncForum()
	if forum == nil {
		_ = s.store.TopicCreationResult(ctx, op.ID, 0, "failed")
		_ = s.store.InvalidateExternalProjection(ctx, op.ThreadID)
		return
	}
	ctx = context.WithValue(ctx, syncCreationAdmissionKey{}, func() error {
		state, err := s.store.GetSyncState(ctx)
		if err != nil {
			return err
		}
		if state.SessionID != op.SessionID || state.State != model.SyncStateActive {
			return errSyncCommandNotSent
		}
		p, err := s.store.ThreadTelegramProjection(ctx, op.SessionID, op.ThreadID)
		if err != nil {
			return err
		}
		if p.State == "connected" || p.AttemptID != op.ID {
			return fmt.Errorf("topic creation superseded")
		}
		return nil
	})
	id, err := forum.CreateLaunchTopic(ctx, op.Title)
	_, _ = s.finishTopicCreation(ctx, op, id, err)
	_ = s.store.InvalidateExternalProjection(ctx, op.ThreadID)
	s.enqueueSyncDelivery(op.ThreadID)
}
func (s *Service) attachExternalTopicOwnerLocked(ctx context.Context, session, thread string) {
	owned, ok := s.boardContinuationLeases[thread]
	if !ok {
		return
	}
	lease, ok := s.syncLeases[thread]
	if !ok || lease != owned.Lease {
		return
	}
	state := model.SyncTurnActive
	if owned.TurnID == "" {
		state = model.SyncTurnUnknown
	}
	_ = s.store.AttachExternalTopicOwner(ctx, session, thread, owned.TurnID, state, lease.Generation)
}

func (s *Service) ensureExternalTopic(ctx context.Context, threadID string) error {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	ctx = context.WithValue(ctx, syncCreationLockKey{}, true)
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return err
	}
	if state.State != model.SyncStateActive {
		return errSyncCommandNotSent
	}
	topic, err := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, threadID)
	if err != nil {
		return err
	}
	if topic != nil {
		s.attachExternalTopicOwnerLocked(ctx, state.SessionID, threadID)
		return nil
	}
	thread, err := s.store.GetThread(ctx, threadID)
	if err != nil {
		return err
	}
	if thread == nil {
		return fmt.Errorf("no saved thread for Telegram recovery")
	}
	forum := s.getSyncForum()
	if forum == nil {
		op, fresh, err := s.prepareTopicCreation(ctx, storage.TopicCreation{SessionID: state.SessionID, ChatID: state.ChatID, Target: "thread:" + threadID, ThreadID: threadID, Title: syncTopicTitle(*thread)})
		if err != nil {
			return err
		}
		if fresh {
			_ = s.store.TopicCreationResult(ctx, op.ID, 0, "failed")
			_ = s.store.InvalidateExternalProjection(ctx, threadID)
		}
		return fmt.Errorf("Telegram transport is unavailable")
	}
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		return err
	}
	rank := 1
	for _, t := range topics {
		if t.Rank >= rank {
			rank = t.Rank + 1
		}
	}
	_, err = s.createSyncThreadTopic(ctx, forum, state.SessionID, state.ChatID, rank, *thread)
	if err == nil {
		s.attachExternalTopicOwnerLocked(ctx, state.SessionID, threadID)
	}
	return err
}
