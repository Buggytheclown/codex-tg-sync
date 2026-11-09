package daemon

import (
	"context"
	"errors"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

var errStaleSyncObservation = errors.New("Sync ownership changed during background IO")

type syncObservationOwner struct {
	poolGeneration               uint64
	session, state, security     string
	chat                         int64
	poll                         Session
	generation                   uint64
	connected                    bool
	lease                        appserver.WriterLease[Session]
	continuation                 boardContinuationLease
	topicID                      int64
	topicState, turn, turnState  string
	writerGeneration             uint64
	snapshotTurn, snapshotStatus string
}

func (s *Service) syncObservationOwnerLocked(ctx context.Context, thread string) (syncObservationOwner, error) {
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return syncObservationOwner{}, err
	}
	o := syncObservationOwner{poolGeneration: s.syncWriter.Snapshot().Generation, session: state.SessionID, state: state.State, security: state.SecurityState, chat: state.ChatID}
	s.mu.RLock()
	o.poll, o.generation, o.connected = s.poll, s.pollGeneration, s.pollConnected
	s.mu.RUnlock()
	if thread == "" {
		return o, nil
	}
	o.lease = s.syncLeases[thread]
	o.continuation = s.boardContinuationLeases[thread]
	topic, err := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, thread)
	if err != nil {
		return o, err
	}
	if topic != nil {
		o.topicID, o.topicState, o.turn, o.turnState, o.writerGeneration = topic.TopicID, topic.TelegramState, topic.ActiveTurnID, topic.ActiveTurnState, topic.WriterGeneration
	}
	snapshot, err := s.store.GetSnapshot(ctx, thread)
	if err != nil {
		return o, err
	}
	if snapshot != nil {
		o.snapshotTurn, o.snapshotStatus = snapshot.LastSeenTurnID, snapshot.LastSeenTurnStatus
	}
	return o, nil
}

// The caller holds syncMu. Remote background work never owns the lifecycle
// gate; its result is usable only for the same session, transport and target.
func (s *Service) syncSourceIOLocked(ctx context.Context, thread string, source Session, call func() error) error {
	return s.syncValidatedIOLocked(ctx, thread, func(o syncObservationOwner) bool {
		return (source == o.poll && o.connected) || (source == o.lease.Process && source != nil)
	}, call)
}
func (s *Service) syncValidatedIOLocked(ctx context.Context, thread string, valid func(syncObservationOwner) bool, call func() error) error {
	before, err := s.syncObservationOwnerLocked(ctx, thread)
	if err != nil {
		return err
	}
	if valid != nil && !valid(before) {
		return errStaleSyncObservation
	}
	err = s.syncForumCallLocked(call)
	if err != nil {
		return err
	}
	after, err := s.syncObservationOwnerLocked(ctx, thread)
	if err != nil {
		return err
	}
	if before != after {
		s.wakeSyncReconcile()
		return errStaleSyncObservation
	}
	return nil
}

func (s *Service) readSyncObservationLocked(ctx context.Context, reader Session, thread string, summary bool) (appserver.ThreadReadSnapshot, error) {
	var current appserver.ThreadReadSnapshot
	err := s.syncSourceIOLocked(ctx, thread, reader, func() (err error) {
		if summary {
			current, err = readLatestSyncSummary(ctx, reader, thread)
		} else {
			current, err = readAuthoritativeSyncSnapshot(ctx, reader, thread)
		}
		return err
	})
	return current, err
}

func (s *Service) listSyncObservationsLocked(ctx context.Context, reader Session) ([]model.Thread, error) {
	var threads []model.Thread
	err := s.syncSourceIOLocked(ctx, "", reader, func() (err error) { threads, err = listSyncThreads(ctx, reader); return err })
	return threads, err
}
