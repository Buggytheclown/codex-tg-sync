package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
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
	created, err := s.store.IngestExternalRequests(ctx, source, cursor, requests)
	return s.finishExternalRequestIngest(ctx, requests, created, err)
}

func (s *Service) EnqueueExternalRequests(ctx context.Context, source string, requests []model.ExternalLaunchRequest) (int, error) {
	created, err := s.store.EnqueueExternalLaunchRequests(ctx, source, requests)
	return s.finishExternalRequestIngest(ctx, requests, created, err)
}

func (s *Service) finishExternalRequestIngest(ctx context.Context, requests []model.ExternalLaunchRequest, created int, err error) (int, error) {
	if err == nil && created > 0 {
		for _, request := range requests {
			if strings.Contains(request.Prompt, "Unavailable: History API did not return the thread root message.") {
				s.logLifecycle("external_context_missing", lifecycleFields{"request_id": request.ID, "source": request.Source, "context": "thread_root"})
			}
		}
		s.processExternalLaunchRequests(ctx)
	}
	return created, err
}

func (s *Service) processExternalLaunchRequests(ctx context.Context) {
	s.externalRequestMu.Lock()
	requestIDs := make([]string, 0)
	state, stateErr := s.store.GetAFCState(ctx)
	if stateErr != nil {
		s.logLifecycle("external_launch_state_read_failed", lifecycleFields{"error": stateErr})
	}
	if state.State == model.AFCStateActive {
		autoRequests, listErr := s.store.ListExternalLaunchRequestsForAutoStart(ctx, 20)
		if listErr != nil {
			s.logLifecycle("external_launch_auto_list_failed", lifecycleFields{"error": listErr})
		}
		for _, request := range autoRequests {
			claimed, err := s.store.ClaimExternalLaunchRequest(ctx, request.ID)
			if err != nil {
				s.logLifecycle("external_launch_auto_claim_failed", lifecycleFields{"request_id": request.ID, "source": request.Source, "error": err})
			} else if claimed {
				requestIDs = append(requestIDs, request.ID)
				s.logLifecycle("external_launch_auto_started", lifecycleFields{"request_id": request.ID, "source": request.Source})
			}
		}
	}
	s.mu.RLock()
	sender := s.sender
	s.mu.RUnlock()
	s.renderExternalLaunchRequestsLocked(ctx, sender)
	s.externalRequestMu.Unlock()
	for _, requestID := range requestIDs {
		s.startExternalLaunchDispatch(requestID)
	}
}

func (s *Service) renderExternalLaunchRequestsLocked(ctx context.Context, sender Sender) {
	if sender != nil && s.cfg.AFCGroupID != 0 {
		requests, err := s.store.ListExternalLaunchRequestsForTelegram(ctx, 20)
		if err != nil {
			s.logLifecycle("external_launch_telegram_list_failed", lifecycleFields{"error": err})
		} else {
			for _, request := range requests {
				if err := s.renderExternalLaunchRequest(ctx, sender, request); err != nil {
					s.logLifecycle("external_launch_telegram_render_failed", lifecycleFields{"request_id": request.ID, "source": request.Source, "error": err})
				}
			}
		}
	}
}

func (s *Service) queueExternalReplyFromSnapshot(ctx context.Context, snapshot appserver.ThreadReadSnapshot) {
	if !isTerminalStatus(snapshot.LatestTurnStatus) || strings.TrimSpace(snapshot.LatestTurnID) == "" {
		return
	}
	text := strings.TrimSpace(snapshot.LatestFinalText)
	if text == "" {
		switch strings.ToLower(strings.TrimSpace(snapshot.LatestTurnStatus)) {
		case "failed", "error":
			text = "The Codex turn failed before producing a final answer."
		case "interrupted", "cancelled", "canceled", "aborted":
			text = "The Codex turn was interrupted before producing a final answer."
		default:
			text = "The Codex turn ended without a final answer."
		}
	}
	launchStatus := model.ExternalLaunchSessionCompleted
	switch strings.ToLower(strings.TrimSpace(snapshot.LatestTurnStatus)) {
	case "failed", "error":
		launchStatus = model.ExternalLaunchSessionFailed
	case "interrupted", "cancelled", "canceled", "aborted":
		launchStatus = model.ExternalLaunchSessionInterrupted
	}
	queued, err := s.store.CompleteExternalTurn(ctx, snapshot.Thread.ID, snapshot.LatestTurnID, launchStatus, text)
	if err != nil {
		s.logLifecycle("external_reply_queue_failed", lifecycleFields{"thread_id": snapshot.Thread.ID, "turn_id": snapshot.LatestTurnID, "error": err})
		return
	}
	if queued {
		s.logLifecycle("external_reply_queued", lifecycleFields{"thread_id": snapshot.Thread.ID, "turn_id": snapshot.LatestTurnID})
		s.processExternalLaunchRequests(ctx)
	}
}

func (s *Service) queueExternalReplyFromStoredSnapshot(ctx context.Context, threadID string) {
	stored, err := s.store.GetSnapshot(ctx, strings.TrimSpace(threadID))
	if err != nil || stored == nil || len(stored.CompactJSON) == 0 {
		return
	}
	var snapshot appserver.ThreadReadSnapshot
	if json.Unmarshal(stored.CompactJSON, &snapshot) == nil {
		s.queueExternalReplyFromSnapshot(ctx, snapshot)
	}
}

func (s *Service) processExternalReplyBatch(ctx context.Context) {
	s.mu.RLock()
	sender := s.externalReplySender
	s.mu.RUnlock()
	if sender == nil {
		return
	}
	acks, err := s.store.ClaimExternalAckBatch(ctx, 10)
	if err != nil {
		s.logLifecycle("external_ack_claim_failed", lifecycleFields{"error": err})
	} else {
		for _, request := range acks {
			s.deliverExternalMessage(ctx, sender, request, true)
		}
	}
	requests, err := s.store.ClaimExternalReplyBatch(ctx, 10)
	if err != nil {
		s.logLifecycle("external_reply_claim_failed", lifecycleFields{"error": err})
		return
	}
	for _, request := range requests {
		s.deliverExternalMessage(ctx, sender, request, false)
	}
}

func (s *Service) deliverExternalMessage(ctx context.Context, sender ExternalReplySender, request model.ExternalLaunchRequest, acknowledgement bool) {
	maxAttempts := s.cfg.DeliveryMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	baseDelay := s.cfg.DeliveryRetryBase
	if baseDelay <= 0 {
		baseDelay = 5 * time.Second
	}
	text := request.ReplyText
	attempts := request.ReplyAttempts
	kind := "reply"
	if acknowledgement {
		text = request.AckText
		attempts = request.AckAttempts
		kind = "ack"
	}
	messageID, sendErr := sender.SendExternalReply(ctx, request.SourceChatID, request.SourceMessageID, request.SourceThreadID, text)
	if acknowledgement {
		if sendErr == nil {
			sendErr = s.store.CompleteExternalAck(ctx, request.ID, messageID)
		}
	} else if sendErr == nil {
		sendErr = s.store.CompleteExternalReply(ctx, request.ID, messageID)
	}
	if sendErr == nil {
		s.logLifecycle("external_"+kind+"_sent", lifecycleFields{"request_id": request.ID, "source": request.Source, "thread_id": request.ThreadID, "turn_id": request.TurnID})
		return
	}
	attempt := attempts + 1
	dead := attempt >= maxAttempts
	backoff := baseDelay * time.Duration(1<<min(attempt-1, 4))
	safeError := sanitizeDiagnosticString(sendErr.Error())
	if acknowledgement {
		_ = s.store.FailExternalAck(ctx, request.ID, attempt, time.Now().UTC().Add(backoff), safeError, dead)
	} else {
		_ = s.store.FailExternalReply(ctx, request.ID, attempt, time.Now().UTC().Add(backoff), safeError, dead)
	}
	if dead {
		s.reportHealthFailure(ctx, "external."+kind+"."+request.ID, "YMessenger "+kind+" delivery stopped retrying",
			"Request "+request.ID+": "+safeError, "Check YMessenger connectivity, then inspect the external delivery backlog.")
	}
	s.logLifecycle("external_"+kind+"_failed", lifecycleFields{"request_id": request.ID, "source": request.Source, "attempt": attempt, "dead": dead, "error": sendErr})
}

func (s *Service) renderExternalLaunchRequest(ctx context.Context, sender Sender, request model.ExternalLaunchRequest) error {
	text := externalLaunchRequestText(request)
	if request.TelegramMessageID == 0 {
		_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
		buttons, routes, err := s.externalLaunchActions(ctx, request)
		if err != nil {
			return err
		}
		messageID, err := sender.SendMessage(ctx, s.cfg.AFCGroupID, request.TelegramTopicID, text,
			buttons, notifySendOptions())
		if err != nil {
			_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
			return err
		}
		for _, route := range routes {
			route.TelegramMessageID = messageID
			if err := s.store.PutCallbackRoute(ctx, route); err != nil {
				return err
			}
		}
		return s.store.MarkExternalLaunchRequestTelegramSent(ctx, request.ID, messageID, request.Status)
	}
	_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
	buttons, routes, err := s.externalLaunchActions(ctx, request)
	if err != nil {
		return err
	}
	for _, route := range routes {
		route.TelegramMessageID = request.TelegramMessageID
		if err := s.store.PutCallbackRoute(ctx, route); err != nil {
			return err
		}
	}
	if err := sender.EditMessage(ctx, s.cfg.AFCGroupID, request.TelegramTopicID, request.TelegramMessageID, text, buttons); err != nil {
		return err
	}
	return s.store.MarkExternalLaunchRequestTelegramRendered(ctx, request.ID, request.TelegramMessageID, request.Status)
}

func (s *Service) externalLaunchActions(ctx context.Context, request model.ExternalLaunchRequest) ([][]model.ButtonSpec, []model.CallbackRoute, error) {
	type action struct {
		label string
		name  string
	}
	var actions []action
	switch request.Status {
	case model.ExternalLaunchPendingApproval:
		if !request.AutoStart {
			actions = []action{{label: "Dismiss", name: "external_launch_dismiss"}, {label: "Start", name: "external_launch_start"}}
		}
	case model.ExternalLaunchFailed:
		actions = []action{{label: "Close", name: "external_launch_close"}, {label: "Retry", name: "external_launch_retry"}}
	case model.ExternalLaunchOutcomeUnknown:
		actions = []action{{label: "Close", name: "external_launch_close"}, {label: "Check status", name: "external_launch_check"}}
	}
	if len(actions) == 0 {
		return nil, nil, nil
	}
	buttons := make([]model.ButtonSpec, 0, len(actions))
	routes := make([]model.CallbackRoute, 0, len(actions))
	for _, action := range actions {
		route, button, err := s.externalLaunchButton(ctx, request, action.label, action.name)
		if err != nil {
			_ = s.store.ExpireExternalLaunchCallbackRoutes(ctx, request.ID)
			return nil, nil, err
		}
		buttons = append(buttons, button)
		routes = append(routes, route)
	}
	return [][]model.ButtonSpec{buttons}, routes, nil
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
	status := externalLaunchStatusLabel(request.Status)
	lines := []string{
		"🚀 [Launch request]",
		fmt.Sprintf("Source: %s", request.Source),
		fmt.Sprintf("From: %s", request.Sender),
	}
	if strings.TrimSpace(request.Title) != "" {
		lines = append(lines, "Title: "+strings.TrimSpace(request.Title))
	}
	lines = append(lines, fmt.Sprintf("Status: %s", status))
	if strings.TrimSpace(request.SourceURL) != "" {
		lines = append(lines, "Link: "+strings.TrimSpace(request.SourceURL))
	}
	if strings.TrimSpace(request.ThreadID) != "" {
		lines = append(lines, "Thread: "+request.ThreadID)
	}
	if strings.TrimSpace(request.ErrorSummary) != "" {
		lines = append(lines, "Error: "+request.ErrorSummary)
	}
	lines = append(lines, "", "Request:", externalLaunchTelegramPreview(request))
	text := strings.Join(lines, "\n")
	const safeTelegramTextRunes = 3900
	runes := []rune(text)
	if len(runes) > safeTelegramTextRunes {
		text = string(runes[:safeTelegramTextRunes]) + "\n… [truncated to Telegram limit]"
	}
	return text
}

func externalLaunchStatusLabel(status string) string {
	label := map[string]string{
		model.ExternalLaunchPendingApproval:    "Awaiting approval",
		model.ExternalLaunchStarting:           "Starting",
		model.ExternalLaunchSessionStarted:     "Started",
		model.ExternalLaunchSessionCompleted:   "Completed",
		model.ExternalLaunchSessionInterrupted: "Interrupted",
		model.ExternalLaunchSessionFailed:      "Run failed",
		model.ExternalLaunchDismissed:          "Dismissed",
		model.ExternalLaunchFailed:             "Failed",
		model.ExternalLaunchOutcomeUnknown:     "Outcome unknown — not retried automatically",
	}[status]
	if label == "" {
		return status
	}
	return label
}

func externalLaunchTelegramPreview(request model.ExternalLaunchRequest) string {
	if preview := strings.TrimSpace(request.SafePreview); preview != "" {
		return preview
	}
	return strings.TrimSpace(request.Prompt)
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
	if route.Action == "external_launch_check" {
		return s.checkExternalLaunchOutcome(ctx, *request)
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
	case "external_launch_retry":
		changed, err = s.store.RetryExternalLaunchRequest(ctx, request.ID)
	case "external_launch_close":
		changed, err = s.store.CloseExternalLaunchRequest(ctx, request.ID)
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
	if route.Action == "external_launch_close" {
		return &DirectResponse{CallbackText: "Closed."}, nil
	}
	if route.Action == "external_launch_retry" {
		s.startExternalLaunchDispatch(request.ID)
		return &DirectResponse{CallbackText: "Retrying."}, nil
	}
	s.startExternalLaunchDispatch(request.ID)
	return &DirectResponse{CallbackText: "Starting."}, nil
}

func (s *Service) checkExternalLaunchOutcome(ctx context.Context, request model.ExternalLaunchRequest) (*DirectResponse, error) {
	if request.Status != model.ExternalLaunchOutcomeUnknown {
		return &DirectResponse{CallbackText: "This launch request button is stale."}, nil
	}
	if strings.TrimSpace(request.ThreadID) == "" {
		return &DirectResponse{CallbackText: "No durable thread id is available; outcome remains unknown."}, nil
	}
	stored, err := s.store.GetSnapshot(ctx, request.ThreadID)
	if err != nil {
		return nil, err
	}
	if stored == nil || len(stored.CompactJSON) == 0 {
		return &DirectResponse{CallbackText: "No authoritative thread snapshot is available yet; outcome remains unknown."}, nil
	}
	var snapshot appserver.ThreadReadSnapshot
	if err := json.Unmarshal(stored.CompactJSON, &snapshot); err != nil || strings.TrimSpace(snapshot.LatestTurnID) == "" {
		return &DirectResponse{CallbackText: "The thread snapshot has no confirmed turn; outcome remains unknown."}, nil
	}
	changed, err := s.store.ResolveExternalLaunchOutcome(ctx, request.ID, request.ThreadID, snapshot.LatestTurnID)
	if err != nil {
		return nil, err
	}
	if !changed {
		return &DirectResponse{CallbackText: "This launch request button is stale."}, nil
	}
	if isTerminalStatus(snapshot.LatestTurnStatus) {
		s.queueExternalReplyFromSnapshot(ctx, snapshot)
	} else {
		s.processExternalLaunchRequests(ctx)
	}
	return &DirectResponse{CallbackText: "Codex session found; request status reconciled."}, nil
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
