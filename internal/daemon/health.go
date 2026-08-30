package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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

type externalPollHealthState struct {
	LastObservedAt       string `json:"last_observed_at,omitempty"`
	WarmupUntil          string `json:"warmup_until,omitempty"`
	FailureSince         string `json:"failure_since,omitempty"`
	RecoverySince        string `json:"recovery_since,omitempty"`
	RecoveredAfterResume bool   `json:"recovered_after_resume,omitempty"`
}

type externalPollerState struct {
	Source              string `json:"source"`
	Enabled             bool   `json:"enabled"`
	IntervalMillis      int64  `json:"interval_millis"`
	RegisteredAt        string `json:"registered_at,omitempty"`
	LastStartedAt       string `json:"last_started_at,omitempty"`
	LastFinishedAt      string `json:"last_finished_at,omitempty"`
	LastSuccessAt       string `json:"last_success_at,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	ConsecutiveFailures int    `json:"consecutive_failures,omitempty"`
}

const (
	externalPollObservationGap = 2 * time.Minute
	externalPollResumeGrace    = 4 * time.Minute
	externalPollFailureDelay   = time.Minute
	externalPollRecoveryDelay  = 30 * time.Second
)

func (s *Service) reportHealthFailure(ctx context.Context, key, title, summary, action string) {
	key = healthKey(key)
	if key == "" {
		return
	}
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.reportHealthFailureLocked(ctx, key, title, summary, action)
}

func (s *Service) reportHealthFailureLocked(ctx context.Context, key, title, summary, action string) {
	s.reportHealthFailureLockedToTopic(ctx, key, title, summary, action, afcGeneralSendTopicID)
}

func (s *Service) reportHealthFailureLockedToTopic(ctx context.Context, key, title, summary, action string, topicID int64) {
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
	episode.OpenedAt = s.now().UTC().Format(time.RFC3339Nano)
	s.saveHealthEpisode(ctx, stateKey, episode)
	text := "⚠️ " + strings.TrimSpace(title)
	if safe := strings.TrimSpace(episode.LastError); safe != "" {
		text += "\n\n" + safe
	}
	if action = strings.TrimSpace(action); action != "" {
		text += "\n\nAction: " + action
	}
	s.enqueueControlNoticeToTopic(ctx, "health:"+key+":"+episode.EpisodeID+":open", text, key, episode.EpisodeID, "open", topicID)
}

func (s *Service) reportHealthRecovered(ctx context.Context, key, title string) {
	key = healthKey(key)
	if key == "" {
		return
	}
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	s.reportHealthRecoveredLocked(ctx, key, title, "")
}

func (s *Service) reportHealthRecoveredLocked(ctx context.Context, key, title, note string) {
	s.reportHealthRecoveredLockedToTopic(ctx, key, title, note, afcGeneralSendTopicID)
}

func (s *Service) reportHealthRecoveredLockedToTopic(ctx context.Context, key, title, note string, topicID int64) {
	stateKey := "health." + key
	episode := s.loadHealthEpisode(ctx, stateKey)
	if !episode.Open || episode.EpisodeID == "" {
		return
	}
	episode.Open = false
	s.saveHealthEpisode(ctx, stateKey, episode)
	text := "✅ " + strings.TrimSpace(title)
	if note = strings.TrimSpace(note); note != "" {
		text += "\n\n" + note
	} else if openedAt, err := time.Parse(time.RFC3339Nano, episode.OpenedAt); err == nil {
		text += fmt.Sprintf("\n\nRecovered after %s.", s.now().UTC().Sub(openedAt).Round(time.Second))
	}
	s.enqueueControlNoticeToTopic(ctx, "health:"+key+":"+episode.EpisodeID+":recovered", text, key, episode.EpisodeID, "recovered", topicID)
}

func (s *Service) enqueueControlNotice(ctx context.Context, eventID, text, healthKey, episodeID, healthState string) {
	s.enqueueControlNoticeToTopic(ctx, eventID, text, healthKey, episodeID, healthState, afcGeneralSendTopicID)
}

func (s *Service) enqueueControlNoticeToTopic(ctx context.Context, eventID, text, healthKey, episodeID, healthState string, topicID int64) {
	if s.cfg.AFCGroupID == 0 || strings.TrimSpace(eventID) == "" || strings.TrimSpace(text) == "" {
		return
	}
	payload := model.DeliveryPayload{
		Text: strings.TrimSpace(text), EventID: eventID,
		HealthKey: healthKey, HealthEpisodeID: episodeID, HealthState: healthState,
	}
	_ = s.store.EnqueueDelivery(ctx, model.DeliveryQueueItem{
		EventID: eventID, ChatKey: model.ChatKey(s.cfg.AFCGroupID, topicID), ChatID: s.cfg.AFCGroupID,
		TopicID: topicID, Kind: "health", Status: model.DeliveryStatusPending,
		AvailableAt: model.NowString(), PayloadJSON: storage.MustJSON(payload), CreatedAt: model.NowString(), UpdatedAt: model.NowString(),
	})
}

func openHealthIncidentNames(state map[string]string) []string {
	var names []string
	for key, raw := range state {
		if !strings.HasPrefix(key, "health.") {
			continue
		}
		var episode healthEpisode
		if json.Unmarshal([]byte(raw), &episode) == nil && episode.Open {
			names = append(names, strings.TrimPrefix(key, "health."))
		}
	}
	sort.Strings(names)
	return names
}

func formatHeartbeatStatus(raw string, now time.Time) string {
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
	if err != nil {
		return "never"
	}
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	state := "fresh"
	if age > 30*time.Second {
		state = "stale"
	}
	return fmt.Sprintf("%s ago (%s)", age.Round(time.Second), state)
}

func (s *Service) healthStatusLines(ctx context.Context, now time.Time) []string {
	deadDeliveries, _ := s.store.DeliveryQueueDeadCount(ctx)
	daemonState, _ := s.store.ListState(ctx)
	openIncidents := openHealthIncidentNames(daemonState)
	incidentSummary := "none"
	if len(openIncidents) > 0 {
		incidentSummary = strings.Join(openIncidents, ", ")
	}
	return []string{
		fmt.Sprintf("Dead deliveries: %d", deadDeliveries),
		fmt.Sprintf("App-server heartbeat: %s", formatHeartbeatStatus(daemonState["appserver.poll.last_heartbeat_at"], now)),
		fmt.Sprintf("Open health incidents: %s", incidentSummary),
	}
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
	s.noteExternalPollerResult(ctx, source, err)
	key := healthKey(source)
	if key == "" {
		key = "external"
	}
	key += ".poll"
	now := s.now().UTC()
	s.healthMu.Lock()
	defer s.healthMu.Unlock()

	trackerKey := "health_tracker." + key
	tracker := s.loadExternalPollHealthState(ctx, trackerKey)
	episode := s.loadHealthEpisode(ctx, "health."+key)
	lastObserved := parseHealthTime(tracker.LastObservedAt)
	if lastObserved.IsZero() || now.Before(lastObserved) || now.Sub(lastObserved) > externalPollObservationGap {
		tracker.WarmupUntil = now.Add(externalPollResumeGrace).Format(time.RFC3339Nano)
		tracker.FailureSince = ""
		tracker.RecoverySince = ""
		tracker.RecoveredAfterResume = episode.Open
	}
	tracker.LastObservedAt = now.Format(time.RFC3339Nano)

	if err != nil {
		safeError := sanitizeDiagnosticString(err.Error())
		_ = s.store.SetState(ctx, key+".last_error", safeError)
		if now.Before(parseHealthTime(tracker.WarmupUntil)) {
			s.saveExternalPollHealthState(ctx, trackerKey, tracker)
			return
		}
		tracker.RecoverySince = ""
		failureSince := parseHealthTime(tracker.FailureSince)
		if failureSince.IsZero() {
			tracker.FailureSince = now.Format(time.RFC3339Nano)
		} else if !episode.Open && now.Sub(failureSince) >= externalPollFailureDelay {
			s.reportHealthFailureLockedToTopic(ctx, key, "External source polling failed: "+source, safeError, externalPollFailureAction(source, err), s.cfg.ExternalRequestsTopicID)
		}
		s.saveExternalPollHealthState(ctx, trackerKey, tracker)
		return
	}

	_ = s.store.SetState(ctx, key+".last_success_at", now.Format(time.RFC3339Nano))
	_ = s.store.SetState(ctx, key+".last_error", "")
	if now.Before(parseHealthTime(tracker.WarmupUntil)) {
		s.saveExternalPollHealthState(ctx, trackerKey, tracker)
		return
	}
	tracker.FailureSince = ""
	if episode.Open {
		recoverySince := parseHealthTime(tracker.RecoverySince)
		if recoverySince.IsZero() {
			tracker.RecoverySince = now.Format(time.RFC3339Nano)
		} else if now.Sub(recoverySince) >= externalPollRecoveryDelay {
			note := ""
			if tracker.RecoveredAfterResume {
				note = "Recovered after wake and stable connectivity."
			}
			s.reportHealthRecoveredLockedToTopic(ctx, key, "External source polling recovered: "+source, note, s.cfg.ExternalRequestsTopicID)
			tracker.RecoverySince = ""
			tracker.RecoveredAfterResume = false
		}
	} else {
		tracker.RecoverySince = ""
		tracker.RecoveredAfterResume = false
	}
	s.saveExternalPollHealthState(ctx, trackerKey, tracker)
}

func (s *Service) RegisterExternalPoller(ctx context.Context, source string, interval time.Duration, enabled bool) error {
	source = healthKey(source)
	if source == "" {
		return fmt.Errorf("poller source is required")
	}
	if interval <= 0 {
		return fmt.Errorf("poller interval must be positive")
	}
	state := externalPollerState{
		Source:         source,
		Enabled:        enabled,
		IntervalMillis: interval.Milliseconds(),
		RegisteredAt:   s.now().UTC().Format(time.RFC3339Nano),
	}
	return s.saveExternalPollerState(ctx, state)
}

func (s *Service) NoteExternalPollStarted(ctx context.Context, source string) {
	state := s.loadExternalPollerState(ctx, source)
	if state.Source == "" {
		state.Source = healthKey(source)
		state.Enabled = true
	}
	state.LastStartedAt = s.now().UTC().Format(time.RFC3339Nano)
	_ = s.saveExternalPollerState(ctx, state)
}

func (s *Service) noteExternalPollerResult(ctx context.Context, source string, resultErr error) {
	state := s.loadExternalPollerState(ctx, source)
	if state.Source == "" {
		state.Source = healthKey(source)
		state.Enabled = true
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	if state.LastStartedAt == "" {
		state.LastStartedAt = now
	}
	state.LastFinishedAt = now
	if resultErr == nil {
		state.LastSuccessAt = now
		state.LastError = ""
		state.ConsecutiveFailures = 0
	} else {
		state.LastError = sanitizeDiagnosticString(resultErr.Error())
		state.ConsecutiveFailures++
	}
	_ = s.saveExternalPollerState(ctx, state)
}

func (s *Service) loadExternalPollerState(ctx context.Context, source string) externalPollerState {
	raw, _ := s.store.GetState(ctx, "poller."+healthKey(source))
	var state externalPollerState
	_ = json.Unmarshal([]byte(raw), &state)
	return state
}

func (s *Service) saveExternalPollerState(ctx context.Context, state externalPollerState) error {
	if state.Source == "" {
		return nil
	}
	payload, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.store.SetState(ctx, "poller."+healthKey(state.Source), string(payload))
}

func (s *Service) PollersSnapshot(ctx context.Context) (string, error) {
	daemonState, err := s.store.ListState(ctx)
	if err != nil {
		return "", err
	}
	states := make([]externalPollerState, 0)
	for key, raw := range daemonState {
		if !strings.HasPrefix(key, "poller.") {
			continue
		}
		var state externalPollerState
		if json.Unmarshal([]byte(raw), &state) != nil || state.Source == "" {
			continue
		}
		states = append(states, state)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].Source < states[j].Source })
	lines := []string{"Source pollers"}
	if len(states) == 0 {
		return strings.Join(append(lines, "", "No pollers registered."), "\n"), nil
	}
	now := s.now().UTC()
	for _, state := range states {
		marker, status := externalPollerDisplayState(state, now)
		interval := time.Duration(state.IntervalMillis) * time.Millisecond
		line := fmt.Sprintf("%s %s · %s", marker, state.Source, status)
		if interval > 0 {
			line += " · every " + interval.String()
		}
		lines = append(lines, "", line)
		if state.LastFinishedAt != "" {
			lines = append(lines, "Last cycle: "+formatPollerObservationAge(state.LastFinishedAt, now))
		}
		if state.LastSuccessAt != "" {
			lines = append(lines, "Last success: "+formatPollerObservationAge(state.LastSuccessAt, now))
		}
		if state.ConsecutiveFailures > 0 {
			lines = append(lines, fmt.Sprintf("Consecutive failures: %d", state.ConsecutiveFailures))
		}
		if state.LastError != "" {
			lines = append(lines, "Error: "+state.LastError)
		}
	}
	return strings.Join(lines, "\n"), nil
}

func externalPollerDisplayState(state externalPollerState, now time.Time) (string, string) {
	if !state.Enabled {
		return "⚪", "disabled"
	}
	interval := time.Duration(state.IntervalMillis) * time.Millisecond
	staleAfter := 2 * interval
	if staleAfter < 30*time.Second {
		staleAfter = 30 * time.Second
	}
	started := parseHealthTime(state.LastStartedAt)
	finished := parseHealthTime(state.LastFinishedAt)
	latest := finished
	if started.After(finished) {
		latest = started
	}
	if latest.IsZero() {
		return "🟡", "waiting"
	}
	age := now.Sub(latest)
	if age < 0 {
		age = 0
	}
	if age > staleAfter {
		return "🟠", "stale"
	}
	if started.After(finished) {
		return "🔵", "polling"
	}
	if state.LastError != "" {
		return "🔴", "failing"
	}
	return "🟢", "healthy"
}

func formatPollerObservationAge(raw string, now time.Time) string {
	at := parseHealthTime(raw)
	if at.IsZero() {
		return "never"
	}
	age := now.Sub(at)
	if age < 0 {
		age = 0
	}
	return age.Round(time.Second).String() + " ago"
}

func (s *Service) loadExternalPollHealthState(ctx context.Context, key string) externalPollHealthState {
	raw, _ := s.store.GetState(ctx, key)
	var state externalPollHealthState
	_ = json.Unmarshal([]byte(raw), &state)
	return state
}

func (s *Service) saveExternalPollHealthState(ctx context.Context, key string, state externalPollHealthState) {
	payload, _ := json.Marshal(state)
	_ = s.store.SetState(ctx, key, string(payload))
}

func parseHealthTime(value string) time.Time {
	at, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return at
}

func externalPollFailureAction(source string, err error) string {
	lower := strings.ToLower(strings.TrimSpace(fmt.Sprint(err)))
	for _, marker := range []string{"no route to host", "network is unreachable", "no such host", "lookup ", "timeout", "deadline exceeded", "unexpected eof"} {
		if strings.Contains(lower, marker) {
			return "Wait for stable network connectivity. If the failure persists while online, verify access for " + strings.TrimSpace(source) + "."
		}
	}
	return "Check credentials and API or CLI access for " + strings.TrimSpace(source) + "."
}
