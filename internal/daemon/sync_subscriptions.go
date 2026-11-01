package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func syncSubscriptionStatus(current appserver.ThreadReadSnapshot) string {
	if current.RuntimeStatus != "" {
		return strings.ToLower(current.RuntimeStatus)
	}
	return strings.ToLower(current.Thread.Status)
}

func syncSnapshotNeedsSubscription(current appserver.ThreadReadSnapshot) bool {
	// Runtime idle/notLoaded overrides an unfinished turn left in persisted history.
	switch syncSubscriptionStatus(current) {
	case "idle", "notloaded", "unknown", "":
		return false
	}
	return terminalGateSnapshotNeedsHotPolling(&current)
}

func (s *Service) syncSubscriptionPollLocked() (Session, uint64) {
	s.mu.RLock()
	poll, connected, generation := s.poll, s.pollConnected, s.pollGeneration
	s.mu.RUnlock()
	if !s.usesSharedAppServer() || !connected || poll == nil {
		return nil, generation
	}
	if s.syncSubscribedPollGeneration != generation {
		s.syncSubscribedThreads = map[string]bool{}
		s.syncSubscribedPollGeneration = generation
	}
	return poll, generation
}

func (s *Service) updateSyncPollSubscriptionLocked(ctx context.Context, topic model.SyncTopic, current appserver.ThreadReadSnapshot) error {
	poll, generation := s.syncSubscriptionPollLocked()
	if poll == nil {
		return nil
	}
	if _, owned := s.syncLeases[topic.ThreadID]; owned {
		if topic.ActiveTurnState == model.SyncTurnStarting || topic.ActiveTurnState == model.SyncTurnUnknown {
			return nil
		}
		return s.unsubscribeSyncPollThreadLocked(ctx, poll, topic.ThreadID)
	}
	// Metadata and the last turn are separate reads; resolve contradictory activity before detaching.
	status := syncSubscriptionStatus(current)
	if (status == "idle" || status == "notloaded") && terminalGateSnapshotNeedsHotPolling(&current) {
		payload, err := poll.ThreadRead(ctx, topic.ThreadID, false)
		if err != nil {
			s.logLifecycle("sync_subscription_recheck_failed", lifecycleFields{"thread_id": topic.ThreadID, "error": err})
			return err
		}
		metadata := appserver.SnapshotFromThreadRead(payload)
		if metadata.Thread.ID != topic.ThreadID {
			return fmt.Errorf("subscription recheck returned a different thread")
		}
		current.RuntimeStatus = metadata.RuntimeStatus
		current.Thread.Status = metadata.Thread.Status
	}
	if syncSnapshotNeedsSubscription(current) {
		if !s.subscribeSyncThreadLocked(ctx, poll, generation, topic.ThreadID) {
			return fmt.Errorf("could not subscribe to active thread %q", topic.ThreadID)
		}
		return nil
	}
	status = syncSubscriptionStatus(current)
	if status == "unknown" || status == "" {
		return nil
	}
	if !isTerminalStatus(current.LatestTurnStatus) && status != "idle" && status != "notloaded" && !isTerminalStatus(status) {
		return nil // Unknown state is not evidence that an existing subscription is idle.
	}
	return s.unsubscribeSyncPollThreadLocked(ctx, poll, topic.ThreadID)
}

func (s *Service) unsubscribeSyncPollThreadLocked(ctx context.Context, poll Session, threadID string) error {
	if _, subscribed := s.syncSubscribedThreads[threadID]; !subscribed {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := poll.ThreadUnsubscribe(ctx, threadID); err != nil {
		s.logLifecycle("sync_unsubscribe_failed", lifecycleFields{"thread_id": threadID, "role": "poll", "error": err})
		return err
	}
	delete(s.syncSubscribedThreads, threadID)
	return nil
}

func (s *Service) pruneSyncPollSubscriptionsLocked(ctx context.Context) {
	poll, _ := s.syncSubscriptionPollLocked()
	if poll == nil || len(s.syncSubscribedThreads) == 0 {
		return
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return
	}
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	if state.State == model.SyncStateActive || state.State == model.SyncStateDraining {
		for _, topic := range topics {
			if topic.TelegramState == model.SyncTopicConnected {
				keep[topic.ThreadID] = true
			}
		}
	}
	for id := range s.syncSubscribedThreads {
		if _, owned := s.syncLeases[id]; !keep[id] && !owned {
			_ = s.unsubscribeSyncPollThreadLocked(ctx, poll, id)
		}
	}
}

func (s *Service) unsubscribeSyncWriterLeaseLocked(ctx context.Context, lease appserver.WriterLease[Session]) {
	if lease.ThreadID == "" {
		return
	}
	err := ctx.Err()
	if err == nil {
		err = lease.Process.ThreadUnsubscribe(ctx, lease.ThreadID)
	}
	if err != nil {
		if s.syncPendingUnsubscribes == nil {
			s.syncPendingUnsubscribes = map[string]appserver.WriterLease[Session]{}
		}
		s.syncPendingUnsubscribes[lease.ThreadID] = lease
		s.logLifecycle("sync_unsubscribe_failed", lifecycleFields{"thread_id": lease.ThreadID, "role": "writer", "error": err})
		return
	}
	delete(s.syncPendingUnsubscribes, lease.ThreadID)
}

func (s *Service) retrySyncWriterUnsubscribesLocked(ctx context.Context) {
	writer := s.syncWriter.Snapshot()
	for id, lease := range s.syncPendingUnsubscribes {
		if ctx.Err() != nil {
			return
		}
		if writer.State == appserver.WriterStopped || writer.Generation != lease.Generation {
			delete(s.syncPendingUnsubscribes, id)
			continue
		}
		if active, owned := s.syncLeases[id]; owned && active.Process == lease.Process && active.Generation == lease.Generation {
			delete(s.syncPendingUnsubscribes, id)
			continue
		}
		if writer.State == appserver.WriterRunning {
			s.unsubscribeSyncWriterLeaseLocked(ctx, lease)
		}
	}
}

func (s *Service) closeSyncSubscriptionsLocked(ctx context.Context) {
	if poll, _ := s.syncSubscriptionPollLocked(); poll != nil {
		for id := range s.syncSubscribedThreads {
			if ctx.Err() != nil {
				return
			}
			_ = s.unsubscribeSyncPollThreadLocked(ctx, poll, id)
		}
	}
	s.retrySyncWriterUnsubscribesLocked(ctx)
	for _, lease := range s.syncLeases {
		if ctx.Err() != nil {
			return
		}
		s.unsubscribeSyncWriterLeaseLocked(ctx, lease)
	}
}
