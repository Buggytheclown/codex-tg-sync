package daemon

import (
	"context"
	"encoding/json"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) boardTracksThreadLocked(ctx context.Context, threadID string) bool {
	ids, err := s.executionThreadIDs(ctx)
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
	ids, err := s.executionThreadIDs(ctx)
	if err != nil || len(ids) == 0 {
		return
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if poll == nil || !connected {
		return
	}
	for _, id := range ids {
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
	if s.applyTelegramOriginTerminalGate(ctx, "external_execution", &current, nil) {
		return
	}
	observed, _, err := s.persistSyncSnapshot(ctx, model.SyncTopic{ThreadID: current.Thread.ID}, current)
	if err != nil {
		return
	}
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
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return
	}
	topic, err := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, current.Thread.ID)
	if err != nil {
		return
	}
	if owned.RequestID != "" && owned.TurnID != "" && ((current.LatestTurnID == owned.TurnID && isTerminalStatus(current.LatestTurnStatus)) || turnIDAfter(current.LatestTurnID, owned.TurnID)) {
		r, err := s.store.GetExternalLaunchRequest(ctx, owned.RequestID)
		if err != nil || r == nil || r.Status == model.ExternalLaunchSessionStarted || r.Status == model.ExternalLaunchOutcomeUnknown {
			return
		}
		if topic != nil && topic.WriterGeneration == lease.Generation && topic.ActiveTurnID == owned.TurnID {
			if err = s.store.MarkSyncTerminal(ctx, topic.SessionID, topic.ThreadID, owned.TurnID, lease.Generation); err != nil {
				return
			}
			topic.ActiveTurnState = model.SyncTurnTerminal
		}
	}
	// A Sync receipt must become terminal before board cleanup can release its writer.
	if topic != nil && topic.WriterGeneration == lease.Generation &&
		(topic.ActiveTurnState == model.SyncTurnActive || topic.ActiveTurnState == model.SyncTurnStarting || topic.ActiveTurnState == model.SyncTurnUnknown) {
		return
	}
	if (current.LatestTurnID == owned.TurnID && isTerminalStatus(current.LatestTurnStatus)) || turnIDAfter(current.LatestTurnID, owned.TurnID) {
		delete(s.syncLeases, current.Thread.ID)
		delete(s.boardContinuationLeases, current.Thread.ID)
		s.unsubscribeSyncWriterLeaseLocked(ctx, lease)
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

func (s *Service) executionThreadIDs(ctx context.Context) ([]string, error) {
	ids, err := s.store.ListTrackedTaskThreadIDs(ctx)
	if err != nil {
		return nil, err
	}
	external, err := s.store.ListExternalExecutionThreadIDs(ctx)
	if err != nil {
		return nil, err
	}
	for id := range s.boardContinuationLeases {
		external = append(external, id)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, id := range append(ids, external...) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *Service) externalTerminalIsDurable(ctx context.Context, thread string) bool {
	owned, ok := s.boardContinuationLeases[thread]
	if !ok || owned.RequestID == "" {
		return true
	}
	if owned.TurnID == "" {
		return false
	}
	r, err := s.store.GetExternalLaunchRequest(ctx, owned.RequestID)
	if err != nil || r == nil {
		return false
	}
	// Closing tracking does not stop Codex, but also must not pin its writer after
	// authoritative completion of the exact turn retained by this live lease.
	if r.Status == model.ExternalLaunchDismissed {
		return true
	}
	if r.ThreadID != thread || r.TurnID != owned.TurnID {
		return false
	}
	return r.Status == model.ExternalLaunchSessionCompleted || r.Status == model.ExternalLaunchSessionFailed || r.Status == model.ExternalLaunchSessionInterrupted
}
