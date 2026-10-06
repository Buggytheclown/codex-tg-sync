package daemon

import (
	"context"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) boardTracksThreadLocked(ctx context.Context, threadID string) bool {
	ids, err := s.store.ListTrackedTaskThreadIDs(ctx)
	if err != nil {
		return false
	}
	for _, id := range ids {
		if id == threadID {
			return true
		}
	}
	return false
}

// This is the board part of the existing periodic producer, never a second
// polling loop. Connected topics were already read by reconcileSyncLocked.
func (s *Service) reconcileBoardThreadsLocked(ctx context.Context) {
	ids, err := s.store.ListTrackedTaskThreadIDs(ctx)
	if err != nil || len(ids) == 0 {
		return
	}
	s.mu.RLock()
	poll, connected, generation := s.poll, s.pollConnected, s.pollGeneration
	s.mu.RUnlock()
	if poll == nil || !connected {
		return
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return
	}
	topicProducerAvailable := (state.State == model.SyncStateActive || state.State == model.SyncStateDraining) && s.syncAccessErrorLocked(ctx) == nil && s.getSyncForum() != nil
	for _, id := range ids {
		if topicProducerAvailable {
			topic, err := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, id)
			if err != nil {
				continue
			}
			if topic != nil {
				continue
			}
		}
		if !s.subscribeSyncThreadLocked(ctx, poll, generation, id) {
			continue
		}
		var reader Session = poll
		if lease, ok := s.syncLeases[id]; ok {
			reader = lease.Process
		}
		current, err := readAuthoritativeSyncSnapshot(ctx, reader, id)
		if err != nil {
			continue
		}
		s.processBoardSnapshotLocked(ctx, current)
	}
}

func (s *Service) processBoardSnapshotLocked(ctx context.Context, current appserver.ThreadReadSnapshot) {
	// persistSyncSnapshot owns ADR-034's terminal fence for both presentations.
	observed, _ := s.persistSyncSnapshot(ctx, model.SyncTopic{ThreadID: current.Thread.ID}, current)
	s.queueExternalReplyFromSnapshot(ctx, observed)
	s.releaseBoardContinuationLeaseLocked(ctx, observed)
}

// Continuation ownership is scoped to the live writer lease and the exact
// dispatched turn. Request history is never evidence about a later lease.
type boardContinuationLease struct {
	Lease     appserver.WriterLease[Session]
	RequestID string
	TurnID    string
}

func (s *Service) releaseBoardContinuationLeaseLocked(ctx context.Context, current appserver.ThreadReadSnapshot) {
	lease, ok := s.syncLeases[current.Thread.ID]
	owned, continuation := s.boardContinuationLeases[current.Thread.ID]
	if !ok || !continuation || owned.Lease != lease {
		return
	}
	if (current.LatestTurnID == owned.TurnID && isTerminalStatus(current.LatestTurnStatus)) || turnIDAfter(current.LatestTurnID, owned.TurnID) {
		delete(s.syncLeases, current.Thread.ID)
		delete(s.boardContinuationLeases, current.Thread.ID)
		_ = s.syncWriter.MarkTerminal(lease)
	}
}
