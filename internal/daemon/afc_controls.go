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

func (s *Service) stopAFCTurn(ctx context.Context, topicID int64) (*DirectResponse, error) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State != model.AFCStateActive {
		return &DirectResponse{Text: "AFC is not accepting topic controls."}, nil
	}
	topic, err := s.store.GetActiveAFCTopic(ctx, state.ChatID, topicID)
	if err != nil {
		return nil, err
	}
	if topic == nil || topic.ActiveTurnState != model.AFCTurnActive || topic.ActiveTurnID == "" {
		return &DirectResponse{Text: "This topic has no stoppable Telegram-origin turn."}, nil
	}
	lease, ok := s.afcLeases[topic.ThreadID]
	if !ok || lease.Generation != topic.WriterGeneration {
		return &DirectResponse{Text: "Turn ownership is unknown; AFC will not interrupt it blindly."}, nil
	}
	if err := lease.Process.TurnInterrupt(ctx, topic.ThreadID, topic.ActiveTurnID); err != nil {
		return &DirectResponse{Text: fmt.Sprintf("Stop request failed: %v", err)}, nil
	}
	return &DirectResponse{Text: "Stop requested. AFC remains active until terminal confirmation."}, nil
}

func (s *Service) forceDeactivateAFC(ctx context.Context) (*DirectResponse, error) {
	s.afcMu.Lock()
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		s.afcMu.Unlock()
		return nil, err
	}
	if state.State == model.AFCStateOff || state.SessionID == "" {
		s.afcMu.Unlock()
		return &DirectResponse{Text: "AFC is already off."}, nil
	}
	_ = s.afcWriter.BeginDrain()
	if err := s.store.MarkAFCDraining(ctx, state.SessionID); err != nil {
		s.afcMu.Unlock()
		return nil, err
	}
	topics, err := s.store.ListAFCTopics(ctx, state.SessionID)
	if err != nil {
		s.afcMu.Unlock()
		return nil, err
	}
	for _, topic := range topics {
		if topic.ActiveTurnState != model.AFCTurnActive || topic.ActiveTurnID == "" {
			continue
		}
		lease, ok := s.afcLeases[topic.ThreadID]
		if !ok || lease.Generation != topic.WriterGeneration {
			continue
		}
		_ = lease.Process.TurnInterrupt(ctx, topic.ThreadID, topic.ActiveTurnID)
	}
	s.afcMu.Unlock()

	timeout := s.cfg.RequestTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	if timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for {
		s.syncAFC(ctx)
		writer := s.afcWriter.Snapshot()
		current, _ := s.store.ListAFCTopics(ctx, state.SessionID)
		if writer.Starting+writer.Active+writer.Unknown == 0 && !hasUnfinishedAFCTopics(current) {
			return s.deactivateAFC(ctx)
		}
		if time.Now().After(deadline) {
			return &DirectResponse{Text: "AFC remains draining; terminal confirmation timed out for: " + strings.Join(unfinishedAFCTopicTitles(current), ", ")}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func hasUnfinishedAFCTopics(topics []model.AFCTopic) bool {
	for _, topic := range topics {
		if topic.ActiveTurnState == model.AFCTurnStarting || topic.ActiveTurnState == model.AFCTurnActive || topic.ActiveTurnState == model.AFCTurnUnknown {
			return true
		}
	}
	return false
}

func unfinishedAFCTopicTitles(topics []model.AFCTopic) []string {
	var titles []string
	for _, topic := range topics {
		if topic.ActiveTurnState == model.AFCTurnStarting || topic.ActiveTurnState == model.AFCTurnActive || topic.ActiveTurnState == model.AFCTurnUnknown {
			title := strings.TrimSpace(topic.Title)
			if title == "" {
				title = topic.ThreadID
			}
			titles = append(titles, title)
		}
	}
	if len(titles) == 0 {
		return []string{"ownership unknown"}
	}
	return titles
}

func (s *Service) handleAFCPendingRequestLocked(ctx context.Context, topic model.AFCTopic, approval model.PendingApproval) {
	if approval.ThreadID != topic.ThreadID || approval.TurnID == "" || approval.TurnID != topic.ActiveTurnID {
		return
	}
	forum := s.getAFCForum()
	if forum == nil {
		return
	}
	text := "[Approval]\n" + strings.TrimSpace(approval.Question)
	var routes []model.CallbackRoute
	var buttons [][]model.ButtonSpec
	if approval.PromptKind == "approval" {
		row := []model.ButtonSpec{}
		for _, choice := range []struct{ label, decision string }{{"Approve", "accept"}, {"Approve session", "acceptForSession"}, {"Deny", "decline"}, {"Cancel", "cancel"}} {
			route, button := s.newAFCCallbackRoute(ctx, topic, approval, "afc_approval", map[string]any{"decision": choice.decision})
			routes, row = append(routes, route), append(row, button)
		}
		buttons = append(buttons, row[:2], row[2:])
	} else {
		text = "[Input]\n" + strings.TrimSpace(approval.Question)
		for _, choice := range afcUserInputChoices(approval.PayloadJSON) {
			route, button := s.newAFCCallbackRoute(ctx, topic, approval, "afc_user_input", map[string]any{"response": choice.Response})
			routes, buttons = append(routes, route), append(buttons, []model.ButtonSpec{button})
		}
		if len(buttons) == 0 {
			text += "\nNo structured choices were provided; answer in Codex Desktop."
		}
	}
	messageID, err := forum.SendAFCActionMessage(ctx, topic.TopicID, text, buttons)
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

func (s *Service) newAFCCallbackRoute(ctx context.Context, topic model.AFCTopic, approval model.PendingApproval, action string, extra map[string]any) (model.CallbackRoute, model.ButtonSpec) {
	payload := map[string]any{"session_id": topic.SessionID, "topic_id": topic.TopicID, "thread_id": topic.ThreadID, "turn_id": topic.ActiveTurnID, "generation": topic.WriterGeneration}
	for key, value := range extra {
		payload[key] = value
	}
	route := model.CallbackRoute{Token: randomToken(), Action: action, ThreadID: topic.ThreadID, TurnID: topic.ActiveTurnID, RequestID: approval.RequestID,
		Status: model.CallbackStatusActive, PayloadJSON: storage.MustJSON(payload), CreatedAt: model.NowString()}
	_ = s.store.PutCallbackRoute(ctx, route)
	label := "Answer"
	if value, ok := extra["decision"].(string); ok {
		label = map[string]string{"accept": "Approve", "acceptForSession": "Approve session", "decline": "Deny", "cancel": "Cancel"}[value]
	}
	if response, ok := extra["response"].(map[string]any); ok {
		if value, ok := response["label"].(string); ok {
			label = value
		}
	}
	return route, model.ButtonSpec{Text: label, CallbackData: route.Token}
}

func (s *Service) handleAFCCallback(ctx context.Context, topicID, messageID int64, token string) (*DirectResponse, error) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	route, err := s.store.GetCallbackRoute(ctx, token)
	if err != nil {
		return nil, err
	}
	if route == nil || route.Status != model.CallbackStatusActive || !strings.HasPrefix(route.Action, "afc_") {
		return &DirectResponse{CallbackText: "AFC button is stale."}, nil
	}
	var payload map[string]any
	_ = json.Unmarshal([]byte(route.PayloadJSON), &payload)
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return nil, err
	}
	generation := uint64(afcPayloadInt64(payload, "generation"))
	if state.State != model.AFCStateActive || afcPayloadString(payload, "session_id") != state.SessionID || afcPayloadInt64(payload, "topic_id") != topicID || route.TelegramMessageID != 0 && route.TelegramMessageID != messageID {
		return &DirectResponse{CallbackText: "AFC button is stale."}, nil
	}
	topic, err := s.store.GetActiveAFCTopic(ctx, state.ChatID, topicID)
	if err != nil {
		return nil, err
	}
	lease, ok := s.afcLeases[route.ThreadID]
	if topic == nil || route.ThreadID != topic.ThreadID || route.TurnID != topic.ActiveTurnID || generation != topic.WriterGeneration || !ok || lease.Generation != generation {
		return &DirectResponse{CallbackText: "AFC button no longer owns this turn."}, nil
	}
	var result map[string]any
	switch route.Action {
	case "afc_approval":
		result = map[string]any{"decision": afcPayloadString(payload, "decision")}
	case "afc_user_input":
		response, _ := payload["response"].(map[string]any)
		answers, _ := response["answers"].(map[string]any)
		result = map[string]any{"answers": answers}
	default:
		return &DirectResponse{CallbackText: "AFC button is unsupported."}, nil
	}
	if err := lease.Process.RespondServerRequest(ctx, route.RequestID, result); err != nil {
		return &DirectResponse{CallbackText: "AFC response failed; button remains active."}, nil
	}
	_ = s.store.ExpireAFCCallbackRoutesByRequest(ctx, route.RequestID)
	return &DirectResponse{CallbackText: "AFC response sent."}, nil
}

type afcInputChoice struct {
	Label    string
	Response map[string]any
}

func afcUserInputChoices(payloadJSON string) []afcInputChoice {
	var payload map[string]any
	if json.Unmarshal([]byte(payloadJSON), &payload) != nil {
		return nil
	}
	questions, _ := payload["questions"].([]any)
	choices := []afcInputChoice{{Label: "", Response: map[string]any{"label": "", "answers": map[string]any{}}}}
	for _, raw := range questions {
		question, _ := raw.(map[string]any)
		id := afcPayloadString(question, "id")
		options, _ := question["options"].([]any)
		if id == "" || len(options) == 0 {
			return nil
		}
		var next []afcInputChoice
		for _, current := range choices {
			for _, rawOption := range options {
				option, _ := rawOption.(map[string]any)
				label := afcPayloadString(option, "label")
				if label == "" {
					continue
				}
				answers := map[string]any{}
				for key, value := range current.Response["answers"].(map[string]any) {
					answers[key] = value
				}
				answers[id] = map[string]any{"answers": []string{label}}
				combined := strings.Trim(strings.Join([]string{current.Label, label}, " / "), " /")
				next = append(next, afcInputChoice{Label: combined, Response: map[string]any{"label": combined, "answers": answers}})
			}
		}
		choices = next
		if len(choices) > 27 {
			return nil
		}
	}
	return choices
}

func afcPayloadString(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}
func afcPayloadInt64(payload map[string]any, key string) int64 {
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
