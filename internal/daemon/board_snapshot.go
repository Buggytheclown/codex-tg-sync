package daemon

import (
	"context"
	"encoding/json"

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
	poll, connected := s.poll, s.pollConnected
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
		// ThreadResume is a mutation: it can update activity and cause Sync to
		// discover a historical chat as recent. Passive board targets are read-only;
		// the existing periodic producer reconciles them without subscribing.
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

// A previously observed old terminal turn cannot become fresh merely because
// ThreadResume changed chat metadata. This narrow discovery fence applies only
// to unbound board targets; current active/waiting status and unknown timing keep
// the normal Sync freshness policy. Compact turn timing is observation history.
func (s *Service) boardDiscoveryActivityRecent(ctx context.Context, thread model.Thread, cutoffUnix int64) (bool, error) {
	if threadLooksActiveForPolling(thread) {
		return true, nil
	}
	stored, err := s.store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		return false, err
	}
	if stored == nil || len(stored.CompactJSON) == 0 {
		return true, nil
	}
	var snapshot appserver.ThreadReadSnapshot
	if json.Unmarshal(stored.CompactJSON, &snapshot) != nil || !isTerminalStatus(snapshot.LatestTurnStatus) {
		return true, nil
	}
	observedTerminalAt := parseTime(model.TimeString(snapshot.LatestTurnUpdatedAt))
	if observedTerminalAt.IsZero() {
		return true, nil
	}
	return observedTerminalAt.Unix() >= cutoffUnix, nil
}
