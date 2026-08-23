package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

type healthEpisode struct {
	Open      bool   `json:"open"`
	EpisodeID string `json:"episode_id"`
	OpenedAt  string `json:"opened_at"`
	LastError string `json:"last_error,omitempty"`
	Count     int    `json:"count"`
}

func (s *Service) reportHealthFailure(ctx context.Context, key, title, summary, action string) {
	key = healthKey(key)
	if key == "" {
		return
	}
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	stateKey := "health." + key
	episode := s.loadHealthEpisode(ctx, stateKey)
	episode.Count++
	episode.LastError = sanitizeDiagnosticString(summary)
	if episode.Open {
		s.saveHealthEpisode(ctx, stateKey, episode)
		return
	}
	episode.Open = true
	episode.EpisodeID = randomToken()
	episode.OpenedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.saveHealthEpisode(ctx, stateKey, episode)
	text := "⚠️ " + strings.TrimSpace(title)
	if safe := strings.TrimSpace(episode.LastError); safe != "" {
		text += "\n\n" + safe
	}
	if action = strings.TrimSpace(action); action != "" {
		text += "\n\nAction: " + action
	}
	s.enqueueControlNotice(ctx, "health:"+key+":"+episode.EpisodeID+":open", text)
}

func (s *Service) reportHealthRecovered(ctx context.Context, key, title string) {
	key = healthKey(key)
	if key == "" {
		return
	}
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	stateKey := "health." + key
	episode := s.loadHealthEpisode(ctx, stateKey)
	if !episode.Open || episode.EpisodeID == "" {
		return
	}
	episode.Open = false
	s.saveHealthEpisode(ctx, stateKey, episode)
	text := "✅ " + strings.TrimSpace(title)
	if openedAt, err := time.Parse(time.RFC3339Nano, episode.OpenedAt); err == nil {
		text += fmt.Sprintf("\n\nRecovered after %s.", time.Since(openedAt).Round(time.Second))
	}
	s.enqueueControlNotice(ctx, "health:"+key+":"+episode.EpisodeID+":recovered", text)
}

func (s *Service) enqueueControlNotice(ctx context.Context, eventID, text string) {
	if s.cfg.AFCGroupID == 0 || strings.TrimSpace(eventID) == "" || strings.TrimSpace(text) == "" {
		return
	}
	payload := model.DeliveryPayload{Text: strings.TrimSpace(text), EventID: eventID}
	_ = s.store.EnqueueDelivery(ctx, model.DeliveryQueueItem{
		EventID: eventID, ChatKey: model.ChatKey(s.cfg.AFCGroupID, afcControlTopicID), ChatID: s.cfg.AFCGroupID,
		TopicID: afcControlTopicID, Kind: "health", Status: model.DeliveryStatusPending,
		AvailableAt: model.NowString(), PayloadJSON: storage.MustJSON(payload), CreatedAt: model.NowString(), UpdatedAt: model.NowString(),
	})
}

func (s *Service) loadHealthEpisode(ctx context.Context, key string) healthEpisode {
	raw, _ := s.store.GetState(ctx, key)
	var episode healthEpisode
	_ = json.Unmarshal([]byte(raw), &episode)
	return episode
}

func (s *Service) saveHealthEpisode(ctx context.Context, key string, episode healthEpisode) {
	payload, _ := json.Marshal(episode)
	_ = s.store.SetState(ctx, key, string(payload))
}

func healthKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			out.WriteRune(r)
		default:
			out.WriteByte('_')
		}
	}
	return strings.Trim(out.String(), "_")
}

func (s *Service) NoteExternalPollResult(ctx context.Context, source string, err error) {
	key := healthKey(source)
	if key == "" {
		key = "external"
	}
	if err != nil {
		_ = s.store.SetState(ctx, key+".poll.last_error", sanitizeDiagnosticString(err.Error()))
		s.reportHealthFailure(ctx, key+".poll", "YMessenger polling failed", err.Error(), "Check the YMessenger token and History API access.")
		return
	}
	_ = s.store.SetState(ctx, key+".poll.last_success_at", time.Now().UTC().Format(time.RFC3339Nano))
	_ = s.store.SetState(ctx, key+".poll.last_error", "")
	s.reportHealthRecovered(ctx, key+".poll", "YMessenger polling recovered")
}
