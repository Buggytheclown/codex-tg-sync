package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

// ExternalSourceCursor and IngestExternalRequests form the intentionally small
// boundary used by optional source adapters. Adapters do not depend on the
// Telegram or App Server implementation owned by Service.
func (s *Service) ExternalSourceCursor(ctx context.Context, source string) (int64, error) {
	return s.store.ExternalSourceCursor(ctx, source)
}

func (s *Service) IngestExternalRequests(ctx context.Context, source string, cursor int64, requests []model.ExternalLaunchRequest) (int, error) {
	return s.store.IngestExternalRequests(ctx, source, cursor, requests)
}

func (s *Service) processExternalLaunchRequests(ctx context.Context) {
	s.externalRequestMu.Lock()
	defer s.externalRequestMu.Unlock()
	s.mu.RLock()
	sender := s.sender
	s.mu.RUnlock()
	if sender == nil || s.cfg.AFCGroupID == 0 {
		return
	}
	requests, err := s.store.ListExternalLaunchRequestsForTelegram(ctx, 20)
	if err != nil {
		return
	}
	for _, request := range requests {
		_ = s.renderExternalLaunchRequest(ctx, sender, request)
	}
}

func (s *Service) renderExternalLaunchRequest(ctx context.Context, sender Sender, request model.ExternalLaunchRequest) error {
	text := externalLaunchRequestText(request)
	if request.TelegramMessageID == 0 {
		_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
		startRoute, startButton, err := s.externalLaunchButton(ctx, request, "Start", "external_launch_start")
		if err != nil {
			return err
		}
		dismissRoute, dismissButton, err := s.externalLaunchButton(ctx, request, "Dismiss", "external_launch_dismiss")
		if err != nil {
			_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
			return err
		}
		messageID, err := sender.SendMessage(ctx, s.cfg.AFCGroupID, request.TelegramTopicID, text,
			[][]model.ButtonSpec{{startButton, dismissButton}}, notifySendOptions())
		if err != nil {
			_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
			return err
		}
		startRoute.TelegramMessageID = messageID
		dismissRoute.TelegramMessageID = messageID
		if err := s.store.PutCallbackRoute(ctx, startRoute); err != nil {
			return err
		}
		if err := s.store.PutCallbackRoute(ctx, dismissRoute); err != nil {
			return err
		}
		return s.store.MarkExternalLaunchRequestTelegramSent(ctx, request.ID, messageID, request.Status)
	}
	var buttons [][]model.ButtonSpec
	if request.Status == model.ExternalLaunchPendingApproval {
		_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
		startRoute, startButton, err := s.externalLaunchButton(ctx, request, "Start", "external_launch_start")
		if err != nil {
			return err
		}
		dismissRoute, dismissButton, err := s.externalLaunchButton(ctx, request, "Dismiss", "external_launch_dismiss")
		if err != nil {
			_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
			return err
		}
		startRoute.TelegramMessageID = request.TelegramMessageID
		dismissRoute.TelegramMessageID = request.TelegramMessageID
		if err := s.store.PutCallbackRoute(ctx, startRoute); err != nil {
			return err
		}
		if err := s.store.PutCallbackRoute(ctx, dismissRoute); err != nil {
			return err
		}
		buttons = [][]model.ButtonSpec{{startButton, dismissButton}}
	}
	if err := sender.EditMessage(ctx, s.cfg.AFCGroupID, request.TelegramTopicID, request.TelegramMessageID, text, buttons); err != nil {
		return err
	}
	return s.store.MarkExternalLaunchRequestTelegramRendered(ctx, request.ID, request.TelegramMessageID, request.Status)
}

func (s *Service) externalLaunchButton(ctx context.Context, request model.ExternalLaunchRequest, label, action string) (model.CallbackRoute, model.ButtonSpec, error) {
	route := model.CallbackRoute{
		Token:     randomToken(),
		Action:    action,
		ThreadID:  request.ID,
		RequestID: request.ID,
		Status:    model.CallbackStatusActive,
		PayloadJSON: storage.MustJSON(map[string]any{
			"chat_id":  s.cfg.AFCGroupID,
			"topic_id": request.TelegramTopicID,
		}),
		CreatedAt: model.NowString(),
	}
	if err := s.store.PutCallbackRoute(ctx, route); err != nil {
		return model.CallbackRoute{}, model.ButtonSpec{}, err
	}
	return route, model.ButtonSpec{Text: label, CallbackData: route.Token}, nil
}

func externalLaunchRequestText(request model.ExternalLaunchRequest) string {
	status := map[string]string{
		model.ExternalLaunchPendingApproval: "Awaiting approval",
		model.ExternalLaunchStarting:        "Starting",
		model.ExternalLaunchSessionStarted:  "Started",
		model.ExternalLaunchDismissed:       "Dismissed",
		model.ExternalLaunchFailed:          "Failed",
		model.ExternalLaunchOutcomeUnknown:  "Outcome unknown — not retried automatically",
	}[request.Status]
	if status == "" {
		status = request.Status
	}
	lines := []string{
		"🚀 [Launch request]",
		fmt.Sprintf("Source: %s", request.Source),
		fmt.Sprintf("Sender: %s", request.Sender),
		fmt.Sprintf("Status: %s", status),
	}
	if strings.TrimSpace(request.SourceURL) != "" {
		lines = append(lines, "Source link: "+strings.TrimSpace(request.SourceURL))
	}
	if strings.TrimSpace(request.ThreadID) != "" {
		lines = append(lines, "Thread: "+request.ThreadID)
	}
	if strings.TrimSpace(request.ErrorSummary) != "" {
		lines = append(lines, "Error: "+request.ErrorSummary)
	}
	lines = append(lines, "", "Requested text:", request.Prompt)
	text := strings.Join(lines, "\n")
	const safeTelegramTextRunes = 3900
	runes := []rune(text)
	if len(runes) > safeTelegramTextRunes {
		text = string(runes[:safeTelegramTextRunes]) + "\n… [truncated to Telegram limit]"
	}
	return text
}

func (s *Service) handleExternalLaunchCallback(ctx context.Context, chatID, topicID, messageID int64, route *model.CallbackRoute) (*DirectResponse, error) {
	if route == nil || route.Status != model.CallbackStatusActive {
		return &DirectResponse{CallbackText: "This launch request button is stale."}, nil
	}
	request, err := s.store.GetExternalLaunchRequest(ctx, route.RequestID)
	if err != nil {
		return nil, err
	}
	if request == nil || chatID != s.cfg.AFCGroupID || topicID != request.TelegramTopicID || messageID != request.TelegramMessageID ||
		route.TelegramMessageID != messageID || route.ThreadID != request.ID {
		return &DirectResponse{CallbackText: "This launch request button is stale."}, nil
	}
	var changed bool
	switch route.Action {
	case "external_launch_start":
		state, stateErr := s.store.GetAFCState(ctx)
		if stateErr != nil {
			return nil, stateErr
		}
		if state.State != model.AFCStateActive {
			_ = s.store.NoteExternalLaunchRequestPendingError(ctx, request.ID, "afc_inactive", "AFC is inactive; enable AFC and press Start again.")
			s.processExternalLaunchRequests(ctx)
			return &DirectResponse{CallbackText: "AFC is inactive; request remains pending."}, nil
		}
		changed, err = s.store.ClaimExternalLaunchRequest(ctx, request.ID)
	case "external_launch_dismiss":
		changed, err = s.store.DismissExternalLaunchRequest(ctx, request.ID)
	default:
		return &DirectResponse{CallbackText: "This launch request button is stale."}, nil
	}
	if err != nil {
		return nil, err
	}
	if !changed {
		return &DirectResponse{CallbackText: "This launch request button is stale."}, nil
	}
	_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
	s.processExternalLaunchRequests(ctx)
	if route.Action == "external_launch_dismiss" {
		return &DirectResponse{CallbackText: "Dismissed."}, nil
	}
	s.startExternalLaunchDispatch(request.ID)
	return &DirectResponse{CallbackText: "Starting."}, nil
}

func (s *Service) startExternalLaunchDispatch(requestID string) {
	s.mu.RLock()
	if !s.started || s.runCtx == nil {
		s.mu.RUnlock()
		return
	}
	ctx := s.runCtx
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.dispatchExternalLaunchRequest(ctx, requestID)
	}()
	s.mu.RUnlock()
}
