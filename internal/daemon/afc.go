package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const (
	afcControlTopicID = int64(1)
	afcTopicLimit     = 8
)

// AFCForum is deliberately scoped to the configured AFC group. Its
// implementation must not accept a chat id, so daemon code cannot accidentally
// route AFC traffic into a legacy chat.
type AFCForum interface {
	ValidateAFCGroup(ctx context.Context, allowedUserID int64) error
	CreateAFCTopic(ctx context.Context, title string) (int64, error)
	DeleteAFCTopic(ctx context.Context, topicID int64) error
	SendAFCMessage(ctx context.Context, topicID int64, text string, silent bool) (int64, error)
	EditAFCMessage(ctx context.Context, topicID, messageID int64, text string) error
}

type AFCForumFailureKind string

const (
	AFCForumFailureDefinitive AFCForumFailureKind = "definitive"
	AFCForumFailureUnknown    AFCForumFailureKind = "unknown"
)

type AFCForumFailure struct {
	Kind AFCForumFailureKind
	Err  error
}

func (e *AFCForumFailure) Error() string { return e.Err.Error() }
func (e *AFCForumFailure) Unwrap() error { return e.Err }

func NewAFCForumFailure(kind AFCForumFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &AFCForumFailure{Kind: kind, Err: err}
}

type afcActivationSummary struct {
	SnapshotAt string   `json:"snapshot_at"`
	Selected   int      `json:"selected"`
	Created    int      `json:"created"`
	Failed     []string `json:"failed,omitempty"`
	Unknown    []string `json:"unknown,omitempty"`
}

func (s *Service) isAFCGroup(chatID int64) bool {
	return s.cfg.AFCGroupID != 0 && chatID == s.cfg.AFCGroupID
}

func isAFCControlTopic(topicID int64) bool { return topicID == 0 || topicID == afcControlTopicID }

func (s *Service) handleAFCMessage(ctx context.Context, topicID, userID int64, text string) (*DirectResponse, error) {
	text = strings.TrimSpace(text)
	if isAFCControlTopic(topicID) {
		fields := strings.Fields(strings.ToLower(text))
		switch {
		case len(fields) == 1 && fields[0] == "/afc":
			state, err := s.store.GetAFCState(ctx)
			if err != nil {
				return nil, err
			}
			if state.State == model.AFCStateActive {
				return s.deactivateAFC(ctx)
			}
			return s.activateAFC(ctx, userID)
		case len(fields) == 2 && fields[0] == "/afc" && fields[1] == "on":
			return s.activateAFC(ctx, userID)
		case len(fields) >= 2 && fields[0] == "/afc" && fields[1] == "off":
			return s.deactivateAFC(ctx)
		case len(fields) == 1 && fields[0] == "/status":
			return s.afcStatus(ctx)
		default:
			return &DirectResponse{Text: "AFC Control accepts /afc on, /afc off, and /status. Legacy commands are disabled in this group."}, nil
		}
	}
	topic, err := s.store.GetActiveAFCTopic(ctx, s.cfg.AFCGroupID, topicID)
	if err != nil {
		return nil, err
	}
	if topic == nil {
		return &DirectResponse{Text: "AFC topic is stale or unknown. Use /status in Control."}, nil
	}
	return &DirectResponse{Text: "AFC topic is connected in read-only mode. Interactive turns arrive in the next rollout step."}, nil
}

func (s *Service) activateAFC(ctx context.Context, userID int64) (*DirectResponse, error) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	if s.cfg.AFCGroupID == 0 {
		return nil, errors.New("CTR_GO_AFC_GROUP_ID is not configured")
	}
	if len(s.cfg.AllowedUserIDs) != 1 || s.cfg.AllowedUserIDs[0] != userID {
		return nil, errors.New("AFC requires exactly one configured allowed user")
	}
	forum := s.getAFCForum()
	if forum == nil {
		return nil, errors.New("AFC Telegram transport is unavailable")
	}
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State == model.AFCStateActive {
		return &DirectResponse{Text: "AFC is already active. The activation snapshot is immutable; use /status."}, nil
	}
	if err := forum.ValidateAFCGroup(ctx, userID); err != nil {
		return nil, fmt.Errorf("validate AFC group: %w", err)
	}
	s.cleanupAFCTopics(ctx, state.SessionID)

	poll, err := s.controlReadSession(ctx)
	if err != nil {
		return nil, err
	}
	result, err := poll.ThreadList(ctx, 50, "")
	if err != nil {
		return nil, fmt.Errorf("AFC activation thread/list: %w", err)
	}
	threads := appserver.ThreadsFromList(result)
	filtered := threads[:0]
	for _, thread := range threads {
		if thread.ID != "" && !thread.Archived && !thread.IsInternal() {
			filtered = append(filtered, thread)
		}
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].UpdatedAt == filtered[j].UpdatedAt {
			return filtered[i].ID < filtered[j].ID
		}
		return filtered[i].UpdatedAt > filtered[j].UpdatedAt
	})
	if len(filtered) > afcTopicLimit {
		filtered = filtered[:afcTopicLimit]
	}
	now := time.Now().UTC()
	sessionID := randomToken()
	if err := s.store.BeginAFCActivation(ctx, sessionID, s.cfg.AFCGroupID); err != nil {
		return nil, err
	}
	summary := afcActivationSummary{SnapshotAt: now.Format(time.RFC3339Nano), Selected: len(filtered)}
	for rank, thread := range filtered {
		title := afcTopicTitle(thread)
		topicID, createErr := forum.CreateAFCTopic(ctx, title)
		if createErr != nil {
			var failure *AFCForumFailure
			entry := fmt.Sprintf("%s: %s", thread.ID, createErr)
			if errors.As(createErr, &failure) && failure.Kind == AFCForumFailureDefinitive {
				summary.Failed = append(summary.Failed, entry)
			} else {
				summary.Unknown = append(summary.Unknown, entry)
			}
			continue
		}
		if err := s.store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: sessionID, ChatID: s.cfg.AFCGroupID,
			TopicID: topicID, ThreadID: thread.ID, Rank: rank + 1, Title: title, TelegramState: model.AFCTopicConnected}); err != nil {
			_ = forum.DeleteAFCTopic(ctx, topicID)
			summary.Failed = append(summary.Failed, fmt.Sprintf("%s: persist: %s", thread.ID, err))
			continue
		}
		summary.Created++
		_ = s.store.UpsertThread(ctx, thread)
	}
	summaryJSON, _ := json.Marshal(summary)
	if err := s.store.FinishAFCActivation(ctx, sessionID, string(summaryJSON), summary.Created > 0); err != nil {
		return nil, err
	}
	return &DirectResponse{Text: renderAFCActivationSummary(summary)}, nil
}

func (s *Service) deactivateAFC(ctx context.Context) (*DirectResponse, error) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State == model.AFCStateOff || state.SessionID == "" {
		return &DirectResponse{Text: "AFC is already off. Legacy lifecycle remains unchanged."}, nil
	}
	topics, err := s.store.MarkAFCOff(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	forum := s.getAFCForum()
	deleted, pending := 0, 0
	for _, topic := range topics {
		if topic.TelegramState != model.AFCTopicCleanup {
			continue
		}
		if forum == nil || forum.DeleteAFCTopic(ctx, topic.TopicID) != nil {
			pending++
			continue
		}
		if err := s.store.DeleteAFCTopic(ctx, topic.SessionID, topic.TopicID); err != nil {
			pending++
		} else {
			deleted++
		}
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC off: deleted %d topic(s), cleanup pending %d. Legacy observer and writer were not restored.", deleted, pending)}, nil
}

func (s *Service) afcStatus(ctx context.Context) (*DirectResponse, error) {
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return nil, err
	}
	topics, err := s.store.ListAFCTopics(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC state: %s\nSession: %s\nConnected topics: %d\nActivation summary: %s",
		state.State, state.SessionID, countConnectedAFCTopics(topics), strings.TrimSpace(state.ActivationSummaryJSON))}, nil
}

func (s *Service) syncAFC(ctx context.Context) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	state, err := s.store.GetAFCState(ctx)
	if err != nil || state.State != model.AFCStateActive {
		return
	}
	forum := s.getAFCForum()
	if forum == nil {
		return
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if !connected || poll == nil {
		return
	}
	topics, err := s.store.ListAFCTopics(ctx, state.SessionID)
	if err != nil {
		return
	}
	for _, topic := range topics {
		if topic.TelegramState != model.AFCTopicConnected {
			continue
		}
		payload, readErr := poll.ThreadRead(ctx, topic.ThreadID, true)
		if readErr != nil || payload == nil {
			continue
		}
		current := appserver.SnapshotFromThreadRead(payload)
		previous, _ := s.store.GetSnapshot(ctx, topic.ThreadID)
		compact := appserver.CompactSnapshot(previous, current, time.Now().UTC())
		_ = s.store.UpsertThread(ctx, current.Thread)
		_ = s.store.UpsertSnapshot(ctx, topic.ThreadID, compact)
		statusText := renderAFCStatus(current)
		renderFP := afcFingerprint(statusText)
		statusID := topic.StatusMessageID
		var deliveryErr error
		if statusID == 0 {
			statusID, deliveryErr = forum.SendAFCMessage(ctx, topic.TopicID, statusText, true)
		} else if renderFP != topic.LastRenderFP {
			deliveryErr = forum.EditAFCMessage(ctx, topic.TopicID, statusID, statusText)
		}
		if deliveryErr != nil {
			continue
		}
		finalFP := topic.LastFinalFP
		if strings.TrimSpace(current.LatestFinalFP) != "" && current.LatestFinalFP != topic.LastFinalFP {
			if _, deliveryErr = forum.SendAFCMessage(ctx, topic.TopicID, "[Final]\n"+strings.TrimSpace(current.LatestFinalText), false); deliveryErr == nil {
				finalFP = current.LatestFinalFP
			}
		}
		_ = s.store.UpdateAFCTopicDelivery(ctx, topic.SessionID, topic.TopicID, statusID, renderFP, finalFP)
	}
}

func (s *Service) cleanupAFCTopics(ctx context.Context, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	forum := s.getAFCForum()
	if forum == nil {
		return
	}
	topics, _ := s.store.ListAFCTopics(ctx, sessionID)
	for _, topic := range topics {
		if topic.TelegramState == model.AFCTopicCleanup && forum.DeleteAFCTopic(ctx, topic.TopicID) == nil {
			_ = s.store.DeleteAFCTopic(ctx, sessionID, topic.TopicID)
		}
	}
}

func (s *Service) getAFCForum() AFCForum { s.mu.RLock(); defer s.mu.RUnlock(); return s.afcForum }

func afcTopicTitle(thread model.Thread) string {
	title := strings.TrimSpace(thread.Title)
	if title == "" {
		title = thread.ShortID()
	}
	for utf8.RuneCountInString(title) > 128 {
		_, size := utf8.DecodeLastRuneInString(title)
		title = title[:len(title)-size]
	}
	return title
}

func renderAFCActivationSummary(summary afcActivationSummary) string {
	text := fmt.Sprintf("AFC activation snapshot %s\nselected: %d\nactive: %d\nfailed: %d\nunknown: %d", summary.SnapshotAt, summary.Selected, summary.Created, len(summary.Failed), len(summary.Unknown))
	if len(summary.Failed) > 0 {
		text += "\n" + strings.Join(summary.Failed, "\n")
	}
	if len(summary.Unknown) > 0 {
		text += "\n" + strings.Join(summary.Unknown, "\n")
	}
	return text
}

func renderAFCStatus(snapshot appserver.ThreadReadSnapshot) string {
	status := strings.TrimSpace(snapshot.LatestTurnStatus)
	if snapshot.WaitingOnApproval || snapshot.WaitingOnReply {
		status = "waiting"
	}
	if status == "" {
		status = strings.TrimSpace(snapshot.Thread.Status)
	}
	if status == "" {
		status = "unknown"
	}
	detail := strings.TrimSpace(snapshot.LatestProgressText)
	if detail == "" && len(snapshot.LatestAgentMessages) > 0 {
		detail = strings.TrimSpace(snapshot.LatestAgentMessages[len(snapshot.LatestAgentMessages)-1])
	}
	if detail == "" {
		detail = strings.TrimSpace(snapshot.Thread.LastPreview)
	}
	text := "[Status]\n" + status
	if detail != "" {
		text += "\n" + detail
	}
	return text
}

func afcFingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:12])
}
func countConnectedAFCTopics(topics []model.AFCTopic) int {
	n := 0
	for _, topic := range topics {
		if topic.TelegramState == model.AFCTopicConnected {
			n++
		}
	}
	return n
}
