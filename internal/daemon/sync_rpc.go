package daemon

import (
	"context"
	"errors"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

type syncExternalRequestKey struct{}

var errSyncCommandNotSent = errors.New("Sync was disabled before this command was sent")

// The caller holds syncMu. The App Server client writes a frame while that lock
// is held, releases it only during response wait, and reacquires before return.
func (s *Service) syncRPCContextLocked(ctx context.Context, sessionID string) context.Context {
	return appserver.WithRequestWaitHooks(ctx, appserver.RequestWaitHooks{
		BeforeSend: func(method string) error {
			if method != "turn/start" && method != "turn/steer" && method != "thread/start" {
				return nil
			}
			state, err := s.store.GetSyncState(ctx)
			if err != nil {
				return err
			}
			if state.SessionID != sessionID || state.State != model.SyncStateActive {
				return errSyncCommandNotSent
			}
			if id, ok := ctx.Value(syncExternalRequestKey{}).(string); ok {
				request, err := s.store.GetExternalLaunchRequest(ctx, id)
				if err != nil {
					return err
				}
				if request == nil || request.Status != model.ExternalLaunchStarting {
					return errSyncCommandNotSent
				}
			}
			return nil
		}, BeforeWait: func() { s.unlockSyncDispatch(ctx) }, AfterWait: func() { s.lockSyncDispatch(ctx) },
	})
}

func (s *Service) unlockSyncDispatch(ctx context.Context) {
	if _, ok := ctx.Value(syncExternalRequestKey{}).(string); ok {
		s.taskLaunchMu.Unlock()
	}
	s.syncMu.Unlock()
}
func (s *Service) lockSyncDispatch(ctx context.Context) {
	s.syncMu.Lock()
	if _, ok := ctx.Value(syncExternalRequestKey{}).(string); ok {
		s.taskLaunchMu.Lock()
	}
}
