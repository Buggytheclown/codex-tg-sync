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
	"unicode/utf16"
	"unicode/utf8"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tgformat"
)

const (
	afcControlTopicID     = int64(1)
	afcGeneralSendTopicID = int64(0)
)

// AFCForum is deliberately scoped to the configured AFC group. Its
// implementation must not accept a chat id, so daemon code cannot accidentally
// route AFC traffic into a legacy chat.
type AFCForum interface {
	ValidateAFCGroup(ctx context.Context, allowedUserID int64) error
	PrepareAFCControl(ctx context.Context) error
	CreateAFCTopic(ctx context.Context, title string) (int64, error)
	RenameAFCTopic(ctx context.Context, topicID int64, title string) error
	DeleteAFCTopic(ctx context.Context, topicID int64) error
	DeleteAFCMessage(ctx context.Context, topicID, messageID int64) error
	SendAFCMessage(ctx context.Context, topicID int64, message model.RenderedMessage, silent bool) (int64, error)
	SendAFCActionMessage(ctx context.Context, topicID int64, text string, buttons [][]model.ButtonSpec) (int64, error)
	EditAFCMessage(ctx context.Context, topicID, messageID int64, message model.RenderedMessage) error
}

const (
	afcUserHeader     = "👤 [User]"
	afcStatusHeader   = "⏱ [Status]"
	afcFinalHeader    = "✅ [Final]"
	afcApprovalHeader = "🔐 [Approval]"
	afcInputHeader    = "❓ [Input]"
)

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
		case len(fields) == 1 && fields[0] == "/sync":
			state, err := s.store.GetAFCState(ctx)
			if err != nil {
				return nil, err
			}
			if state.State == model.AFCStateActive || state.State == model.AFCStateDraining {
				return s.deactivateAFC(ctx)
			}
			return s.activateAFC(ctx, userID)
		case len(fields) == 2 && fields[0] == "/sync" && fields[1] == "on":
			return s.activateAFC(ctx, userID)
		case len(fields) == 3 && fields[0] == "/sync" && fields[1] == "off" && fields[2] == "--force":
			return s.forceDeactivateAFC(ctx)
		case len(fields) == 2 && fields[0] == "/sync" && fields[1] == "off":
			return s.deactivateAFC(ctx)
		case len(fields) == 1 && fields[0] == "/status":
			return s.afcStatus(ctx)
		case len(fields) == 1 && fields[0] == "/refresh":
			return s.syncAFCCommand(ctx)
		case len(fields) == 1 && fields[0] == "/repair":
			if err := s.RequestRepair(ctx, "telegram"); err != nil {
				return nil, err
			}
			return &DirectResponse{Text: "Repair requested. App-server sessions will be recreated in the background."}, nil
		case len(fields) == 1 && (fields[0] == "/projects" || fields[0] == "/newchat"):
			return s.afcProjectsMenu(ctx, topicID)
		default:
			return &DirectResponse{Text: "Sync Control accepts /sync on, /sync off, /status, /refresh, /repair, /projects, and /newchat. Legacy commands are disabled in this group."}, nil
		}
	}
	topic, err := s.store.GetActiveAFCTopic(ctx, s.cfg.AFCGroupID, topicID)
	if err != nil {
		return nil, err
	}
	if topic == nil {
		draft, draftErr := s.store.GetActiveAFCTopicDraft(ctx, s.cfg.AFCGroupID, topicID)
		if draftErr != nil {
			return nil, draftErr
		}
		if draft != nil {
			return s.handleAFCDraftMessage(ctx, *draft, messageID, text)
		}
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
	if topic.ActiveTurnState == model.AFCTurnActive {
		return s.steerManagedAFCTurnLocked(ctx, topic, receipt, text)
	}
	if topic.ActiveTurnState == model.AFCTurnStarting {
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
		return &DirectResponse{Text: "This AFC topic is starting or ownership-unknown. The new message was not queued."}, nil
	}
	if topic.ActiveTurnState == model.AFCTurnUnknown {
		if !s.usesSharedAppServer() {
			_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
			return &DirectResponse{Text: "This AFC topic is starting or ownership-unknown. The new message was not queued."}, nil
		}
		if _, reconcileErr := s.authoritativeAFCActiveTurnLocked(ctx, topic); reconcileErr != nil {
			_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
			return &DirectResponse{Text: "AFC could not reconcile restart ownership from the shared App Server; the new message was not sent: " + reconcileErr.Error()}, nil
		}
		if reconcileErr := s.store.ResolveAFCSharedDaemonUnknown(ctx, topic.SessionID, topic.TopicID, topic.ThreadID, topic.WriterGeneration); reconcileErr != nil {
			_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
			return &DirectResponse{Text: "AFC shared App Server restart reconciliation became stale; the new message was not sent."}, nil
		}
		refreshed, refreshErr := s.store.GetActiveAFCTopic(ctx, topic.ChatID, topic.TopicID)
		if refreshErr != nil || refreshed == nil {
			_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
			return &DirectResponse{Text: "AFC shared App Server restart reconciliation could not reload the topic; the new message was not sent."}, nil
		}
		topic = *refreshed
	}
	targetTurnID, targetErr := s.authoritativeAFCActiveTurnLocked(ctx, topic)
	if targetErr != nil {
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
		return &DirectResponse{Text: "AFC could not verify the current shared App Server turn; no prompt was sent: " + targetErr.Error()}, nil
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
		if isAFCNoRolloutError(err) {
			_ = s.afcWriter.Abort(lease)
			draft := model.AFCTopicDraft{SessionID: topic.SessionID, ChatID: topic.ChatID, TopicID: topic.TopicID,
				Rank: topic.Rank, Title: topic.Title, CWD: cwd}
			if thread != nil {
				draft.ProjectName, draft.DirectoryName = thread.ProjectName, thread.DirectoryName
			}
			if convertErr := s.store.ConvertEmptyAFCTopicToDraft(ctx, topic, draft, receipt, lease.Generation); convertErr == nil {
				draft.State, draft.SourceMessageID = model.AFCDraftStarting, messageID
				receipt.ThreadID = ""
				return s.startClaimedAFCDraftLocked(ctx, draft, receipt, text, text, appserver.ThreadStartOptions{}, appserver.TurnStartOptions{})
			}
			_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptRejected, model.AFCTurnTerminal, lease.Generation)
			return &DirectResponse{Text: "AFC found an empty pre-migration thread but could not convert it safely. The prompt was not sent."}, nil
		}
		_ = s.afcWriter.Abort(lease)
		_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptRejected, model.AFCTurnTerminal, lease.Generation)
		return &DirectResponse{Text: fmt.Sprintf("AFC could not resume the thread; the prompt was not sent: %v", err)}, nil
	}
	startedNewTurn := targetTurnID == ""
	var result map[string]any
	var turnErr error
	if targetTurnID != "" {
		result, turnErr = lease.Process.TurnSteer(ctx, topic.ThreadID, targetTurnID, text)
		if foundTurnID := activeTurnIDFromSteerMismatch(turnErr); foundTurnID != "" {
			targetTurnID = foundTurnID
			result, turnErr = lease.Process.TurnSteer(ctx, topic.ThreadID, targetTurnID, text)
		}
		if result == nil && steerFailureMeansNoActiveTurn(turnErr) {
			current, readErr := readAuthoritativeAFCSnapshot(ctx, lease.Process, topic.ThreadID)
			if readErr != nil {
				turnErr = readErr
			} else {
				if refreshedTurnID := activeTurnIDFromAFCSnapshot(current); refreshedTurnID != "" {
					targetTurnID = refreshedTurnID
					result, turnErr = lease.Process.TurnSteer(ctx, topic.ThreadID, targetTurnID, text)
				} else {
					startedNewTurn = true
					result, turnErr = lease.Process.TurnStart(ctx, topic.ThreadID, text, cwd, s.turnStartOptions(ctx, "", thread))
				}
			}
		}
	} else {
		result, turnErr = lease.Process.TurnStart(ctx, topic.ThreadID, text, cwd, s.turnStartOptions(ctx, "", thread))
	}
	turnID := appserverThreadTurnID(result)
	if turnID == "" && turnErr == nil && !startedNewTurn {
		turnID = targetTurnID
	}
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
	if err := s.store.MarkAFCDispatchStateWithTelegramUser(ctx, receipt, model.AFCReceiptDispatched, turnID, model.AFCTurnActive, lease.Generation, afcUserTextFingerprint(turnID, text)); err != nil {
		_ = s.afcWriter.MarkUncertain(lease)
		_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptUnknown, model.AFCTurnUnknown, lease.Generation)
		return &DirectResponse{Text: "AFC dispatched the request but could not persist confirmation; outcome is unknown."}, nil
	}
	if startedNewTurn {
		_ = s.markTelegramOriginTurnFromTelegram(ctx, topic.ThreadID, turnID, topic.ChatID, topic.TopicID)
	}
	if thread != nil {
		s.ensureStartedTurnSnapshot(ctx, thread, turnID)
	}
	s.startAFCTelegramOriginHotPoll(ctx, topic.ThreadID, turnID)
	if startedNewTurn {
		return &DirectResponse{Text: fmt.Sprintf("AFC turn started: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC input steered to active turn: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
}

func (s *Service) steerManagedAFCTurnLocked(ctx context.Context, topic model.AFCTopic, receipt model.AFCMessageReceipt, text string) (*DirectResponse, error) {
	lease, ok := s.afcLeases[topic.ThreadID]
	if !ok || lease.Generation != topic.WriterGeneration || strings.TrimSpace(topic.ActiveTurnID) == "" {
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
		return &DirectResponse{Text: "This AFC turn has no current guarded writer; the message was not sent."}, nil
	}
	turnID := topic.ActiveTurnID
	result, err := lease.Process.TurnSteer(ctx, topic.ThreadID, turnID, text)
	if foundTurnID := activeTurnIDFromSteerMismatch(err); foundTurnID != "" {
		turnID = foundTurnID
		result, err = lease.Process.TurnSteer(ctx, topic.ThreadID, turnID, text)
	}
	startedNewTurn := false
	if result == nil && steerFailureMeansNoActiveTurn(err) {
		current, readErr := readAuthoritativeAFCSnapshot(ctx, lease.Process, topic.ThreadID)
		if readErr != nil {
			err = readErr
		} else if refreshedTurnID := activeTurnIDFromAFCSnapshot(current); refreshedTurnID != "" {
			turnID = refreshedTurnID
			result, err = lease.Process.TurnSteer(ctx, topic.ThreadID, turnID, text)
		} else {
			thread, _ := s.store.GetThread(ctx, topic.ThreadID)
			cwd := ""
			if thread != nil {
				cwd = thread.CWD
			}
			startedNewTurn = true
			result, err = lease.Process.TurnStart(ctx, topic.ThreadID, text, cwd, s.turnStartOptions(ctx, "", thread))
		}
	}
	returnedTurnID := appserverThreadTurnID(result)
	if returnedTurnID != "" {
		turnID = returnedTurnID
	}
	if err != nil || result == nil {
		if startedNewTurn && afcDispatchAmbiguous(err, returnedTurnID) {
			_ = s.afcWriter.MarkUncertain(lease)
			_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptUnknown, model.AFCTurnUnknown, lease.Generation)
			return &DirectResponse{Text: "AFC dispatch outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptRejected)
		return &DirectResponse{Text: activeThreadReplyText(&model.Thread{ID: topic.ThreadID, Title: topic.Title, Status: "active", ActiveTurnID: turnID}, err)}, nil
	}
	if err := s.store.MarkAFCDispatchStateWithTelegramUser(ctx, receipt, model.AFCReceiptDispatched, turnID, model.AFCTurnActive, lease.Generation, afcUserTextFingerprint(turnID, text)); err != nil {
		_ = s.store.MarkAFCReceiptState(ctx, receipt, model.AFCReceiptUnknown)
		return &DirectResponse{Text: "AFC steered the turn but could not persist confirmation; outcome is unknown."}, nil
	}
	if startedNewTurn {
		_ = s.markTelegramOriginTurnFromTelegram(ctx, topic.ThreadID, turnID, topic.ChatID, topic.TopicID)
		s.startAFCTelegramOriginHotPoll(ctx, topic.ThreadID, turnID)
		return &DirectResponse{Text: fmt.Sprintf("AFC turn started: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC input steered to active turn: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
}

func (s *Service) authoritativeAFCActiveTurnLocked(ctx context.Context, topic model.AFCTopic) (string, error) {
	if !s.usesSharedAppServer() {
		return "", nil
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if !connected || poll == nil {
		return "", errors.New("shared App Server session is unavailable")
	}
	current, err := readAuthoritativeAFCSnapshot(ctx, poll, topic.ThreadID)
	if err != nil {
		return "", err
	}
	_ = s.store.UpsertThread(ctx, current.Thread)
	return activeTurnIDFromAFCSnapshot(current), nil
}

func readAuthoritativeAFCSnapshot(ctx context.Context, session Session, threadID string) (appserver.ThreadReadSnapshot, error) {
	payload, err := session.ThreadRead(ctx, threadID, true)
	if err != nil {
		return appserver.ThreadReadSnapshot{}, err
	}
	if payload == nil {
		return appserver.ThreadReadSnapshot{}, errors.New("App Server returned an empty thread snapshot")
	}
	current := appserver.SnapshotFromThreadRead(payload)
	if strings.TrimSpace(current.Thread.ID) != strings.TrimSpace(threadID) {
		return appserver.ThreadReadSnapshot{}, fmt.Errorf("App Server returned thread %q while %q was requested", current.Thread.ID, threadID)
	}
	return current, nil
}

func activeTurnIDFromAFCSnapshot(snapshot appserver.ThreadReadSnapshot) string {
	turnID := strings.TrimSpace(snapshot.LatestTurnID)
	if turnID == "" || isTerminalStatus(snapshot.LatestTurnStatus) || !threadLooksActiveForInput(&snapshot.Thread) {
		return ""
	}
	return turnID
}

func (s *Service) afcOwnsTelegramMutations(ctx context.Context) (bool, error) {
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return false, err
	}
	return state.State == model.AFCStateActivating || state.State == model.AFCStateActive || state.State == model.AFCStateDraining, nil
}

func afcLegacyReadOnlyCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(strings.ToLower(text)))
	if len(fields) != 1 {
		return false
	}
	command, _, _ := strings.Cut(fields[0], "@")
	return command == "/help" || command == "/status"
}

func isAFCNoRolloutError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no rollout found for thread id")
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
	limit := afcInitialTopicLimit(s.cfg.AFCInitialTopicLimit)
	if len(filtered) > limit {
		filtered = filtered[:limit]
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
			summary.Items[rank].Error = sanitizeDiagnosticString(createErr.Error())
			continue
		}
		if err := s.store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: sessionID, ChatID: s.cfg.AFCGroupID,
			TopicID: topicID, ThreadID: thread.ID, Rank: rank + 1, Title: title, TelegramState: model.AFCTopicConnected}); err != nil {
			_ = forum.DeleteAFCTopic(ctx, topicID)
			summary.Failed = append(summary.Failed, fmt.Sprintf("%s: persist: %s", thread.ID, err))
			summary.Items[rank].Telegram, summary.Items[rank].Error = "binding failed", sanitizeDiagnosticString(err.Error())
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
	topics, err := s.store.ListAFCTopics(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	drafts, err := s.store.ListAFCTopicDrafts(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	if writer.Starting+writer.Active+writer.Unknown > 0 || hasUnfinishedAFCTopics(topics) || hasUnfinishedAFCDrafts(drafts) {
		titles := unfinishedAFCWorkTitles(topics, drafts)
		return &DirectResponse{Text: "AFC off refused; unfinished topics: " + strings.Join(titles, ", ") + ". Use /sync off --force to drain."}, nil
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
	drafts, err := s.store.ListAFCTopicDrafts(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	health := strings.Join(s.healthStatusLines(ctx, time.Now().UTC()), "\n")
	return &DirectResponse{Text: fmt.Sprintf("AFC state: %s\nSession: %s\nInitial topic limit: %d\nConnected topics: %d\nReady drafts: %d\n%s\nRefresh command: /refresh\nRepair command: /repair\nNew task commands: /projects, /newchat\nActivation summary: %s",
		state.State, state.SessionID, afcInitialTopicLimit(s.cfg.AFCInitialTopicLimit), countConnectedAFCTopics(topics), countReadyAFCDrafts(drafts), health, strings.TrimSpace(state.ActivationSummaryJSON))}, nil
}

func (s *Service) syncAFC(ctx context.Context) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	_, _ = s.syncAFCLocked(ctx)
}

type afcSyncResult struct {
	Discovered           int
	DiscoveryFailures    int
	Connected            int
	SubscriptionFailures int
	ReadFailures         int
}

func (s *Service) syncAFCCommand(ctx context.Context) (*DirectResponse, error) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	result, err := s.syncAFCLocked(ctx)
	if err != nil {
		return &DirectResponse{Text: "AFC sync failed: " + sanitizeDiagnosticString(err.Error())}, nil
	}
	return &DirectResponse{Text: fmt.Sprintf("AFC sync complete. discovered: %d, discovery failures: %d, connected: %d, subscription failures: %d, read failures: %d",
		result.Discovered, result.DiscoveryFailures, result.Connected, result.SubscriptionFailures, result.ReadFailures)}, nil
}

func (s *Service) syncAFCLocked(ctx context.Context) (afcSyncResult, error) {
	var result afcSyncResult
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return result, err
	}
	if state.State != model.AFCStateActive && state.State != model.AFCStateDraining {
		return result, errors.New("AFC is not active")
	}
	forum := s.getAFCForum()
	if forum == nil {
		return result, errors.New("AFC Telegram transport is unavailable")
	}
	s.mu.RLock()
	poll, connected, pollGeneration := s.poll, s.pollConnected, s.pollGeneration
	s.mu.RUnlock()
	if !connected || poll == nil {
		return result, errors.New("App Server poll session is unavailable")
	}
	topics, err := s.store.ListAFCTopics(ctx, state.SessionID)
	if err != nil {
		return result, err
	}
	var discoveryErr error
	if state.State == model.AFCStateActive {
		discovered, failures, discoverErr := s.discoverAFCThreadsLocked(ctx, state, forum, poll, topics)
		result.Discovered = discovered
		result.DiscoveryFailures = failures
		discoveryErr = discoverErr
		if discoverErr == nil {
			topics, err = s.store.ListAFCTopics(ctx, state.SessionID)
			if err != nil {
				return result, err
			}
		}
	}
	for _, topic := range topics {
		if topic.TelegramState != model.AFCTopicConnected {
			continue
		}
		result.Connected++
		if !s.subscribeAFCThreadLocked(ctx, poll, pollGeneration, topic.ThreadID) {
			result.SubscriptionFailures++
		}
		payload, readErr := poll.ThreadRead(ctx, topic.ThreadID, true)
		if readErr != nil || payload == nil {
			result.ReadFailures++
			continue
		}
		current := appserver.SnapshotFromThreadRead(payload)
		s.processAFCSnapshotLocked(ctx, state, forum, topic, current, "afc_poll")
	}
	return result, discoveryErr
}

func (s *Service) discoverAFCThreadsLocked(ctx context.Context, state model.AFCState, forum AFCForum, poll Session, topics []model.AFCTopic) (int, int, error) {
	cutoff := parseTime(state.SnapshotAt)
	if cutoff.IsZero() {
		return 0, 0, errors.New("AFC activation cutoff is unavailable")
	}
	result, err := poll.ThreadList(ctx, observerRecentThreadLimit, "")
	if err != nil {
		return 0, 0, fmt.Errorf("AFC discovery thread/list: %w", err)
	}
	bound := make(map[string]struct{}, len(topics))
	nextRank := 0
	for _, topic := range topics {
		bound[topic.ThreadID] = struct{}{}
		if topic.Rank > nextRank {
			nextRank = topic.Rank
		}
	}
	drafts, err := s.store.ListAFCTopicDrafts(ctx, state.SessionID)
	if err != nil {
		return 0, 0, err
	}
	for _, draft := range drafts {
		if draft.Rank > nextRank {
			nextRank = draft.Rank
		}
	}
	threads := appserver.ThreadsFromList(result)
	sort.Slice(threads, func(i, j int) bool {
		if threads[i].UpdatedAt == threads[j].UpdatedAt {
			return threads[i].ID < threads[j].ID
		}
		return threads[i].UpdatedAt < threads[j].UpdatedAt
	})
	discovered, failures := 0, 0
	for _, thread := range threads {
		if thread.ID == "" || thread.Archived || thread.IsInternal() || thread.CreatedAt < cutoff.Unix() {
			continue
		}
		if _, ok := bound[thread.ID]; ok {
			continue
		}
		title := afcTopicTitle(thread)
		topicID, createErr := forum.CreateAFCTopic(ctx, title)
		if createErr != nil {
			failures++
			continue
		}
		nextRank++
		topic := model.AFCTopic{SessionID: state.SessionID, ChatID: state.ChatID, TopicID: topicID,
			ThreadID: thread.ID, Rank: nextRank, Title: title, TelegramState: model.AFCTopicConnected}
		if err := s.store.UpsertAFCTopic(ctx, topic); err != nil {
			_ = forum.DeleteAFCTopic(ctx, topicID)
			nextRank--
			failures++
			continue
		}
		bound[thread.ID] = struct{}{}
		discovered++
		_ = s.store.UpsertThread(ctx, thread)
	}
	return discovered, failures, nil
}

func (s *Service) subscribeAFCThreadLocked(ctx context.Context, poll Session, pollGeneration uint64, threadID string) bool {
	if !s.usesSharedAppServer() {
		return true
	}
	if s.afcSubscribedThreads == nil || s.afcSubscribedPollGeneration != pollGeneration {
		s.afcSubscribedThreads = map[string]struct{}{}
		s.afcSubscribedPollGeneration = pollGeneration
	}
	if _, ok := s.afcSubscribedThreads[threadID]; ok {
		return true
	}
	if _, err := poll.ThreadResume(ctx, threadID, ""); err != nil {
		return false
	}
	s.afcSubscribedThreads[threadID] = struct{}{}
	return true
}

func afcInitialTopicLimit(value int) int {
	if value <= 0 {
		return config.DefaultAFCInitialTopicLimit
	}
	return value
}

func (s *Service) startAFCTelegramOriginHotPoll(ctx context.Context, threadID, turnID string) {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return
	}
	s.mu.RLock()
	started := s.started
	s.mu.RUnlock()
	if !started {
		return
	}
	s.spawn(ctx, func(ctx context.Context) {
		boundedTurnHotPollLoop(ctx, telegramOriginHotPollMax, telegramOriginHotPollTick, func() bool {
			return s.afcTelegramOriginHotPollOnce(ctx, threadID, turnID)
		})
	})
}

func (s *Service) afcTelegramOriginHotPollOnce(ctx context.Context, threadID, turnID string) bool {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return false
	}
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	state, err := s.store.GetAFCState(ctx)
	if err != nil || (state.State != model.AFCStateActive && state.State != model.AFCStateDraining) {
		return false
	}
	topic, err := s.store.GetActiveAFCTopicByThread(ctx, state.SessionID, threadID)
	if err != nil || topic == nil || topic.ActiveTurnState != model.AFCTurnActive || strings.TrimSpace(topic.ActiveTurnID) != turnID {
		return false
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if !connected || poll == nil {
		return true
	}
	payload, readErr := poll.ThreadRead(ctx, threadID, true)
	if readErr != nil || payload == nil {
		return true
	}
	current := appserver.SnapshotFromThreadRead(payload)
	if currentTurnID := strings.TrimSpace(current.LatestTurnID); currentTurnID != "" && currentTurnID != turnID {
		return true
	}
	forum := s.getAFCForum()
	s.processAFCSnapshotLocked(ctx, state, forum, *topic, current, "afc_hot_poll")
	updated, err := s.store.GetActiveAFCTopicByThread(ctx, state.SessionID, threadID)
	return err == nil && updated != nil && updated.ActiveTurnState == model.AFCTurnActive && strings.TrimSpace(updated.ActiveTurnID) == turnID
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
	liveTool, hasLiveTool := appserver.ToolSnapshotFromLiveNotification(event, model.Thread{ID: threadID})
	payload, err := process.ThreadRead(ctx, threadID, true)
	if err != nil || payload == nil {
		return
	}
	current := appserver.SnapshotFromThreadRead(payload)
	if hasLiveTool {
		_ = mergeLiveToolSnapshot(&current, liveTool)
	}
	forum := s.getAFCForum()
	s.processAFCSnapshotLocked(ctx, state, forum, *topic, current, "afc_event")
}

func (s *Service) processAFCSnapshotLocked(ctx context.Context, state model.AFCState, forum AFCForum, topic model.AFCTopic, current appserver.ThreadReadSnapshot, operation string) {
	activeTurnID := strings.TrimSpace(topic.ActiveTurnID)
	currentTurnID := strings.TrimSpace(current.LatestTurnID)
	if topic.ActiveTurnState == model.AFCTurnActive && activeTurnID != "" && currentTurnID != "" && currentTurnID != activeTurnID {
		if !s.usesSharedAppServer() ||
			!s.releaseAFCTurnLeaseLocked(ctx, state, topic, activeTurnID) {
			return
		}
		topic.ActiveTurnState = model.AFCTurnTerminal
	}
	if topic.ActiveTurnState == model.AFCTurnActive &&
		activeTurnID != "" &&
		currentTurnID == activeTurnID {
		previous, _ := s.store.GetSnapshot(ctx, topic.ThreadID)
		if s.applyTelegramOriginTerminalGate(ctx, operation, &current, previous) {
			return
		}
		s.preserveTelegramOriginLiveCurrentTool(ctx, &current, previous)
	}
	s.queueExternalReplyFromSnapshot(ctx, current)
	if forum != nil {
		s.persistAndDeliverAFCSnapshotLocked(ctx, forum, topic, current)
	}
	s.completeAFCTurnLocked(ctx, state, topic, current)
}

func (s *Service) completeAFCTurnLocked(ctx context.Context, state model.AFCState, topic model.AFCTopic, current appserver.ThreadReadSnapshot) {
	if !isTerminalStatus(current.LatestTurnStatus) || strings.TrimSpace(current.LatestTurnID) == "" || current.LatestTurnID != topic.ActiveTurnID {
		return
	}
	_ = s.releaseAFCTurnLeaseLocked(ctx, state, topic, current.LatestTurnID)
}

func (s *Service) releaseAFCTurnLeaseLocked(ctx context.Context, state model.AFCState, topic model.AFCTopic, turnID string) bool {
	lease, ok := s.afcLeases[topic.ThreadID]
	if !ok || lease.Generation != topic.WriterGeneration {
		return false
	}
	if err := s.store.MarkAFCTerminal(ctx, state.SessionID, topic.ThreadID, turnID, topic.WriterGeneration); err != nil {
		return false
	}
	_ = s.store.ExpireAFCCallbackRoutes(ctx, topic.ThreadID, turnID)
	delete(s.afcLeases, topic.ThreadID)
	if err := s.afcWriter.MarkTerminal(lease); err != nil {
		return false
	}
	if s.afcWriter.Snapshot().State == appserver.WriterStopped && s.afcEventCancel != nil {
		s.afcEventCancel()
		s.afcEventCancel, s.afcEventProcess, s.afcEventGeneration = nil, nil, 0
	}
	return true
}

func (s *Service) persistAndDeliverAFCSnapshotLocked(ctx context.Context, forum AFCForum, topic model.AFCTopic, current appserver.ThreadReadSnapshot) {
	previous, _ := s.store.GetSnapshot(ctx, topic.ThreadID)
	observedAt := s.now().UTC()
	compact := appserver.CompactSnapshot(previous, current, observedAt)
	observed := current
	_ = json.Unmarshal(compact.CompactJSON, &observed)
	_ = s.store.UpsertThread(ctx, current.Thread)
	_ = s.store.UpsertSnapshot(ctx, topic.ThreadID, compact)
	currentTurnID := strings.TrimSpace(observed.LatestTurnID)
	desiredTitle := strings.TrimSpace(current.Thread.Title)
	if desiredTitle != "" && desiredTitle != current.Thread.ID {
		desiredTitle = afcTopicTitle(current.Thread)
		if desiredTitle != topic.Title && forum.RenameAFCTopic(ctx, topic.TopicID, desiredTitle) == nil {
			if currentTurnID != "" &&
				topic.StatusMessageID != 0 &&
				strings.TrimSpace(topic.StatusTurnID) == currentTurnID &&
				!isTerminalStatus(observed.LatestTurnStatus) {
				oldStatusID := topic.StatusMessageID
				reset, err := s.store.ResetAFCTopicStatusDelivery(ctx, topic.SessionID, topic.TopicID, oldStatusID, currentTurnID)
				if err != nil || !reset {
					return
				}
				_ = forum.DeleteAFCMessage(ctx, topic.TopicID, oldStatusID)
				topic.StatusMessageID = 0
				topic.StatusTurnID = ""
				topic.LastRenderFP = ""
			}
			_ = s.store.UpdateAFCTopicTitle(ctx, topic.SessionID, topic.TopicID, desiredTitle)
		}
	}
	var userDeliveryOK bool
	topic, userDeliveryOK = s.deliverAFCUserMessageLocked(ctx, forum, topic, observed)
	if !userDeliveryOK {
		return
	}
	statusMessage := renderAFCStatusAt(observed, observedAt)
	renderFP := afcFingerprint(tgformat.HashRendered(statusMessage))
	statusID := topic.StatusMessageID
	statusTurnID := strings.TrimSpace(topic.StatusTurnID)
	newObservedTurn := statusTurnID != "" && currentTurnID != "" && statusTurnID != currentTurnID
	var deliveryErr error
	if statusID == 0 || newObservedTurn {
		statusID, deliveryErr = forum.SendAFCMessage(ctx, topic.TopicID, statusMessage, true)
	} else if renderFP != topic.LastRenderFP {
		deliveryErr = forum.EditAFCMessage(ctx, topic.TopicID, statusID, statusMessage)
	}
	if deliveryErr != nil {
		return
	}
	if currentTurnID != "" {
		statusTurnID = currentTurnID
	}
	finalFP := topic.LastFinalFP
	if strings.TrimSpace(current.LatestFinalFP) != "" && current.LatestFinalFP != topic.LastFinalFP {
		finalMessages := renderAFCFinal(current.LatestFinalText)
		for index, message := range finalMessages {
			if _, deliveryErr = forum.SendAFCMessage(ctx, topic.TopicID, message, false); deliveryErr != nil {
				logKey := strings.Join([]string{"afc_final_delivery_failed", topic.ThreadID, current.LatestTurnID, current.LatestFinalFP}, ":")
				if s.allowDiagnosticRepeat(logKey, diagnosticRepeatWindow) {
					s.logLifecycle("afc_final_delivery_failed", lifecycleFields{
						"thread_id":   topic.ThreadID,
						"turn_id":     current.LatestTurnID,
						"topic_id":    topic.TopicID,
						"chunk_index": index + 1,
						"chunk_count": len(finalMessages),
						"error":       deliveryErr,
					})
				}
				break
			}
		}
		if deliveryErr == nil {
			finalFP = current.LatestFinalFP
		}
	}
	_ = s.store.UpdateAFCTopicDelivery(ctx, topic.SessionID, topic.TopicID, statusID, statusTurnID, renderFP, finalFP)
}

func renderAFCFinal(finalText string) []model.RenderedMessage {
	body := strings.TrimSpace(finalText)
	if body == "" {
		return []model.RenderedMessage{{Text: afcFinalHeader}}
	}
	prefix := afcFinalHeader + "\n"
	prefixUnits := afcUTF16Len(prefix)
	chunks := tgformat.RenderSegments(
		[]tgformat.Segment{tgformat.Plain(body)},
		tgformat.TelegramMessageLimit-prefixUnits,
	)
	for index := range chunks {
		chunks[index].Text = prefix + chunks[index].Text
		for entityIndex := range chunks[index].Entities {
			chunks[index].Entities[entityIndex].Offset += prefixUnits
		}
	}
	return chunks
}

func (s *Service) deliverAFCUserMessageLocked(ctx context.Context, forum AFCForum, topic model.AFCTopic, current appserver.ThreadReadSnapshot) (model.AFCTopic, bool) {
	userFP := strings.TrimSpace(current.LatestUserMessageFP)
	userText := strings.TrimSpace(current.LatestUserMessageText)
	if userFP == "" || userText == "" || userFP == strings.TrimSpace(topic.LastUserFP) {
		return topic, true
	}
	turnID := strings.TrimSpace(current.LatestTurnID)
	pendingFP := strings.TrimSpace(topic.PendingTelegramUserFP)
	pendingTurnID := strings.TrimSpace(topic.PendingTelegramTurnID)
	if pendingFP != "" {
		if pendingTurnID == turnID {
			if afcUserTextFingerprint(turnID, userText) != pendingFP {
				return topic, false
			}
			if err := s.store.UpdateAFCTopicUserDelivery(ctx, topic.SessionID, topic.TopicID, userFP, "", ""); err != nil {
				return topic, false
			}
			topic.LastUserFP = userFP
			topic.PendingTelegramTurnID = ""
			topic.PendingTelegramUserFP = ""
			return topic, true
		}
		if err := s.store.UpdateAFCTopicUserDelivery(ctx, topic.SessionID, topic.TopicID, topic.LastUserFP, "", ""); err != nil {
			return topic, false
		}
		topic.PendingTelegramTurnID = ""
		topic.PendingTelegramUserFP = ""
	}
	if topic.StatusMessageID != 0 && strings.TrimSpace(topic.StatusTurnID) == turnID {
		oldStatusID := topic.StatusMessageID
		reset, err := s.store.ResetAFCTopicStatusDelivery(ctx, topic.SessionID, topic.TopicID, oldStatusID, turnID)
		if err != nil || !reset {
			return topic, false
		}
		_ = forum.DeleteAFCMessage(ctx, topic.TopicID, oldStatusID)
		topic.StatusMessageID = 0
		topic.StatusTurnID = ""
		topic.LastRenderFP = ""
	}
	if _, err := forum.SendAFCMessage(ctx, topic.TopicID, model.RenderedMessage{Text: afcUserHeader + "\n" + userText}, true); err != nil {
		return topic, false
	}
	if err := s.store.UpdateAFCTopicUserDelivery(ctx, topic.SessionID, topic.TopicID, userFP, "", ""); err != nil {
		return topic, false
	}
	topic.LastUserFP = userFP
	return topic, true
}

func (s *Service) reanchorAFCDirectDelivery(ctx context.Context, chatID, topicID int64, response *DirectResponse) {
	if response == nil || !s.isAFCGroup(chatID) || isAFCControlTopic(topicID) {
		return
	}
	threadID := strings.TrimSpace(response.ThreadID)
	turnID := strings.TrimSpace(response.TurnID)
	if threadID == "" || turnID == "" {
		return
	}
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	state, err := s.store.GetAFCState(ctx)
	if err != nil || (state.State != model.AFCStateActive && state.State != model.AFCStateDraining) {
		return
	}
	topic, err := s.store.GetActiveAFCTopic(ctx, chatID, topicID)
	if err != nil || topic == nil || strings.TrimSpace(topic.ThreadID) != threadID {
		return
	}
	forum := s.getAFCForum()
	if forum == nil {
		return
	}
	if topic.StatusMessageID != 0 && strings.TrimSpace(topic.StatusTurnID) == turnID {
		oldStatusID := topic.StatusMessageID
		reset, resetErr := s.store.ResetAFCTopicStatusDelivery(ctx, topic.SessionID, topic.TopicID, oldStatusID, turnID)
		if resetErr != nil || !reset {
			return
		}
		_ = forum.DeleteAFCMessage(ctx, topic.TopicID, oldStatusID)
		topic, err = s.store.GetActiveAFCTopic(ctx, chatID, topicID)
		if err != nil || topic == nil {
			return
		}
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if !connected || poll == nil {
		return
	}
	payload, err := poll.ThreadRead(ctx, threadID, true)
	if err != nil || payload == nil {
		return
	}
	current := appserver.SnapshotFromThreadRead(payload)
	if strings.TrimSpace(current.LatestTurnID) != turnID {
		return
	}
	s.processAFCSnapshotLocked(ctx, state, forum, *topic, current, "afc_direct_delivery")
}

func (s *Service) cleanupAFCTopics(ctx context.Context, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	forum := s.getAFCForum()
	if forum == nil {
		return
	}
	topics, _ := s.store.ListAFCCleanupTargets(ctx, sessionID)
	for _, topic := range topics {
		if isAFCControlTopic(topic.TopicID) {
			continue
		}
		if topic.TelegramState == model.AFCTopicCleanup && forum.DeleteAFCTopic(ctx, topic.TopicID) == nil {
			_ = s.store.DeleteAFCTopic(ctx, sessionID, topic.TopicID)
		}
	}
}

// FinishStartup runs after the Telegram bot is ready. It cleans the previous
// AFC session and reports a missing shared App Server once per process start.
func (s *Service) FinishStartup(ctx context.Context) {
	s.mu.Lock()
	if s.startupFinished {
		s.mu.Unlock()
		return
	}
	s.startupFinished = true
	cleanupSessionID := s.startupCleanupSessionID
	s.startupCleanupSessionID = ""
	done := s.startupDone
	s.mu.Unlock()
	s.spawn(ctx, func(ctx context.Context) {
		defer close(done)
		s.finishStartup(ctx, cleanupSessionID)
	})
}

func (s *Service) finishStartup(ctx context.Context, cleanupSessionID string) {
	s.cleanupAFCTopics(ctx, cleanupSessionID)
	s.ensurePollSession(ctx)

	s.mu.RLock()
	connected := s.pollConnected
	forum := s.afcForum
	s.mu.RUnlock()
	if connected || forum == nil || s.cfg.AFCGroupID == 0 || !s.usesSharedAppServer() {
		return
	}

	message := model.RenderedMessage{Text: s.sharedAppServerStartupWarning()}
	if _, err := forum.SendAFCMessage(ctx, afcGeneralSendTopicID, message, false); err != nil {
		s.logLifecycle("afc_startup_warning_failed", lifecycleFields{"error": err})
		return
	}
	s.logLifecycle("afc_startup_warning_sent", nil)
}

func (s *Service) sharedAppServerStartupWarning() string {
	fix := "1. Start the managed daemon: codex app-server daemon start\n" +
		"2. Restart Codex Desktop in local-daemon mode.\n" +
		"3. Restart codex-tg, then run /sync on in Control."
	if appserver.TransportMode(strings.ToLower(strings.TrimSpace(s.cfg.AppServerMode))) == appserver.TransportWebSocket {
		endpoint := strings.TrimSpace(s.cfg.AppServerListen)
		fix = "1. Restore the shared App Server at " + endpoint + ".\n" +
			"2. Ensure Codex Desktop is connected to the same endpoint.\n" +
			"3. Restart codex-tg, then run /sync on in Control."
	}
	return "⚠️ Shared Codex App Server is unavailable.\n\n" +
		"AFC was reset to off.\n\n" +
		"Fix:\n" + fix
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

func renderAFCStatusAt(snapshot appserver.ThreadReadSnapshot, now time.Time) model.RenderedMessage {
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
	header := afcStatusHeader + " " + status
	if duration, _ := runTimingValue(&snapshot, now); duration != "" {
		header += " · " + duration
	}

	blocks := afcStatusBlocks(snapshot.DetailItems)
	if len(blocks) == 0 {
		return model.RenderedMessage{Text: header}
	}
	body := renderAFCStatusBlocks(snapshot, blocks, now)
	body = trimAFCStatusBody(header, body)
	message := model.RenderedMessage{Text: header + "\n" + body}
	if isTerminalStatus(snapshot.LatestTurnStatus) && body != "" {
		message.Entities = []model.MessageEntity{{
			Type:   "expandable_blockquote",
			Offset: afcUTF16Len(header + "\n"),
			Length: afcUTF16Len(body),
		}}
	}
	return message
}

func afcStatusBlocks(items []model.DetailItem) []model.DetailItem {
	blocks := make([]model.DetailItem, 0, len(items))
	for _, item := range items {
		if (item.Kind == model.DetailItemCommentary || item.Kind == model.DetailItemPlan) && strings.TrimSpace(item.Text) != "" {
			blocks = append(blocks, item)
		}
	}
	return blocks
}

func renderAFCStatusBlocks(snapshot appserver.ThreadReadSnapshot, blocks []model.DetailItem, now time.Time) string {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	turnStart := parseTime(model.TimeString(snapshot.LatestTurnStartedAt))
	if turnStart.IsZero() {
		turnStart = now
	}
	turnEnd := now
	if isTerminalStatus(snapshot.LatestTurnStatus) {
		if endedAt := parseTime(model.TimeString(snapshot.LatestTurnUpdatedAt)); !endedAt.IsZero() {
			turnEnd = endedAt
		}
	}
	if turnEnd.Before(turnStart) {
		turnEnd = turnStart
	}
	span := turnEnd.Sub(turnStart)
	starts := make([]time.Time, len(blocks))
	for index, block := range blocks {
		startedAt := parseTime(block.StartedAt)
		if startedAt.IsZero() {
			startedAt = turnStart.Add(time.Duration(int64(span) * int64(index) / int64(len(blocks))))
		}
		if startedAt.Before(turnStart) {
			startedAt = turnStart
		}
		if index > 0 && startedAt.Before(starts[index-1]) {
			startedAt = starts[index-1]
		}
		if startedAt.After(turnEnd) {
			startedAt = turnEnd
		}
		starts[index] = startedAt
	}

	parts := make([]string, 0, len(blocks))
	for index, block := range blocks {
		endedAt := turnEnd
		if index+1 < len(starts) {
			endedAt = starts[index+1]
		}
		if endedAt.Before(starts[index]) {
			endedAt = starts[index]
		}
		parts = append(parts, fmt.Sprintf("Блок %d · %s\n%s", index+1, formatToolDuration(endedAt.Sub(starts[index])), strings.TrimSpace(block.Text)))
	}
	return strings.Join(parts, "\n\n")
}

func trimAFCStatusBody(header, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	budget := tgformat.TelegramMessageLimit - afcUTF16Len(header+"\n")
	if budget <= 0 {
		return ""
	}
	if afcUTF16Len(body) <= budget {
		return body
	}
	lines := strings.Split(body, "\n")
	for removed := 1; removed < len(lines); removed++ {
		candidate := fmt.Sprintf("… удалено строк: %d\n%s", removed, strings.Join(lines[removed:], "\n"))
		if afcUTF16Len(candidate) <= budget {
			return candidate
		}
	}
	marker := fmt.Sprintf("… удалено строк: %d", len(lines))
	remaining := budget - afcUTF16Len(marker+"\n")
	if remaining <= 0 {
		return afcUTF16Suffix(marker, budget)
	}
	tail := afcUTF16Suffix(lines[len(lines)-1], remaining)
	if tail == "" {
		return marker
	}
	return marker + "\n" + tail
}

func afcUTF16Len(text string) int {
	return len(utf16.Encode([]rune(text)))
}

func afcUTF16Suffix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(text)
	units := 0
	start := len(runes)
	for start > 0 {
		width := 1
		if runes[start-1] > 0xffff {
			width = 2
		}
		if units+width > limit {
			break
		}
		units += width
		start--
	}
	return string(runes[start:])
}

func afcFingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:12])
}

func afcUserTextFingerprint(turnID, text string) string {
	return afcFingerprint(strings.TrimSpace(turnID) + "\x00" + strings.TrimSpace(text))
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

func countReadyAFCDrafts(drafts []model.AFCTopicDraft) int {
	n := 0
	for _, draft := range drafts {
		if draft.State == model.AFCDraftReady {
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
