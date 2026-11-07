package daemon

import (
	"context"
	"errors"
	"fmt"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func (s *Service) prepareTopicCreation(ctx context.Context, op storage.TopicCreation) (storage.TopicCreation, bool, error) {
	if err := s.store.ImportLegacyTopicCreations(ctx); err != nil {
		return op, false, err
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return op, false, err
	}
	if state.SessionID != op.SessionID || (state.State != model.SyncStateActive && state.State != model.SyncStateActivating) {
		return op, false, NewSyncForumFailure(SyncForumFailureDefinitive, errSyncCommandNotSent)
	}
	op.ID = randomToken()
	fresh, err := s.store.PrepareTopicCreation(ctx, op)
	return op, fresh, err
}

func (s *Service) finishTopicCreation(ctx context.Context, op storage.TopicCreation, id int64, createErr error) (int64, error) {
	if createErr != nil {
		outcome := "unknown"
		var failure *SyncForumFailure
		if errors.As(createErr, &failure) && (failure.Kind == SyncForumFailureDefinitive || failure.Kind == SyncForumFailureRetryable) {
			outcome = "failed"
		}
		if err := s.store.TopicCreationResult(ctx, op.ID, 0, outcome); err != nil {
			return 0, errors.Join(createErr, err)
		}
		return 0, createErr
	}
	if id <= 0 {
		err := errors.New("Telegram creation returned no topic id")
		persistErr := s.store.TopicCreationResult(ctx, op.ID, 0, "unknown")
		return 0, errors.Join(err, persistErr)
	}
	if err := s.store.TopicCreationResult(ctx, op.ID, id, "confirmed"); err != nil {
		return 0, err
	}
	op.TopicID = id
	if err := s.store.MaterializeTopicCreation(ctx, op); err != nil {
		return 0, err
	}
	return id, nil
}

// RecordSyncTopicCreated is called only after the Bot adapter has authenticated
// a service event from its own bot in the configured forum.
func (s *Service) RecordSyncTopicCreated(ctx context.Context, chatID, topicID, updateID int64, title string) error {
	if chatID != s.cfg.SyncGroupID || topicID <= 1 {
		return fmt.Errorf("invalid topic-created service event")
	}
	if err := s.store.RecordTopicCreated(ctx, chatID, topicID, updateID, title); err != nil {
		return err
	}
	s.wakeSyncReconcile()
	s.wakeSyncCleanup()
	return nil
}

func (s *Service) reconcileTopicCreations(ctx context.Context) error {
	ops, err := s.store.PendingTopicCreations(ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.TopicID > 0 {
			if err := s.store.MaterializeTopicCreation(ctx, op); err != nil {
				return err
			}
		}
	}
	ids, err := s.store.ResumeConfirmedTopicLaunches(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		s.startExternalLaunchDispatch(id)
	}
	return nil
}

type syncCreationAdmissionKey struct{}
type syncCreationLockKey struct{}

// CheckSyncTopicCreation runs immediately before a queued raw create attempt.
func CheckSyncTopicCreation(ctx context.Context) error {
	if check, ok := ctx.Value(syncCreationAdmissionKey{}).(func() error); ok {
		return check()
	}
	return nil
}

func (s *Service) createTopicIO(ctx context.Context, session string, create func(context.Context) (int64, error)) (int64, error) {
	if held, _ := ctx.Value(syncCreationLockKey{}).(bool); !held {
		return create(ctx)
	}
	ctx = context.WithValue(ctx, syncCreationAdmissionKey{}, func() error {
		s.syncMu.Lock()
		defer s.syncMu.Unlock()
		state, err := s.store.GetSyncState(ctx)
		if err != nil {
			return err
		}
		if state.SessionID != session || (state.State != model.SyncStateActive && state.State != model.SyncStateActivating) {
			return errSyncCommandNotSent
		}
		return nil
	})
	s.unlockSyncDispatch(ctx)
	id, err := create(ctx)
	s.lockSyncDispatch(ctx)
	return id, err
}
