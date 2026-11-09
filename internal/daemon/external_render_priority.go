package daemon

import (
	"context"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type externalRenderAttempt struct {
	background bool
	cancel     context.CancelFunc
}

// SQLite owns due work; the existing foreground loop and one bounded background
// worker share per-request ownership. A parked maintenance edit cannot own the
// only path to the transport's foreground queue.
func (s *Service) renderExternalLaunchRequest(ctx context.Context, sender Sender, request model.ExternalLaunchRequest) error {
	current, err := s.store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || current == nil {
		return err
	}
	request = *current
	s.externalRenderMu.Lock()
	if active := s.externalRenderInFlight[request.ID]; active != nil {
		if active.background && request.TelegramForeground() {
			active.cancel()
		}
		s.externalRenderMu.Unlock()
		return nil
	}
	if s.externalRenderInFlight == nil {
		s.externalRenderInFlight = map[string]*externalRenderAttempt{}
	}
	renderCtx, cancel := context.WithCancel(ctx)
	attempt := &externalRenderAttempt{background: !request.TelegramForeground(), cancel: cancel}
	s.externalRenderInFlight[request.ID] = attempt
	s.externalRenderMu.Unlock()
	defer func() {
		cancel()
		s.externalRenderMu.Lock()
		delete(s.externalRenderInFlight, request.ID)
		s.externalRenderMu.Unlock()
	}()
	return s.renderOwnedExternalLaunchRequest(renderCtx, sender, request)
}

func (s *Service) renderExternalBackgroundRequests(ctx context.Context, sender Sender) {
	requests, err := s.store.ListExternalTelegramPriority(ctx, 20, true)
	if err != nil {
		s.logLifecycle("external_launch_telegram_list_failed", lifecycleFields{"error": err})
		return
	}
	for _, request := range requests {
		if ctx.Err() != nil {
			return
		}
		if err := s.renderExternalLaunchRequest(ctx, sender, request); err != nil && ctx.Err() == nil {
			s.logLifecycle("external_launch_telegram_render_failed", lifecycleFields{"request_id": request.ID, "source": request.Source, "error": err})
		}
	}
}

func (s *Service) startExternalBackgroundRender(ctx context.Context, sender Sender) {
	if sender == nil || s.cfg.SyncGroupID == 0 {
		return
	}
	s.mu.RLock()
	if !s.started || s.runCtx == nil {
		s.mu.RUnlock()
		s.renderExternalBackgroundRequests(ctx, sender)
		return
	}
	s.externalRenderMu.Lock()
	if s.externalBackgroundRendering {
		s.externalRenderMu.Unlock()
		s.mu.RUnlock()
		return
	}
	s.externalBackgroundRendering = true
	s.externalRenderMu.Unlock()
	runCtx := s.runCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() { s.externalRenderMu.Lock(); s.externalBackgroundRendering = false; s.externalRenderMu.Unlock() }()
		s.renderExternalBackgroundRequests(runCtx, sender)
	}()
	s.mu.RUnlock()
}
