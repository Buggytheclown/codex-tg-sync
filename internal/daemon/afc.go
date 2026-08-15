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
	PrepareAFCControl(ctx context.Context) error
	CreateAFCTopic(ctx context.Context, title string) (int64, error)
	DeleteAFCTopic(ctx context.Context, topicID int64) error
	SendAFCMessage(ctx context.Context, topicID int64, text string, silent bool) (int64, error)
	SendAFCActionMessage(ctx context.Context, topicID int64, text string, buttons [][]model.ButtonSpec) (int64, error)
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
	SnapshotAt string              `json:"snapshot_at"`
	Selected   int                 `json:"selected"`
	Created    int                 `json:"created"`
	Failed     []string            `json:"failed,omitempty"`
	Unknown    []string            `json:"unknown,omitempty"`
	Items      []afcActivationItem `json:"items"`
}

type afcActivationItem struct {
	Rank     int    `json:"rank"`
	ThreadID string `json:"thread_id"`
	Title    string `json:"title"`
	Telegram string `json:"telegram"`
	Codex    string `json:"codex"`
	Error    string `json:"error,omitempty"`
}

func (s *Service) isAFCGroup(chatID int64) bool {
	return s.cfg.AFCGroupID != 0 && chatID == s.cfg.AFCGroupID
}

func isAFCControlTopic(topicID int64) bool { return topicID == 0 || topicID == afcControlTopicID }

func (s *Service) handleAFCMessage(ctx context.Context, topicID, messageID, userID int64, text string) (*DirectResponse, error) {
	text = strings.TrimSpace(text)
	if isAFCControlTopic(topicID) {
		fields := strings.Fields(strings.ToLower(text))
		if len(fields) > 0 {
			fields[0], _, _ = strings.Cut(fields[0], "@")
		}
		switch {
		case len(fields) == 1 && fields[0] == "/afc":
			state, err := s.store.GetAFCState(ctx)
			if err != nil {
				return nil, err
			}
			if state.State == model.AFCStateActive || state.State == model.AFCStateDraining {
				return s.deactivateAFC(ctx)
			}
			return s.activateAFC(ctx, userID)
		case len(fields) == 2 && fields[0] == "/afc" && fields[1] == "on":
			return s.activateAFC(ctx, userID)
		case len(fields) == 3 && fields[0] == "/afc" && fields[1] == "off" && fields[2] == "--force":
			return s.forceDeactivateAFC(ctx)
		case len(fields) == 2 && fields[0] == "/afc" && fields[1] == "off":
			return s.deactivateAFC(ctx)
		case len(fields) == 1 && fields[0] == "/status":
			return s.afcStatus(ctx)
		case len(fields) == 1 && (fields[0] == "/projects" || fields[0] == "/newchat"):
			return s.afcProjectsMenu(ctx, topicID)
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
	if text == "" {
		return &DirectResponse{Text: "AFC topic requires a non-empty plain-text prompt."}, nil
	}
	if strings.EqualFold(text, "/stop") || strings.HasPrefix(strings.ToLower(text), "/stop@") {
		return s.stopAFCTurn(ctx, topicID)
	}
	if strings.HasPrefix(text, "/") {
		return &DirectResponse{Text: "AFC topic accepts a plain-text prompt. Topic commands arrive with the control rollout."}, nil
	}
	return s.dispatchAFCMessage(ctx, *topic, messageID, text)
}

func (s *Service) dispatchAFCMessage(ctx context.Context, topic model.AFCTopic, messageID int64, text string) (*DirectResponse, error) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	receipt, created, err := s.store.AcceptAFCMessage(ctx, topic.ChatID, topic.TopicID, messageID)
	if err != nil {
		return nil, err
	}
	if !created {
		return afcDuplicateReceiptResponse(receipt), nil
	}
	if topic.ActiveTurnState == model.AFCTurnStarting || topic.ActiveTurnState == model.AFCTurnActive || topic.ActiveTurnState == model.AFCTurnUnknown {
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
		return &DirectResponse{Text: "This AFC topic is already active or ownership-unknown. The new message was not queued."}, nil
	}
	lease, err := s.afcWriter.Reserve(ctx, topic.ThreadID)
	if err != nil {
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
		var claimErr *appserver.ThreadClaimError
		if errors.As(err, &claimErr) {
			return &DirectResponse{Text: fmt.Sprintf("This thread is owned by %s writer generation %d; AFC did not mutate App Server.", claimErr.Current.Writer, claimErr.Current.Generation)}, nil
		}
		return &DirectResponse{Text: fmt.Sprintf("AFC could not reserve this thread: %v", err)}, nil
	}
	if err := s.store.MarkAFCStarting(ctx, receipt, lease.Generation); err != nil {
		_ = s.afcWriter.Abort(lease)
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
		return &DirectResponse{Text: "This AFC topic already has unfinished work. The message was not dispatched."}, nil
	}
	s.afcLeases[topic.ThreadID] = lease
	s.installAFCWriterLocked(lease)
	thread, _ := s.store.GetThread(ctx, topic.ThreadID)
	cwd := ""
	if thread != nil {
		cwd = thread.CWD
	}
	if _, err := lease.Process.ThreadResume(ctx, topic.ThreadID, cwd); err != nil {
		delete(s.afcLeases, topic.ThreadID)
		_ = s.afcWriter.Abort(lease)
		_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptRejected, model.AFCTurnTerminal, lease.Generation)
		return &DirectResponse{Text: fmt.Sprintf("AFC could not resume the thread; the prompt was not sent: %v", err)}, nil
	}
	result, turnErr := lease.Process.TurnStart(ctx, topic.ThreadID, text, cwd, s.turnStartOptions(ctx, "", thread))
	turnID := appserverThreadTurnID(result)
	if turnErr != nil || strings.TrimSpace(turnID) == "" {
		if afcDispatchAmbiguous(turnErr, turnID) {
			_ = s.afcWriter.MarkUnknown(lease)
			_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptUnknown, model.AFCTurnUnknown, lease.Generation)
			return &DirectResponse{Text: "AFC dispatch outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		delete(s.afcLeases, topic.ThreadID)
		_ = s.afcWriter.Abort(lease)
		_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptRejected, model.AFCTurnTerminal, lease.Generation)
		return &DirectResponse{Text: fmt.Sprintf("AFC rejected the prompt before dispatch: %v", turnErr)}, nil
	}
	if err := s.afcWriter.MarkActive(lease); err != nil {
		_ = s.afcWriter.MarkUncertain(lease)
		_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptUnknown, model.AFCTurnUnknown, lease.Generation)
		return &DirectResponse{Text: "AFC dispatched the request but could not confirm local ownership; outcome is unknown."}, nil
	}
	if err := s.store.MarkAFCDispatchState(ctx, receipt, model.AFCReceiptDispatched, turnID, model.AFCTurnActive, lease.Generation); err != nil {
		_ = s.afcWriter.MarkUncertain(lease)
		_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptUnknown, model.AFCTurnUnknown, lease.Generation)
		return &DirectResponse{Text: "AFC dispatched the request but could not persist confirmation; outcome is unknown."}, nil
	}
	_ = s.markTelegramOriginTurnFromTelegram(ctx, topic.ThreadID, turnID, topic.ChatID, topic.TopicID)
	if thread != nil {
		s.ensureStartedTurnSnapshot(ctx, thread, turnID)
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC turn started: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
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
	if state.State == model.AFCStateActive || state.State == model.AFCStateDraining {
		return &DirectResponse{Text: "AFC is already active. The activation snapshot is immutable; use /status."}, nil
	}
	if err := forum.ValidateAFCGroup(ctx, userID); err != nil {
		return nil, fmt.Errorf("validate AFC group: %w", err)
	}
	if err := forum.PrepareAFCControl(ctx); err != nil {
		return nil, fmt.Errorf("prepare AFC Control: %w", err)
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
		summary.Items = append(summary.Items, afcActivationItem{Rank: rank + 1, ThreadID: thread.ID, Title: afcTopicTitle(thread), Telegram: "pending", Codex: afcCodexStatus(thread)})
	}
	for rank, thread := range filtered {
		title := afcTopicTitle(thread)
		topicID, createErr := forum.CreateAFCTopic(ctx, title)
		if createErr != nil {
			var failure *AFCForumFailure
			entry := fmt.Sprintf("%s: %s", thread.ID, createErr)
			if errors.As(createErr, &failure) && failure.Kind == AFCForumFailureDefinitive {
				summary.Failed = append(summary.Failed, entry)
				summary.Items[rank].Telegram = "create failed"
			} else {
				summary.Unknown = append(summary.Unknown, entry)
				summary.Items[rank].Telegram = "create outcome unknown"
			}
			summary.Items[rank].Error = createErr.Error()
			continue
		}
		if err := s.store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: sessionID, ChatID: s.cfg.AFCGroupID,
			TopicID: topicID, ThreadID: thread.ID, Rank: rank + 1, Title: title, TelegramState: model.AFCTopicConnected}); err != nil {
			_ = forum.DeleteAFCTopic(ctx, topicID)
			summary.Failed = append(summary.Failed, fmt.Sprintf("%s: persist: %s", thread.ID, err))
			summary.Items[rank].Telegram, summary.Items[rank].Error = "binding failed", err.Error()
			continue
		}
		summary.Created++
		summary.Items[rank].Telegram = "connected"
		_ = s.store.UpsertThread(ctx, thread)
	}
	summaryJSON, _ := json.Marshal(summary)
	if err := s.store.FinishAFCActivation(ctx, sessionID, string(summaryJSON), summary.Created > 0); err != nil {
		return nil, err
	}
	if summary.Created > 0 {
		_ = s.afcWriter.AcceptNewWork()
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
	writer := s.afcWriter.Snapshot()
	topics, _ := s.store.ListAFCTopics(ctx, state.SessionID)
	if writer.Starting+writer.Active+writer.Unknown > 0 || hasUnfinishedAFCTopics(topics) {
		return &DirectResponse{Text: "AFC off refused; unfinished topics: " + strings.Join(unfinishedAFCTopicTitles(topics), ", ") + ". Use /afc off --force to drain."}, nil
	}
	topics, err = s.store.MarkAFCOff(ctx, state.SessionID)
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
	if err != nil || (state.State != model.AFCStateActive && state.State != model.AFCStateDraining) {
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
		s.processAFCSnapshotLocked(ctx, state, forum, topic, current, "afc_poll")
	}
}

func (s *Service) installAFCWriterLocked(lease appserver.WriterLease[Session]) {
	if s.afcEventProcess == lease.Process && s.afcEventGeneration == lease.Generation {
		return
	}
	if s.afcEventCancel != nil {
		s.afcEventCancel()
	}
	events := lease.Process.Subscribe()
	s.mu.RLock()
	baseCtx, started := s.runCtx, s.started
	s.mu.RUnlock()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	loopCtx, cancel := context.WithCancel(baseCtx)
	s.afcEventProcess, s.afcEventGeneration, s.afcEventCancel = lease.Process, lease.Generation, cancel
	if events == nil {
		return
	}
	loop := func() { s.afcWriterEventLoop(loopCtx, lease.Process, events, lease.Generation) }
	if started {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); loop() }()
	} else {
		go loop()
	}
}

func (s *Service) afcWriterEventLoop(ctx context.Context, process Session, events <-chan appserver.Event, generation uint64) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			s.handleAFCWriterEvent(ctx, process, event, generation)
		}
	}
}

func (s *Service) handleAFCWriterEvent(ctx context.Context, process Session, event appserver.Event, generation uint64) {
	threadID := threadIDFromEvent(event)
	if threadID == "" {
		return
	}
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	if s.afcEventProcess != process || s.afcEventGeneration != generation {
		return
	}
	lease, ok := s.afcLeases[threadID]
	if !ok || lease.Generation != generation {
		return
	}
	state, err := s.store.GetAFCState(ctx)
	if err != nil || (state.State != model.AFCStateActive && state.State != model.AFCStateDraining) {
		return
	}
	topic, err := s.store.GetActiveAFCTopicByThread(ctx, state.SessionID, threadID)
	if err != nil || topic == nil || topic.WriterGeneration != generation {
		return
	}
	if eventTurnID := afcEventTurnID(event); eventTurnID != "" && topic.ActiveTurnID != "" && eventTurnID != topic.ActiveTurnID {
		return
	}
	if approval, ok := appserver.PendingApprovalFromServerRequest(event); ok {
		s.handleAFCPendingRequestLocked(ctx, *topic, *approval)
		return
	}
	if strings.EqualFold(event.Method, "serverRequest/resolved") {
		if requestID := payloadMapString(event.Params, "requestId"); requestID != "" {
			_ = s.store.ExpireAFCCallbackRoutesByRequest(ctx, requestID)
		}
		return
	}
	payload, err := process.ThreadRead(ctx, threadID, true)
	if err != nil || payload == nil {
		return
	}
	current := appserver.SnapshotFromThreadRead(payload)
	forum := s.getAFCForum()
	s.processAFCSnapshotLocked(ctx, state, forum, *topic, current, "afc_event")
}

func (s *Service) processAFCSnapshotLocked(ctx context.Context, state model.AFCState, forum AFCForum, topic model.AFCTopic, current appserver.ThreadReadSnapshot, operation string) {
	activeTurnID := strings.TrimSpace(topic.ActiveTurnID)
	currentTurnID := strings.TrimSpace(current.LatestTurnID)
	if topic.ActiveTurnState == model.AFCTurnActive && activeTurnID != "" && currentTurnID != "" && currentTurnID != activeTurnID {
		return
	}
	if topic.ActiveTurnState == model.AFCTurnActive &&
		activeTurnID != "" &&
		currentTurnID == activeTurnID {
		previous, _ := s.store.GetSnapshot(ctx, topic.ThreadID)
		if s.applyTelegramOriginTerminalGate(ctx, operation, &current, previous) {
			return
		}
	}
	if forum != nil {
		s.persistAndDeliverAFCSnapshotLocked(ctx, forum, topic, current)
	}
	s.completeAFCTurnLocked(ctx, state, topic, current)
}

func (s *Service) completeAFCTurnLocked(ctx context.Context, state model.AFCState, topic model.AFCTopic, current appserver.ThreadReadSnapshot) {
	if !isTerminalStatus(current.LatestTurnStatus) || strings.TrimSpace(current.LatestTurnID) == "" || current.LatestTurnID != topic.ActiveTurnID {
		return
	}
	lease, ok := s.afcLeases[topic.ThreadID]
	if !ok || lease.Generation != topic.WriterGeneration {
		return
	}
	if err := s.store.MarkAFCTerminal(ctx, state.SessionID, topic.ThreadID, current.LatestTurnID, topic.WriterGeneration); err != nil {
		return
	}
	_ = s.store.ExpireAFCCallbackRoutes(ctx, topic.ThreadID, current.LatestTurnID)
	delete(s.afcLeases, topic.ThreadID)
	if err := s.afcWriter.MarkTerminal(lease); err != nil {
		return
	}
	if s.afcWriter.Snapshot().State == appserver.WriterStopped && s.afcEventCancel != nil {
		s.afcEventCancel()
		s.afcEventCancel, s.afcEventProcess, s.afcEventGeneration = nil, nil, 0
	}
}

func (s *Service) persistAndDeliverAFCSnapshotLocked(ctx context.Context, forum AFCForum, topic model.AFCTopic, current appserver.ThreadReadSnapshot) {
	previous, _ := s.store.GetSnapshot(ctx, topic.ThreadID)
	compact := appserver.CompactSnapshot(previous, current, time.Now().UTC())
	_ = s.store.UpsertThread(ctx, current.Thread)
	_ = s.store.UpsertSnapshot(ctx, topic.ThreadID, compact)
	statusText := renderAFCStatus(current)
	renderFP := afcFingerprint(statusText)
	statusID := topic.StatusMessageID
	statusTurnID := strings.TrimSpace(topic.StatusTurnID)
	currentTurnID := strings.TrimSpace(current.LatestTurnID)
	newObservedTurn := statusTurnID != "" && currentTurnID != "" && statusTurnID != currentTurnID
	var deliveryErr error
	if statusID == 0 || newObservedTurn {
		statusID, deliveryErr = forum.SendAFCMessage(ctx, topic.TopicID, statusText, true)
	} else if renderFP != topic.LastRenderFP {
		deliveryErr = forum.EditAFCMessage(ctx, topic.TopicID, statusID, statusText)
	}
	if deliveryErr != nil {
		return
	}
	if currentTurnID != "" {
		statusTurnID = currentTurnID
	}
	finalFP := topic.LastFinalFP
	if strings.TrimSpace(current.LatestFinalFP) != "" && current.LatestFinalFP != topic.LastFinalFP {
		if _, deliveryErr = forum.SendAFCMessage(ctx, topic.TopicID, "[Final]\n"+strings.TrimSpace(current.LatestFinalText), false); deliveryErr == nil {
			finalFP = current.LatestFinalFP
		}
	}
	_ = s.store.UpdateAFCTopicDelivery(ctx, topic.SessionID, topic.TopicID, statusID, statusTurnID, renderFP, finalFP)
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
	for _, item := range summary.Items {
		text += fmt.Sprintf("\n\n%d. %s\nTelegram: %s\nCodex: %s", item.Rank, item.Title, item.Telegram, item.Codex)
		if item.Error != "" {
			text += "\nError: " + item.Error
		}
	}
	return text
}

func afcCodexStatus(thread model.Thread) string {
	status := strings.ToLower(strings.TrimSpace(thread.Status))
	switch {
	case strings.Contains(status, "waiting"):
		return "waiting"
	case strings.Contains(status, "progress"), strings.Contains(status, "running"), strings.Contains(status, "active"):
		return "running"
	case strings.Contains(status, "complete"):
		return "completed"
	case strings.Contains(status, "interrupt"), strings.Contains(status, "cancel"), strings.Contains(status, "failed"):
		return "interrupted"
	default:
		return "unknown"
	}
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

func afcDuplicateReceiptResponse(receipt model.AFCMessageReceipt) *DirectResponse {
	switch receipt.State {
	case model.AFCReceiptDispatched:
		return &DirectResponse{Text: "This Telegram message was already dispatched; it will not be replayed."}
	case model.AFCReceiptUnknown:
		return &DirectResponse{Text: "This Telegram message has an unknown dispatch outcome and will never be replayed automatically."}
	case model.AFCReceiptRejected:
		return &DirectResponse{Text: "This Telegram message was already rejected and will not be replayed."}
	default:
		return &DirectResponse{Text: "This Telegram message was already accepted; duplicate delivery was ignored."}
	}
}

func afcDispatchAmbiguous(err error, turnID string) bool {
	if err == nil {
		return strings.TrimSpace(turnID) == ""
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"timeout", "deadline", "context canceled", "closed before response", "eof", "broken pipe", "connection reset"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func afcEventTurnID(event appserver.Event) string {
	if event.Params == nil {
		return ""
	}
	if value, ok := event.Params["turnId"].(string); ok {
		return strings.TrimSpace(value)
	}
	if turn, ok := event.Params["turn"].(map[string]any); ok {
		if value, ok := turn["id"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
