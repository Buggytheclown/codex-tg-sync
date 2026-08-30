package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func (s *Service) stopSyncTurn(ctx context.Context, topicID int64) (*DirectResponse, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State != model.SyncStateActive {
		return &DirectResponse{Text: "Sync is not accepting topic controls."}, nil
	}
	topic, err := s.store.GetActiveSyncTopic(ctx, state.ChatID, topicID)
	if err != nil {
		return nil, err
	}
	if topic == nil {
		return &DirectResponse{Text: "This Sync topic is stale or unknown."}, nil
	}
	if topic.ActiveTurnState == model.SyncTurnActive && topic.ActiveTurnID != "" {
		lease, ok := s.syncLeases[topic.ThreadID]
		if ok && lease.Generation == topic.WriterGeneration {
			if err := lease.Process.TurnInterrupt(ctx, topic.ThreadID, topic.ActiveTurnID); err != nil {
				return &DirectResponse{Text: fmt.Sprintf("Stop request failed: %v", err)}, nil
			}
			_ = s.markTelegramOriginExplicitInterrupt(ctx, topic.ThreadID, topic.ActiveTurnID)
			return &DirectResponse{Text: "Stop requested. Sync remains active until terminal confirmation."}, nil
		}
	}
	if !s.usesSharedAppServer() {
		return &DirectResponse{Text: "This topic has no stoppable Sync-owned turn."}, nil
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if !connected || poll == nil {
		return &DirectResponse{Text: "Shared App Server session is unavailable; Stop was not sent."}, nil
	}
	current, readErr := readAuthoritativeSyncSnapshot(ctx, poll, topic.ThreadID)
	if readErr != nil {
		return &DirectResponse{Text: fmt.Sprintf("Stop could not verify the current turn: %v", readErr)}, nil
	}
	turnID := activeTurnIDFromSyncSnapshot(current)
	if turnID == "" {
		return &DirectResponse{Text: "This topic has no active turn to stop."}, nil
	}
	if err := poll.TurnInterrupt(ctx, topic.ThreadID, turnID); err != nil {
		return &DirectResponse{Text: fmt.Sprintf("Stop request failed: %v", err)}, nil
	}
	return &DirectResponse{Text: "Stop requested for the current shared App Server turn."}, nil
}

func (s *Service) forceDeactivateSync(ctx context.Context) (*DirectResponse, error) {
	s.syncMu.Lock()
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		s.syncMu.Unlock()
		return nil, err
	}
	if state.State == model.SyncStateOff || state.SessionID == "" {
		s.syncMu.Unlock()
		return &DirectResponse{Text: "Sync is already off."}, nil
	}
	_ = s.syncWriter.BeginDrain()
	if err := s.store.MarkSyncDraining(ctx, state.SessionID); err != nil {
		s.syncMu.Unlock()
		return nil, err
	}
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		s.syncMu.Unlock()
		return nil, err
	}
	for _, topic := range topics {
		if topic.ActiveTurnState != model.SyncTurnActive || topic.ActiveTurnID == "" {
			continue
		}
		lease, ok := s.syncLeases[topic.ThreadID]
		if !ok || lease.Generation != topic.WriterGeneration {
			continue
		}
		if err := lease.Process.TurnInterrupt(ctx, topic.ThreadID, topic.ActiveTurnID); err == nil {
			_ = s.markTelegramOriginExplicitInterrupt(ctx, topic.ThreadID, topic.ActiveTurnID)
		}
	}
	s.syncMu.Unlock()

	timeout := s.cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		s.reconcileSync(ctx)
		writer := s.syncWriter.Snapshot()
		current, _ := s.store.ListSyncTopics(ctx, state.SessionID)
		drafts, draftErr := s.store.ListSyncTopicDrafts(ctx, state.SessionID)
		if draftErr != nil {
			return nil, draftErr
		}
		if writer.Starting+writer.Active+writer.Unknown == 0 && !hasUnfinishedSyncTopics(current) && !hasUnfinishedSyncDrafts(drafts) {
			return s.deactivateSync(ctx)
		}
		if time.Now().After(deadline) {
			titles := unfinishedSyncWorkTitles(current, drafts)
			return &DirectResponse{Text: "Sync remains draining; terminal confirmation timed out for: " + strings.Join(titles, ", ")}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func hasUnfinishedSyncDrafts(drafts []model.SyncTopicDraft) bool {
	for _, draft := range drafts {
		if draft.State == model.SyncDraftStarting || draft.State == model.SyncDraftUnknown {
			return true
		}
	}
	return false
}

func unfinishedSyncDraftTitles(drafts []model.SyncTopicDraft) []string {
	var titles []string
	for _, draft := range drafts {
		if draft.State != model.SyncDraftStarting && draft.State != model.SyncDraftUnknown {
			continue
		}
		title := strings.TrimSpace(draft.Title)
		if title == "" {
			title = fmt.Sprintf("topic %d", draft.TopicID)
		}
		titles = append(titles, title)
	}
	return titles
}

func unfinishedSyncWorkTitles(topics []model.SyncTopic, drafts []model.SyncTopicDraft) []string {
	titles := append(unfinishedSyncTopicTitles(topics), unfinishedSyncDraftTitles(drafts)...)
	if len(titles) == 0 {
		return []string{"ownership unknown"}
	}
	return titles
}

func hasUnfinishedSyncTopics(topics []model.SyncTopic) bool {
	for _, topic := range topics {
		if topic.ActiveTurnState == model.SyncTurnStarting || topic.ActiveTurnState == model.SyncTurnActive || topic.ActiveTurnState == model.SyncTurnUnknown {
			return true
		}
	}
	return false
}

func unfinishedSyncTopicTitles(topics []model.SyncTopic) []string {
	var titles []string
	for _, topic := range topics {
		if topic.ActiveTurnState == model.SyncTurnStarting || topic.ActiveTurnState == model.SyncTurnActive || topic.ActiveTurnState == model.SyncTurnUnknown {
			title := strings.TrimSpace(topic.Title)
			if title == "" {
				title = topic.ThreadID
			}
			titles = append(titles, title)
		}
	}
	return titles
}

func (s *Service) handleSyncPendingRequestLocked(ctx context.Context, topic model.SyncTopic, approval model.PendingApproval) {
	if approval.ThreadID != topic.ThreadID || approval.TurnID == "" || approval.TurnID != topic.ActiveTurnID {
		return
	}
	forum := s.getSyncForum()
	if forum == nil {
		return
	}
	text := syncApprovalHeader + "\n" + strings.TrimSpace(approval.Question)
	var routes []model.CallbackRoute
	var buttons [][]model.ButtonSpec
	if approval.PromptKind == "approval" {
		row := []model.ButtonSpec{}
		for _, choice := range []struct{ label, decision string }{{"Approve", "accept"}, {"Allow command prefix", "acceptForSession"}, {"Deny", "decline"}, {"Cancel", "cancel"}} {
			route, button := s.newSyncCallbackRoute(ctx, topic, approval, "sync_approval", map[string]any{"decision": choice.decision})
			routes, row = append(routes, route), append(row, button)
		}
		buttons = append(buttons, row[:2], row[2:])
	} else {
		text = syncInputHeader + "\n" + strings.TrimSpace(approval.Question)
		for _, choice := range syncUserInputChoices(approval.PayloadJSON) {
			route, button := s.newSyncCallbackRoute(ctx, topic, approval, "sync_user_input", map[string]any{"response": choice.Response})
			routes, buttons = append(routes, route), append(buttons, []model.ButtonSpec{button})
		}
		if len(buttons) == 0 {
			text += "\nNo structured choices were provided; answer in Codex Desktop."
		}
	}
	messageID, err := forum.SendSyncActionMessage(ctx, topic.TopicID, text, buttons)
	if err != nil {
		for _, route := range routes {
			_ = s.store.ExpireCallbackRoute(ctx, route.Token)
		}
		return
	}
	for _, route := range routes {
		route.TelegramMessageID = messageID
		_ = s.store.PutCallbackRoute(ctx, route)
	}
}

func (s *Service) newSyncCallbackRoute(ctx context.Context, topic model.SyncTopic, approval model.PendingApproval, action string, extra map[string]any) (model.CallbackRoute, model.ButtonSpec) {
	payload := map[string]any{"session_id": topic.SessionID, "topic_id": topic.TopicID, "thread_id": topic.ThreadID, "turn_id": topic.ActiveTurnID, "generation": topic.WriterGeneration, "question": approval.Question}
	for key, value := range extra {
		payload[key] = value
	}
	route := model.CallbackRoute{Token: randomToken(), Action: action, ThreadID: topic.ThreadID, TurnID: topic.ActiveTurnID, RequestID: approval.RequestID,
		Status: model.CallbackStatusActive, PayloadJSON: storage.MustJSON(payload), CreatedAt: model.NowString()}
	_ = s.store.PutCallbackRoute(ctx, route)
	label := "Answer"
	if value, ok := extra["decision"].(string); ok {
		label = map[string]string{"accept": "Approve", "acceptForSession": "Allow command prefix", "decline": "Deny", "cancel": "Cancel"}[value]
	}
	if response, ok := extra["response"].(map[string]any); ok {
		if value, ok := response["label"].(string); ok {
			label = value
		}
	}
	return route, model.ButtonSpec{Text: label, CallbackData: route.Token}
}

func (s *Service) handleSyncCallback(ctx context.Context, topicID, messageID int64, token string) (*DirectResponse, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	route, err := s.store.GetCallbackRoute(ctx, token)
	if err != nil {
		return nil, err
	}
	if route == nil || route.Status != model.CallbackStatusActive || !strings.HasPrefix(route.Action, "sync_") {
		return &DirectResponse{CallbackText: "Sync button is stale."}, nil
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(route.PayloadJSON), &payload)
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return nil, err
	}
	generation := uint64(syncPayloadInt64(payload, "generation"))
	if route.Action == "sync_new_project" {
		if state.State != model.SyncStateActive || syncPayloadString(payload, "session_id") != state.SessionID || syncPayloadInt64(payload, "control_topic_id") != topicID {
			return &DirectResponse{CallbackText: "Sync project button is stale."}, nil
		}
		_ = s.store.ExpireCallbackRoute(ctx, route.Token)
		return s.createSyncNewTaskLocked(ctx, state, payload)
	}
	if state.State != model.SyncStateActive || syncPayloadString(payload, "session_id") != state.SessionID || syncPayloadInt64(payload, "topic_id") != topicID || route.TelegramMessageID != 0 && route.TelegramMessageID != messageID {
		return &DirectResponse{CallbackText: "Sync button is stale."}, nil
	}
	topic, err := s.store.GetActiveSyncTopic(ctx, state.ChatID, topicID)
	if err != nil {
		return nil, err
	}
	lease, ok := s.syncLeases[route.ThreadID]
	if topic == nil || route.ThreadID != topic.ThreadID || route.TurnID != topic.ActiveTurnID || generation != topic.WriterGeneration || !ok || lease.Generation != generation {
		return &DirectResponse{CallbackText: "Sync button no longer owns this turn."}, nil
	}
	var result map[string]any
	switch route.Action {
	case "sync_approval":
		result = map[string]any{"decision": syncPayloadString(payload, "decision")}
	case "sync_user_input":
		response, _ := payload["response"].(map[string]any)
		answers, _ := response["answers"].(map[string]any)
		result = map[string]any{"answers": answers}
	default:
		return &DirectResponse{CallbackText: "Sync button is unsupported."}, nil
	}
	if err := lease.Process.RespondServerRequest(ctx, route.RequestID, result); err != nil {
		return &DirectResponse{CallbackText: "Sync response failed; button remains active."}, nil
	}
	_ = s.store.ExpireSyncCallbackRoutesByRequest(ctx, route.RequestID)
	if route.Action == "sync_approval" {
		lines := []string{syncApprovalHeader}
		if question := strings.TrimSpace(syncPayloadString(payload, "question")); question != "" {
			lines = append(lines, question)
		}
		lines = append(lines, "", syncApprovalResolution(syncPayloadString(payload, "decision")))
		forum := s.getSyncForum()
		if forum == nil {
			return &DirectResponse{CallbackText: "Sync response sent; card update failed."}, nil
		}
		if err := forum.EditSyncMessage(ctx, topicID, messageID, model.RenderedMessage{Text: strings.Join(lines, "\n")}); err != nil {
			s.setError(ctx, fmt.Errorf("edit Sync approval card: %w", err))
			return &DirectResponse{CallbackText: "Sync response sent; card update failed."}, nil
		}
	}
	return &DirectResponse{CallbackText: "Sync response sent."}, nil
}

func syncApprovalResolution(decision string) string {
	switch decision {
	case "accept", "acceptForSession":
		return "Approved"
	case "decline":
		return "Denied"
	case "cancel":
		return "Cancelled"
	default:
		return "Resolved"
	}
}

type syncInputChoice struct {
	Label    string
	Response map[string]any
}

func syncUserInputChoices(payloadJSON string) []syncInputChoice {
	var payload map[string]any
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil {
		return nil
	}
	questions, _ := payload["questions"].([]any)
	choices := []syncInputChoice{{Label: "", Response: map[string]any{"label": "", "answers": map[string]any{}}}}
	for _, raw := range questions {
		question, _ := raw.(map[string]any)
		id := syncPayloadString(question, "id")
		options, _ := question["options"].([]any)
		if id == "" || len(options) == 0 {
			return nil
		}
		var next []syncInputChoice
		for _, current := range choices {
			for _, rawOption := range options {
				option, _ := rawOption.(map[string]any)
				label := syncPayloadString(option, "label")
				if label == "" {
					continue
				}
				answers := map[string]any{}
				for key, value := range current.Response["answers"].(map[string]any) {
					answers[key] = value
				}
				answers[id] = map[string]any{"answers": []string{label}}
				combined := strings.Trim(strings.Join([]string{current.Label, label}, " / "), " /")
				next = append(next, syncInputChoice{Label: combined, Response: map[string]any{"label": combined, "answers": answers}})
			}
		}
		choices = next
		if len(choices) > 27 {
			return nil
		}
	}
	return choices
}

func syncPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}
func syncPayloadInt64(payload map[string]any, key string) int64 {
	switch value := payload[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return parsed
	}
	return 0
}
