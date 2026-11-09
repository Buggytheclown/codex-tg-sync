package daemon

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// Presentation deletion must not delete the owner of a running command.
func (s *Service) reconcileDetachedSyncWorkLocked(ctx context.Context) {
	topics, err := s.store.ListDetachedSyncWork(ctx, s.cfg.SyncGroupID)
	if err != nil {
		return
	}
	for _, topic := range topics {
		s.mu.RLock()
		process := s.poll
		s.mu.RUnlock()
		lease, owned := s.syncLeases[topic.ThreadID]
		if owned {
			process = lease.Process
		}
		if process == nil {
			continue
		}
		current, err := readAuthoritativeSyncSnapshot(ctx, process, topic.ThreadID)
		if err != nil {
			continue
		}
		previous, err := s.store.GetSnapshot(ctx, topic.ThreadID)
		if err != nil {
			continue
		}
		if s.applyTelegramOriginTerminalGate(ctx, "sync_detached", &current, previous) {
			continue
		}
		observed, _, err := s.persistSyncSnapshot(ctx, topic, current)
		if err != nil {
			continue
		}
		s.queueExternalReplyFromSnapshot(ctx, observed)
		superseded := false
		if owned {
			if topic.ActiveTurnState != model.SyncTurnActive || topic.ActiveTurnID == "" {
				continue
			}
			if current.LatestTurnID != topic.ActiveTurnID {
				superseded = turnIDAfter(current.LatestTurnID, topic.ActiveTurnID) && s.externalTerminalIsDurable(ctx, topic.ThreadID)
				if !superseded {
					continue
				}
			}
		}
		if !superseded && !isTerminalStatus(current.LatestTurnStatus) {
			continue
		}
		if !s.externalTerminalIsDurable(ctx, topic.ThreadID) {
			continue
		}
		if err := s.store.ResolveDetachedSyncTerminal(ctx, topic); err != nil {
			s.logLifecycle("sync_detached_terminal_failed", lifecycleFields{"error": err})
			continue
		}
		if owned {
			delete(s.syncLeases, topic.ThreadID)
			delete(s.boardContinuationLeases, topic.ThreadID)
			s.unsubscribeSyncWriterLeaseLocked(ctx, lease)
			if err := s.syncWriter.MarkTerminal(lease); err != nil {
				s.logLifecycle("sync_detached_writer_release_failed", lifecycleFields{"error": err})
			}
		}

	}
}
