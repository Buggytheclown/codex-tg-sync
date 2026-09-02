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
	syncControlTopicID         = int64(1)
	syncGeneralSendTopicID     = int64(0)
	syncActivationDeliveryKind = "sync_activation"
)

// SyncForum is deliberately scoped to the configured Sync group. Its
// implementation does not accept a chat id, so every operation stays inside
// that group.
type SyncForum interface {
	ValidateSyncGroup(ctx context.Context, allowedUserID int64) error
	PrepareSyncControl(ctx context.Context) error
	CreateSyncTopic(ctx context.Context, title string) (int64, error)
	CreateLaunchTopic(ctx context.Context, title string) (int64, error)
	RenameSyncTopic(ctx context.Context, topicID int64, title string) error
	DeleteSyncTopic(ctx context.Context, topicID int64) error
	DeleteSyncMessage(ctx context.Context, topicID, messageID int64) error
	SendSyncMessage(ctx context.Context, topicID int64, message model.RenderedMessage, options model.SendOptions) (int64, error)
	SendSyncActionMessage(ctx context.Context, topicID int64, text string, buttons [][]model.ButtonSpec) (int64, error)
	EditSyncMessage(ctx context.Context, topicID, messageID int64, message model.RenderedMessage, options model.SendOptions) error
}

const (
	syncUserHeader      = "👤 [User]"
	syncStatusHeader    = "⏱ [Status]"
	syncFinalHeader     = "✅ [Final]"
	syncApprovalHeader  = "🔐 [Approval]"
	syncInputHeader     = "❓ [Input]"
	syncStatusTimerTick = 10 * time.Second
)

type SyncForumFailureKind string

const (
	SyncForumFailureDefinitive SyncForumFailureKind = "definitive"
	SyncForumFailureRetryable  SyncForumFailureKind = "retryable"
	SyncForumFailureUnknown    SyncForumFailureKind = "unknown"
)

type SyncForumFailure struct {
	Kind SyncForumFailureKind
	Err  error
}

func (e *SyncForumFailure) Error() string { return e.Err.Error() }
func (e *SyncForumFailure) Unwrap() error { return e.Err }

func NewSyncForumFailure(kind SyncForumFailureKind, err error) error {
	if err == nil {
		return nil
	}
	return &SyncForumFailure{Kind: kind, Err: err}
}

type syncActivationSummary struct {
	SnapshotAt string               `json:"snapshot_at"`
	Selected   int                  `json:"selected"`
	Created    int                  `json:"created"`
	Failed     []string             `json:"failed,omitempty"`
	Unknown    []string             `json:"unknown,omitempty"`
	Skipped    []string             `json:"skipped,omitempty"`
	Items      []syncActivationItem `json:"items"`
}

type syncActivationItem struct {
	Rank     int    `json:"rank"`
	ThreadID string `json:"thread_id"`
	Title    string `json:"title"`
	Telegram string `json:"telegram"`
	Codex    string `json:"codex"`
	Error    string `json:"error,omitempty"`
}

func (s *Service) isSyncGroup(chatID int64) bool {
	return s.cfg.SyncGroupID != 0 && chatID == s.cfg.SyncGroupID
}

func isSyncControlTopic(topicID int64) bool { return topicID == 0 || topicID == syncControlTopicID }

func (s *Service) handleSyncMessage(ctx context.Context, topicID, messageID, userID int64, text string) (*DirectResponse, error) {
	text = strings.TrimSpace(text)
	if isSyncControlTopic(topicID) {
		fields := strings.Fields(strings.ToLower(text))
		if len(fields) > 0 {
			fields[0], _, _ = strings.Cut(fields[0], "@")
		}
		switch {
		case len(fields) == 1 && fields[0] == "/sync":
			state, err := s.store.GetSyncState(ctx)
			if err != nil {
				return nil, err
			}
			if state.State == model.SyncStateActive || state.State == model.SyncStateDraining {
				return s.deactivateSync(ctx)
			}
			return s.activateSync(ctx, userID)
		case len(fields) == 2 && fields[0] == "/sync" && fields[1] == "on":
			return s.activateSync(ctx, userID)
		case len(fields) == 3 && fields[0] == "/sync" && fields[1] == "off" && fields[2] == "--force":
			return s.forceDeactivateSync(ctx)
		case len(fields) == 2 && fields[0] == "/sync" && fields[1] == "off":
			return s.deactivateSync(ctx)
		case len(fields) == 1 && fields[0] == "/status":
			return s.syncStatus(ctx)
		case len(fields) == 1 && fields[0] == "/pollers":
			text, err := s.PollersSnapshot(ctx)
			if err != nil {
				return nil, err
			}
			return &DirectResponse{Text: text}, nil
		case len(fields) >= 1 && len(fields) <= 2 && fields[0] == "/requests":
			filter := ""
			if len(fields) == 2 {
				filter = fields[1]
			}
			return s.externalRequestsOverview(ctx, filter)
		case len(fields) == 1 && fields[0] == "/refresh":
			return s.reconcileSyncCommand(ctx)
		case len(fields) == 1 && fields[0] == "/repair":
			if err := s.RequestRepair(ctx, "telegram"); err != nil {
				return nil, err
			}
			return &DirectResponse{Text: "Repair requested. App-server sessions will be recreated in the background."}, nil
		case len(fields) == 1 && (fields[0] == "/projects" || fields[0] == "/newchat"):
			return s.syncProjectsMenu(ctx, topicID)
		default:
			return &DirectResponse{Text: "Sync Control accepts /sync on, /sync off, /status, /pollers, /requests, /refresh, /repair, /projects, and /newchat. Unsupported commands are disabled in this group."}, nil
		}
	}
	topic, err := s.store.GetActiveSyncTopic(ctx, s.cfg.SyncGroupID, topicID)
	if err != nil {
		return nil, err
	}
	if topic == nil {
		draft, draftErr := s.store.GetActiveSyncTopicDraft(ctx, s.cfg.SyncGroupID, topicID)
		if draftErr != nil {
			return nil, draftErr
		}
		if draft != nil {
			return s.handleSyncDraftMessage(ctx, *draft, messageID, text)
		}
		return &DirectResponse{Text: "Sync topic is stale or unknown. Use /status in Control."}, nil
	}
	if text == "" {
		return &DirectResponse{Text: "Sync topic requires a non-empty plain-text prompt."}, nil
	}
	if strings.EqualFold(text, "/stop") || strings.HasPrefix(strings.ToLower(text), "/stop@") {
		return s.stopSyncTurn(ctx, topicID)
	}
	if strings.HasPrefix(text, "/") {
		return &DirectResponse{Text: "Sync topic accepts a plain-text prompt. Topic commands arrive with the control rollout."}, nil
	}
	return s.dispatchSyncMessage(ctx, *topic, messageID, text)
}

func (s *Service) dispatchSyncMessage(ctx context.Context, topic model.SyncTopic, messageID int64, text string) (*DirectResponse, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	receipt, created, err := s.store.AcceptSyncMessage(ctx, topic.ChatID, topic.TopicID, messageID)
	if err != nil {
		return nil, err
	}
	if !created {
		return syncDuplicateReceiptResponse(receipt), nil
	}
	if topic.ActiveTurnState == model.SyncTurnActive {
		return s.steerManagedSyncTurnLocked(ctx, topic, receipt, text)
	}
	if topic.ActiveTurnState == model.SyncTurnStarting {
		_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
		return &DirectResponse{Text: "This Sync topic is starting or ownership-unknown. The new message was not queued."}, nil
	}
	if topic.ActiveTurnState == model.SyncTurnUnknown {
		if !s.usesSharedAppServer() {
			_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
			return &DirectResponse{Text: "This Sync topic is starting or ownership-unknown. The new message was not queued."}, nil
		}
		if _, reconcileErr := s.authoritativeSyncActiveTurnLocked(ctx, topic); reconcileErr != nil {
			_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
			return &DirectResponse{Text: "Sync could not reconcile restart ownership from the shared App Server; the new message was not sent: " + reconcileErr.Error()}, nil
		}
		if reconcileErr := s.store.ResolveSyncSharedDaemonUnknown(ctx, topic.SessionID, topic.TopicID, topic.ThreadID, topic.WriterGeneration); reconcileErr != nil {
			_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
			return &DirectResponse{Text: "Sync shared App Server restart reconciliation became stale; the new message was not sent."}, nil
		}
		refreshed, refreshErr := s.store.GetActiveSyncTopic(ctx, topic.ChatID, topic.TopicID)
		if refreshErr != nil || refreshed == nil {
			_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
			return &DirectResponse{Text: "Sync shared App Server restart reconciliation could not reload the topic; the new message was not sent."}, nil
		}
		topic = *refreshed
	}
	targetTurnID, targetErr := s.authoritativeSyncActiveTurnLocked(ctx, topic)
	if targetErr != nil {
		_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
		return &DirectResponse{Text: "Sync could not verify the current shared App Server turn; no prompt was sent: " + targetErr.Error()}, nil
	}
	lease, err := s.syncWriter.Reserve(ctx, topic.ThreadID)
	if err != nil {
		_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
		var claimErr *appserver.ThreadClaimError
		if errors.As(err, &claimErr) {
			return &DirectResponse{Text: fmt.Sprintf("This thread is owned by %s writer generation %d; Sync did not mutate App Server.", claimErr.Current.Writer, claimErr.Current.Generation)}, nil
		}
		return &DirectResponse{Text: fmt.Sprintf("Sync could not reserve this thread: %v", err)}, nil
	}
	if err := s.store.MarkSyncStarting(ctx, receipt, lease.Generation); err != nil {
		_ = s.syncWriter.Abort(lease)
		_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
		return &DirectResponse{Text: "This Sync topic already has unfinished work. The message was not dispatched."}, nil
	}
	s.syncLeases[topic.ThreadID] = lease
	s.installSyncWriterLocked(lease)
	thread, _ := s.store.GetThread(ctx, topic.ThreadID)
	cwd := ""
	if thread != nil {
		cwd = thread.CWD
	}
	if _, err := lease.Process.ThreadResume(ctx, topic.ThreadID, cwd); err != nil {
		delete(s.syncLeases, topic.ThreadID)
		if isSyncNoRolloutError(err) {
			_ = s.syncWriter.Abort(lease)
			draft := model.SyncTopicDraft{SessionID: topic.SessionID, ChatID: topic.ChatID, TopicID: topic.TopicID,
				Rank: topic.Rank, Title: topic.Title, CWD: cwd}
			if thread != nil {
				draft.ProjectName, draft.DirectoryName = thread.ProjectName, thread.DirectoryName
			}
			if convertErr := s.store.ConvertEmptySyncTopicToDraft(ctx, topic, draft, receipt, lease.Generation); convertErr == nil {
				draft.State, draft.SourceMessageID = model.SyncDraftStarting, messageID
				receipt.ThreadID = ""
				return s.startClaimedSyncDraftLocked(ctx, draft, receipt, text, text, telegramThreadStartOptions(), appserver.TurnStartOptions{})
			}
			_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptRejected, model.SyncTurnTerminal, lease.Generation)
			return &DirectResponse{Text: "Sync found an empty pre-migration thread but could not convert it safely. The prompt was not sent."}, nil
		}
		_ = s.syncWriter.Abort(lease)
		_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptRejected, model.SyncTurnTerminal, lease.Generation)
		return &DirectResponse{Text: fmt.Sprintf("Sync could not resume the thread; the prompt was not sent: %v", err)}, nil
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
			current, readErr := readAuthoritativeSyncSnapshot(ctx, lease.Process, topic.ThreadID)
			if readErr != nil {
				turnErr = readErr
			} else {
				if refreshedTurnID := activeTurnIDFromSyncSnapshot(current); refreshedTurnID != "" {
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
		if syncDispatchAmbiguous(turnErr, turnID) {
			_ = s.syncWriter.MarkUnknown(lease)
			_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation)
			return &DirectResponse{Text: "Sync dispatch outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		delete(s.syncLeases, topic.ThreadID)
		_ = s.syncWriter.Abort(lease)
		_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptRejected, model.SyncTurnTerminal, lease.Generation)
		return &DirectResponse{Text: fmt.Sprintf("Sync rejected the prompt before dispatch: %v", turnErr)}, nil
	}
	if err := s.syncWriter.MarkActive(lease); err != nil {
		_ = s.syncWriter.MarkUncertain(lease)
		_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation)
		return &DirectResponse{Text: "Sync dispatched the request but could not confirm local ownership; outcome is unknown."}, nil
	}
	if err := s.store.MarkSyncDispatchStateWithTelegramUser(ctx, receipt, model.SyncReceiptDispatched, turnID, model.SyncTurnActive, lease.Generation, syncUserTextFingerprint(turnID, text)); err != nil {
		_ = s.syncWriter.MarkUncertain(lease)
		_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation)
		return &DirectResponse{Text: "Sync dispatched the request but could not persist confirmation; outcome is unknown."}, nil
	}
	if startedNewTurn {
		_ = s.markTelegramOriginTurnFromTelegram(ctx, topic.ThreadID, turnID, topic.ChatID, topic.TopicID)
	}
	if thread != nil {
		s.ensureStartedTurnSnapshot(ctx, thread, turnID)
	}
	s.startSyncTelegramOriginHotPoll(ctx, topic.ThreadID, turnID)
	if startedNewTurn {
		return &DirectResponse{Text: fmt.Sprintf("Sync turn started: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
	}
	return &DirectResponse{Text: fmt.Sprintf("Sync input steered to active turn: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
}

func (s *Service) steerManagedSyncTurnLocked(ctx context.Context, topic model.SyncTopic, receipt model.SyncMessageReceipt, text string) (*DirectResponse, error) {
	lease, ok := s.syncLeases[topic.ThreadID]
	if !ok || lease.Generation != topic.WriterGeneration || strings.TrimSpace(topic.ActiveTurnID) == "" {
		_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
		return &DirectResponse{Text: "This Sync turn has no current guarded writer; the message was not sent."}, nil
	}
	turnID := topic.ActiveTurnID
	result, err := lease.Process.TurnSteer(ctx, topic.ThreadID, turnID, text)
	if foundTurnID := activeTurnIDFromSteerMismatch(err); foundTurnID != "" {
		turnID = foundTurnID
		result, err = lease.Process.TurnSteer(ctx, topic.ThreadID, turnID, text)
	}
	startedNewTurn := false
	if result == nil && steerFailureMeansNoActiveTurn(err) {
		current, readErr := readAuthoritativeSyncSnapshot(ctx, lease.Process, topic.ThreadID)
		if readErr != nil {
			err = readErr
		} else if refreshedTurnID := activeTurnIDFromSyncSnapshot(current); refreshedTurnID != "" {
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
		if startedNewTurn && syncDispatchAmbiguous(err, returnedTurnID) {
			_ = s.syncWriter.MarkUncertain(lease)
			_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation)
			return &DirectResponse{Text: "Sync dispatch outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptRejected)
		return &DirectResponse{Text: activeThreadReplyText(&model.Thread{ID: topic.ThreadID, Title: topic.Title, Status: "active", ActiveTurnID: turnID}, err)}, nil
	}
	if err := s.store.MarkSyncDispatchStateWithTelegramUser(ctx, receipt, model.SyncReceiptDispatched, turnID, model.SyncTurnActive, lease.Generation, syncUserTextFingerprint(turnID, text)); err != nil {
		_ = s.store.MarkSyncReceiptState(ctx, receipt, model.SyncReceiptUnknown)
		return &DirectResponse{Text: "Sync steered the turn but could not persist confirmation; outcome is unknown."}, nil
	}
	if startedNewTurn {
		_ = s.markTelegramOriginTurnFromTelegram(ctx, topic.ThreadID, turnID, topic.ChatID, topic.TopicID)
		s.startSyncTelegramOriginHotPoll(ctx, topic.ThreadID, turnID)
		return &DirectResponse{Text: fmt.Sprintf("Sync turn started: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
	}
	return &DirectResponse{Text: fmt.Sprintf("Sync input steered to active turn: %s", turnID), ThreadID: topic.ThreadID, TurnID: turnID}, nil
}

func (s *Service) authoritativeSyncActiveTurnLocked(ctx context.Context, topic model.SyncTopic) (string, error) {
	if !s.usesSharedAppServer() {
		return "", nil
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if !connected || poll == nil {
		return "", errors.New("shared App Server session is unavailable")
	}
	current, err := readAuthoritativeSyncSnapshot(ctx, poll, topic.ThreadID)
	if err != nil {
		return "", err
	}
	_ = s.store.UpsertThread(ctx, current.Thread)
	return activeTurnIDFromSyncSnapshot(current), nil
}

func readAuthoritativeSyncSnapshot(ctx context.Context, session Session, threadID string) (appserver.ThreadReadSnapshot, error) {
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

func activeTurnIDFromSyncSnapshot(snapshot appserver.ThreadReadSnapshot) string {
	turnID := strings.TrimSpace(snapshot.LatestTurnID)
	if turnID == "" || isTerminalStatus(snapshot.LatestTurnStatus) || !threadLooksActiveForInput(&snapshot.Thread) {
		return ""
	}
	return turnID
}

func (s *Service) syncOwnsTelegramMutations(ctx context.Context) (bool, error) {
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return false, err
	}
	return state.State == model.SyncStateActivating || state.State == model.SyncStateActive || state.State == model.SyncStateDraining, nil
}

func syncLegacyReadOnlyCommand(text string) bool {
	fields := strings.Fields(strings.TrimSpace(strings.ToLower(text)))
	if len(fields) == 0 || len(fields) > 2 {
		return false
	}
	command, _, _ := strings.Cut(fields[0], "@")
	if command == "/requests" {
		return true
	}
	return len(fields) == 1 && (command == "/help" || command == "/status" || command == "/pollers")
}

func isSyncNoRolloutError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no rollout found for thread id")
}

func (s *Service) activateSync(ctx context.Context, userID int64) (*DirectResponse, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.cfg.SyncGroupID == 0 {
		return nil, errors.New("CTR_GO_SYNC_GROUP_ID is not configured")
	}
	if len(s.cfg.AllowedUserIDs) != 1 || s.cfg.AllowedUserIDs[0] != userID {
		return nil, errors.New("Sync requires exactly one configured allowed user")
	}
	forum := s.getSyncForum()
	if forum == nil {
		return nil, errors.New("Sync Telegram transport is unavailable")
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State == model.SyncStateActive || state.State == model.SyncStateDraining {
		return &DirectResponse{Text: "Sync is already active. The activation snapshot is immutable; use /status."}, nil
	}
	if err := forum.ValidateSyncGroup(ctx, userID); err != nil {
		return nil, fmt.Errorf("validate Sync group: %w", err)
	}
	if err := forum.PrepareSyncControl(ctx); err != nil {
		return nil, fmt.Errorf("prepare Sync Control: %w", err)
	}
	s.cleanupSyncTopics(ctx, state.SessionID)

	poll, err := s.controlReadSession(ctx)
	if err != nil {
		return nil, err
	}
	threads, err := listSyncActivationThreads(ctx, poll)
	if err != nil {
		return nil, fmt.Errorf("Sync activation thread/list: %w", err)
	}
	filtered := threads[:0]
	for _, thread := range threads {
		if thread.ID != "" && !thread.Archived && !thread.IsInternal() {
			filtered = append(filtered, thread)
		}
	}
	sortSyncActivationThreads(filtered)
	limit := syncInitialTopicLimit(s.cfg.SyncInitialTopicLimit)
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	now := time.Now().UTC()
	sessionID := randomToken()
	if err := s.store.BeginSyncActivation(ctx, sessionID, s.cfg.SyncGroupID); err != nil {
		return nil, err
	}
	summary := syncActivationSummary{SnapshotAt: now.Format(time.RFC3339Nano), Selected: len(filtered)}
	for rank, thread := range filtered {
		summary.Items = append(summary.Items, syncActivationItem{Rank: rank + 1, ThreadID: thread.ID, Title: syncTopicTitle(thread), Telegram: "pending", Codex: syncCodexStatus(thread)})
	}
	for rank, thread := range filtered {
		title := syncTopicTitle(thread)
		topicID, createErr := forum.CreateSyncTopic(ctx, title)
		if createErr != nil {
			var failure *SyncForumFailure
			entry := fmt.Sprintf("%s: %s", thread.ID, createErr)
			if errors.As(createErr, &failure) && failure.Kind == SyncForumFailureDefinitive {
				summary.Failed = append(summary.Failed, entry)
				summary.Items[rank].Telegram = "create failed"
				summary.Items[rank].Error = sanitizeDiagnosticString(createErr.Error())
				continue
			}
			summary.Unknown = append(summary.Unknown, entry)
			summary.Items[rank].Telegram = "create outcome unknown"
			summary.Items[rank].Error = sanitizeDiagnosticString(createErr.Error())
			for skippedRank := rank + 1; skippedRank < len(filtered); skippedRank++ {
				summary.Skipped = append(summary.Skipped, filtered[skippedRank].ID)
				summary.Items[skippedRank].Telegram = "skipped after unknown outcome"
			}
			break
		}
		if err := s.store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: sessionID, ChatID: s.cfg.SyncGroupID,
			TopicID: topicID, ThreadID: thread.ID, Rank: rank + 1, Title: title, TelegramState: model.SyncTopicConnected}); err != nil {
			_ = forum.DeleteSyncTopic(ctx, topicID)
			summary.Failed = append(summary.Failed, fmt.Sprintf("%s: persist: %s", thread.ID, err))
			summary.Items[rank].Telegram, summary.Items[rank].Error = "binding failed", sanitizeDiagnosticString(err.Error())
			continue
		}
		summary.Created++
		summary.Items[rank].Telegram = "connected"
		_ = s.store.UpsertThread(ctx, thread)
	}
	summaryJSON, _ := json.Marshal(summary)
	eventID := "sync-activation:" + sessionID
	payloadJSON, _ := json.Marshal(model.DeliveryPayload{Text: renderSyncActivationSummary(summary), EventID: eventID})
	delivery := model.DeliveryQueueItem{
		EventID: eventID, ChatKey: model.ChatKey(s.cfg.SyncGroupID, syncGeneralSendTopicID),
		ChatID: s.cfg.SyncGroupID, TopicID: syncGeneralSendTopicID, Kind: syncActivationDeliveryKind,
		Status: model.DeliveryStatusPending, AvailableAt: model.NowString(), PayloadJSON: string(payloadJSON),
		CreatedAt: model.NowString(), UpdatedAt: model.NowString(),
	}
	if err := s.store.FinishSyncActivationWithDelivery(ctx, sessionID, string(summaryJSON), summary.Created > 0, delivery); err != nil {
		return nil, err
	}
	if summary.Created > 0 {
		_ = s.syncWriter.AcceptNewWork()
	}
	return nil, nil
}

func (s *Service) deactivateSync(ctx context.Context) (*DirectResponse, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return nil, err
	}
	if state.State == model.SyncStateOff || state.SessionID == "" {
		return &DirectResponse{Text: "Sync is already off."}, nil
	}
	writer := s.syncWriter.Snapshot()
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	drafts, err := s.store.ListSyncTopicDrafts(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	if writer.Starting+writer.Active+writer.Unknown > 0 || hasUnfinishedSyncTopics(topics) || hasUnfinishedSyncDrafts(drafts) {
		titles := unfinishedSyncWorkTitles(topics, drafts)
		return &DirectResponse{Text: "Sync off refused; unfinished topics: " + strings.Join(titles, ", ") + ". Use /sync off --force to drain."}, nil
	}
	topics, err = s.store.MarkSyncOff(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	forum := s.getSyncForum()
	deleted, pending := 0, 0
	for _, topic := range topics {
		if topic.TelegramState != model.SyncTopicCleanup {
			continue
		}
		if forum == nil || forum.DeleteSyncTopic(ctx, topic.TopicID) != nil {
			pending++
			continue
		}
		if err := s.store.DeleteSyncTopic(ctx, topic.SessionID, topic.TopicID); err != nil {
			pending++
		} else {
			deleted++
		}
	}
	return &DirectResponse{Text: fmt.Sprintf("Sync off: deleted %d topic(s), cleanup pending %d.", deleted, pending)}, nil
}

func (s *Service) syncStatus(ctx context.Context) (*DirectResponse, error) {
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return nil, err
	}
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	drafts, err := s.store.ListSyncTopicDrafts(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	health := strings.Join(s.healthStatusLines(ctx, time.Now().UTC()), "\n")
	return &DirectResponse{Text: fmt.Sprintf("Sync state: %s\nSession: %s\nInitial topic limit: %d\nConnected topics: %d\nReady drafts: %d\n%s\nPoller status: /pollers\nExternal requests: /requests\nRefresh command: /refresh\nRepair command: /repair\nNew task commands: /projects, /newchat\nActivation summary: %s",
		state.State, state.SessionID, syncInitialTopicLimit(s.cfg.SyncInitialTopicLimit), countConnectedSyncTopics(topics), countReadySyncDrafts(drafts), health, strings.TrimSpace(state.ActivationSummaryJSON))}, nil
}

func (s *Service) reconcileSync(ctx context.Context) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	_, _ = s.reconcileSyncLocked(ctx)
}

type reconcileSyncResult struct {
	Discovered           int
	DiscoveryFailures    int
	Connected            int
	SubscriptionFailures int
	ReadFailures         int
}

func (s *Service) reconcileSyncCommand(ctx context.Context) (*DirectResponse, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	result, err := s.reconcileSyncLocked(ctx)
	if err != nil {
		return &DirectResponse{Text: "Sync failed: " + sanitizeDiagnosticString(err.Error())}, nil
	}
	return &DirectResponse{Text: fmt.Sprintf("Sync complete. discovered: %d, discovery failures: %d, connected: %d, subscription failures: %d, read failures: %d",
		result.Discovered, result.DiscoveryFailures, result.Connected, result.SubscriptionFailures, result.ReadFailures)}, nil
}

func (s *Service) reconcileSyncLocked(ctx context.Context) (reconcileSyncResult, error) {
	var result reconcileSyncResult
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return result, err
	}
	if state.State != model.SyncStateActive && state.State != model.SyncStateDraining {
		return result, errors.New("Sync is not active")
	}
	forum := s.getSyncForum()
	if forum == nil {
		return result, errors.New("Sync Telegram transport is unavailable")
	}
	s.mu.RLock()
	poll, connected, pollGeneration := s.poll, s.pollConnected, s.pollGeneration
	s.mu.RUnlock()
	if !connected || poll == nil {
		return result, errors.New("App Server poll session is unavailable")
	}
	topics, err := s.store.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		return result, err
	}
	var discoveryErr error
	if state.State == model.SyncStateActive {
		discovered, failures, discoverErr := s.discoverSyncThreadsLocked(ctx, state, forum, poll, topics)
		result.Discovered = discovered
		result.DiscoveryFailures = failures
		discoveryErr = discoverErr
		if discoverErr == nil {
			topics, err = s.store.ListSyncTopics(ctx, state.SessionID)
			if err != nil {
				return result, err
			}
		}
	}
	for _, topic := range topics {
		if topic.TelegramState != model.SyncTopicConnected {
			continue
		}
		result.Connected++
		if !s.subscribeSyncThreadLocked(ctx, poll, pollGeneration, topic.ThreadID) {
			result.SubscriptionFailures++
		}
		payload, readErr := poll.ThreadRead(ctx, topic.ThreadID, true)
		if readErr != nil || payload == nil {
			result.ReadFailures++
			continue
		}
		current := appserver.SnapshotFromThreadRead(payload)
		s.processSyncSnapshotLocked(ctx, state, forum, topic, current, "sync_poll")
	}
	return result, discoveryErr
}

func (s *Service) discoverSyncThreadsLocked(ctx context.Context, state model.SyncState, forum SyncForum, poll Session, topics []model.SyncTopic) (int, int, error) {
	cutoff := parseTime(state.SnapshotAt)
	if cutoff.IsZero() {
		return 0, 0, errors.New("Sync activation cutoff is unavailable")
	}
	result, err := poll.ThreadList(ctx, syncRecentThreadLimit, "")
	if err != nil {
		return 0, 0, fmt.Errorf("Sync discovery thread/list: %w", err)
	}
	bound := make(map[string]struct{}, len(topics))
	nextRank := 0
	for _, topic := range topics {
		bound[topic.ThreadID] = struct{}{}
		if topic.Rank > nextRank {
			nextRank = topic.Rank
		}
	}
	drafts, err := s.store.ListSyncTopicDrafts(ctx, state.SessionID)
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
		title := syncTopicTitle(thread)
		topicID, createErr := forum.CreateSyncTopic(ctx, title)
		if createErr != nil {
			failures++
			continue
		}
		nextRank++
		topic := model.SyncTopic{SessionID: state.SessionID, ChatID: state.ChatID, TopicID: topicID,
			ThreadID: thread.ID, Rank: nextRank, Title: title, TelegramState: model.SyncTopicConnected}
		if err := s.store.UpsertSyncTopic(ctx, topic); err != nil {
			_ = forum.DeleteSyncTopic(ctx, topicID)
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

func (s *Service) subscribeSyncThreadLocked(ctx context.Context, poll Session, pollGeneration uint64, threadID string) bool {
	if !s.usesSharedAppServer() {
		return true
	}
	if s.syncSubscribedThreads == nil || s.syncSubscribedPollGeneration != pollGeneration {
		s.syncSubscribedThreads = map[string]struct{}{}
		s.syncSubscribedPollGeneration = pollGeneration
	}
	if _, ok := s.syncSubscribedThreads[threadID]; ok {
		return true
	}
	if _, err := poll.ThreadResume(ctx, threadID, ""); err != nil {
		return false
	}
	s.syncSubscribedThreads[threadID] = struct{}{}
	return true
}

func syncInitialTopicLimit(value int) int {
	if value <= 0 {
		return config.DefaultSyncInitialTopicLimit
	}
	return value
}

func (s *Service) startSyncTelegramOriginHotPoll(ctx context.Context, threadID, turnID string) {
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
			return s.syncTelegramOriginHotPollOnce(ctx, threadID, turnID)
		})
	})
}

func (s *Service) syncTelegramOriginHotPollOnce(ctx context.Context, threadID, turnID string) bool {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	if threadID == "" || turnID == "" {
		return false
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	state, err := s.store.GetSyncState(ctx)
	if err != nil || (state.State != model.SyncStateActive && state.State != model.SyncStateDraining) {
		return false
	}
	topic, err := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, threadID)
	if err != nil || topic == nil || topic.ActiveTurnState != model.SyncTurnActive || strings.TrimSpace(topic.ActiveTurnID) != turnID {
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
	forum := s.getSyncForum()
	s.processSyncSnapshotLocked(ctx, state, forum, *topic, current, "sync_hot_poll")
	updated, err := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, threadID)
	return err == nil && updated != nil && updated.ActiveTurnState == model.SyncTurnActive && strings.TrimSpace(updated.ActiveTurnID) == turnID
}

func (s *Service) installSyncWriterLocked(lease appserver.WriterLease[Session]) {
	if s.syncEventProcess == lease.Process && s.syncEventGeneration == lease.Generation {
		return
	}
	if s.syncEventCancel != nil {
		s.syncEventCancel()
	}
	events := lease.Process.Subscribe()
	s.mu.RLock()
	baseCtx, started := s.runCtx, s.started
	s.mu.RUnlock()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	loopCtx, cancel := context.WithCancel(baseCtx)
	s.syncEventProcess, s.syncEventGeneration, s.syncEventCancel = lease.Process, lease.Generation, cancel
	if events == nil {
		return
	}
	loop := func() { s.syncWriterEventLoop(loopCtx, lease.Process, events, lease.Generation) }
	if started {
		s.wg.Add(1)
		go func() { defer s.wg.Done(); loop() }()
	} else {
		go loop()
	}
}

func (s *Service) syncWriterEventLoop(ctx context.Context, process Session, events <-chan appserver.Event, generation uint64) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			s.handleSyncWriterEvent(ctx, process, event, generation)
		}
	}
}

func (s *Service) handleSyncWriterEvent(ctx context.Context, process Session, event appserver.Event, generation uint64) {
	threadID := threadIDFromEvent(event)
	if threadID == "" {
		return
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if s.syncEventProcess != process || s.syncEventGeneration != generation {
		return
	}
	lease, ok := s.syncLeases[threadID]
	if !ok || lease.Generation != generation {
		return
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil || (state.State != model.SyncStateActive && state.State != model.SyncStateDraining) {
		return
	}
	topic, err := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, threadID)
	if err != nil || topic == nil || topic.WriterGeneration != generation {
		return
	}
	if eventTurnID := syncEventTurnID(event); eventTurnID != "" && topic.ActiveTurnID != "" && eventTurnID != topic.ActiveTurnID {
		return
	}
	if approval, ok := appserver.PendingApprovalFromServerRequest(event); ok {
		s.handleSyncPendingRequestLocked(ctx, *topic, *approval)
		return
	}
	if strings.EqualFold(event.Method, "serverRequest/resolved") {
		if requestID := payloadMapString(event.Params, "requestId"); requestID != "" {
			_ = s.store.ExpireSyncCallbackRoutesByRequest(ctx, requestID)
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
	forum := s.getSyncForum()
	s.processSyncSnapshotLocked(ctx, state, forum, *topic, current, "sync_event")
}

func (s *Service) processSyncSnapshotLocked(ctx context.Context, state model.SyncState, forum SyncForum, topic model.SyncTopic, current appserver.ThreadReadSnapshot, operation string) {
	activeTurnID := strings.TrimSpace(topic.ActiveTurnID)
	currentTurnID := strings.TrimSpace(current.LatestTurnID)
	if topic.ActiveTurnState == model.SyncTurnActive && activeTurnID != "" && currentTurnID != "" && currentTurnID != activeTurnID {
		if !s.usesSharedAppServer() ||
			!s.releaseSyncTurnLeaseLocked(ctx, state, topic, activeTurnID) {
			return
		}
		topic.ActiveTurnState = model.SyncTurnTerminal
	}
	if topic.ActiveTurnState == model.SyncTurnActive &&
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
		s.persistAndDeliverSyncSnapshotLocked(ctx, forum, topic, current)
	}
	s.completeSyncTurnLocked(ctx, state, topic, current)
}

func (s *Service) completeSyncTurnLocked(ctx context.Context, state model.SyncState, topic model.SyncTopic, current appserver.ThreadReadSnapshot) {
	if !isTerminalStatus(current.LatestTurnStatus) || strings.TrimSpace(current.LatestTurnID) == "" || current.LatestTurnID != topic.ActiveTurnID {
		return
	}
	_ = s.releaseSyncTurnLeaseLocked(ctx, state, topic, current.LatestTurnID)
}

func (s *Service) releaseSyncTurnLeaseLocked(ctx context.Context, state model.SyncState, topic model.SyncTopic, turnID string) bool {
	lease, ok := s.syncLeases[topic.ThreadID]
	if !ok || lease.Generation != topic.WriterGeneration {
		return false
	}
	if err := s.store.MarkSyncTerminal(ctx, state.SessionID, topic.ThreadID, turnID, topic.WriterGeneration); err != nil {
		return false
	}
	_ = s.store.ExpireSyncCallbackRoutes(ctx, topic.ThreadID, turnID)
	delete(s.syncLeases, topic.ThreadID)
	if err := s.syncWriter.MarkTerminal(lease); err != nil {
		return false
	}
	if s.syncWriter.Snapshot().State == appserver.WriterStopped && s.syncEventCancel != nil {
		s.syncEventCancel()
		s.syncEventCancel, s.syncEventProcess, s.syncEventGeneration = nil, nil, 0
	}
	return true
}

func (s *Service) persistAndDeliverSyncSnapshotLocked(ctx context.Context, forum SyncForum, topic model.SyncTopic, current appserver.ThreadReadSnapshot) {
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
		desiredTitle = syncTopicTitle(current.Thread)
		if desiredTitle != topic.Title && forum.RenameSyncTopic(ctx, topic.TopicID, desiredTitle) == nil {
			if currentTurnID != "" &&
				topic.StatusMessageID != 0 &&
				strings.TrimSpace(topic.StatusTurnID) == currentTurnID &&
				!isTerminalStatus(observed.LatestTurnStatus) {
				oldStatusID := topic.StatusMessageID
				reset, err := s.store.ResetSyncTopicStatusDelivery(ctx, topic.SessionID, topic.TopicID, oldStatusID, currentTurnID)
				if err != nil || !reset {
					return
				}
				_ = forum.DeleteSyncMessage(ctx, topic.TopicID, oldStatusID)
				topic.StatusMessageID = 0
				topic.StatusTurnID = ""
				topic.LastRenderFP = ""
			}
			_ = s.store.UpdateSyncTopicTitle(ctx, topic.SessionID, topic.TopicID, desiredTitle)
		}
	}
	var userDeliveryOK bool
	topic, userDeliveryOK = s.deliverSyncUserMessageLocked(ctx, forum, topic, observed)
	if !userDeliveryOK {
		return
	}
	statusMessage := renderSyncStatusAt(observed, observedAt)
	renderFP := syncFingerprint(tgformat.HashRendered(statusMessage))
	statusID := topic.StatusMessageID
	statusTurnID := strings.TrimSpace(topic.StatusTurnID)
	newObservedTurn := statusTurnID != "" && currentTurnID != "" && statusTurnID != currentTurnID
	var deliveryErr error
	if statusID == 0 || newObservedTurn {
		statusID, deliveryErr = forum.SendSyncMessage(ctx, topic.TopicID, statusMessage, model.SendOptions{Silent: true, Background: true})
	} else if renderFP != topic.LastRenderFP {
		deliveryErr = forum.EditSyncMessage(ctx, topic.TopicID, statusID, statusMessage, model.SendOptions{Background: true})
	}
	if deliveryErr != nil {
		return
	}
	if currentTurnID != "" {
		statusTurnID = currentTurnID
	}
	finalFP := topic.LastFinalFP
	if strings.TrimSpace(current.LatestFinalFP) != "" && current.LatestFinalFP != topic.LastFinalFP {
		finalMessages := renderSyncFinal(current.LatestFinalText)
		for index, message := range finalMessages {
			if _, deliveryErr = forum.SendSyncMessage(ctx, topic.TopicID, message, model.SendOptions{}); deliveryErr != nil {
				logKey := strings.Join([]string{"sync_final_delivery_failed", topic.ThreadID, current.LatestTurnID, current.LatestFinalFP}, ":")
				if s.allowDiagnosticRepeat(logKey, diagnosticRepeatWindow) {
					s.logLifecycle("sync_final_delivery_failed", lifecycleFields{
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
	_ = s.store.UpdateSyncTopicDelivery(ctx, topic.SessionID, topic.TopicID, statusID, statusTurnID, renderFP, finalFP)
}

func renderSyncFinal(finalText string) []model.RenderedMessage {
	body := strings.TrimSpace(finalText)
	if body == "" {
		return []model.RenderedMessage{{Text: syncFinalHeader}}
	}
	prefix := syncFinalHeader + "\n"
	prefixUnits := syncUTF16Len(prefix)
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

func (s *Service) deliverSyncUserMessageLocked(ctx context.Context, forum SyncForum, topic model.SyncTopic, current appserver.ThreadReadSnapshot) (model.SyncTopic, bool) {
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
			if syncUserTextFingerprint(turnID, userText) != pendingFP {
				return topic, false
			}
			if err := s.store.UpdateSyncTopicUserDelivery(ctx, topic.SessionID, topic.TopicID, userFP, "", ""); err != nil {
				return topic, false
			}
			topic.LastUserFP = userFP
			topic.PendingTelegramTurnID = ""
			topic.PendingTelegramUserFP = ""
			return topic, true
		}
		if err := s.store.UpdateSyncTopicUserDelivery(ctx, topic.SessionID, topic.TopicID, topic.LastUserFP, "", ""); err != nil {
			return topic, false
		}
		topic.PendingTelegramTurnID = ""
		topic.PendingTelegramUserFP = ""
	}
	if topic.StatusMessageID != 0 && strings.TrimSpace(topic.StatusTurnID) == turnID {
		oldStatusID := topic.StatusMessageID
		reset, err := s.store.ResetSyncTopicStatusDelivery(ctx, topic.SessionID, topic.TopicID, oldStatusID, turnID)
		if err != nil || !reset {
			return topic, false
		}
		_ = forum.DeleteSyncMessage(ctx, topic.TopicID, oldStatusID)
		topic.StatusMessageID = 0
		topic.StatusTurnID = ""
		topic.LastRenderFP = ""
	}
	if _, err := forum.SendSyncMessage(ctx, topic.TopicID, model.RenderedMessage{Text: syncUserHeader + "\n" + userText}, model.SendOptions{Silent: true}); err != nil {
		return topic, false
	}
	if err := s.store.UpdateSyncTopicUserDelivery(ctx, topic.SessionID, topic.TopicID, userFP, "", ""); err != nil {
		return topic, false
	}
	topic.LastUserFP = userFP
	return topic, true
}

func (s *Service) reanchorSyncDirectDelivery(ctx context.Context, chatID, topicID int64, response *DirectResponse) {
	if response == nil || !s.isSyncGroup(chatID) || isSyncControlTopic(topicID) {
		return
	}
	threadID := strings.TrimSpace(response.ThreadID)
	turnID := strings.TrimSpace(response.TurnID)
	if threadID == "" || turnID == "" {
		return
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	state, err := s.store.GetSyncState(ctx)
	if err != nil || (state.State != model.SyncStateActive && state.State != model.SyncStateDraining) {
		return
	}
	topic, err := s.store.GetActiveSyncTopic(ctx, chatID, topicID)
	if err != nil || topic == nil || strings.TrimSpace(topic.ThreadID) != threadID {
		return
	}
	forum := s.getSyncForum()
	if forum == nil {
		return
	}
	if topic.StatusMessageID != 0 && strings.TrimSpace(topic.StatusTurnID) == turnID {
		oldStatusID := topic.StatusMessageID
		reset, resetErr := s.store.ResetSyncTopicStatusDelivery(ctx, topic.SessionID, topic.TopicID, oldStatusID, turnID)
		if resetErr != nil || !reset {
			return
		}
		_ = forum.DeleteSyncMessage(ctx, topic.TopicID, oldStatusID)
		topic, err = s.store.GetActiveSyncTopic(ctx, chatID, topicID)
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
	s.processSyncSnapshotLocked(ctx, state, forum, *topic, current, "sync_direct_delivery")
}

func (s *Service) cleanupSyncTopics(ctx context.Context, sessionID string) {
	if strings.TrimSpace(sessionID) == "" {
		return
	}
	forum := s.getSyncForum()
	if forum == nil {
		return
	}
	topics, _ := s.store.ListSyncCleanupTargets(ctx, sessionID)
	for _, topic := range topics {
		if isSyncControlTopic(topic.TopicID) {
			continue
		}
		if topic.TelegramState == model.SyncTopicCleanup && forum.DeleteSyncTopic(ctx, topic.TopicID) == nil {
			_ = s.store.DeleteSyncTopic(ctx, sessionID, topic.TopicID)
		}
	}
}

// FinishStartup runs after the Telegram bot is ready. It cleans the previous
// Sync session and reports a missing shared App Server once per process start.
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
	s.cleanupSyncTopics(ctx, cleanupSessionID)
	s.ensurePollSession(ctx)

	s.mu.RLock()
	connected := s.pollConnected
	forum := s.syncForum
	s.mu.RUnlock()
	if connected || forum == nil || s.cfg.SyncGroupID == 0 || !s.usesSharedAppServer() {
		return
	}

	message := model.RenderedMessage{Text: s.sharedAppServerStartupWarning()}
	if _, err := forum.SendSyncMessage(ctx, syncGeneralSendTopicID, message, model.SendOptions{}); err != nil {
		s.logLifecycle("sync_startup_warning_failed", lifecycleFields{"error": err})
		return
	}
	s.logLifecycle("sync_startup_warning_sent", nil)
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
		"Sync was reset to off.\n\n" +
		"Fix:\n" + fix
}

func (s *Service) getSyncForum() SyncForum { s.mu.RLock(); defer s.mu.RUnlock(); return s.syncForum }

func syncTopicTitle(thread model.Thread) string {
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

func renderSyncActivationSummary(summary syncActivationSummary) string {
	text := fmt.Sprintf("Sync activation snapshot %s\nselected: %d\nactive: %d\nfailed: %d\nunknown: %d\nskipped: %d", summary.SnapshotAt, summary.Selected, summary.Created, len(summary.Failed), len(summary.Unknown), len(summary.Skipped))
	for _, item := range summary.Items {
		text += fmt.Sprintf("\n\n%d. %s\nTelegram: %s\nCodex: %s", item.Rank, item.Title, item.Telegram, item.Codex)
		if item.Error != "" {
			text += "\nError: " + item.Error
		}
	}
	return text
}

func syncCodexStatus(thread model.Thread) string {
	if strings.TrimSpace(thread.ActiveTurnID) != "" {
		return "running"
	}
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

func listSyncActivationThreads(ctx context.Context, poll Session) ([]model.Thread, error) {
	const pageSize = 100
	threads := make([]model.Thread, 0, pageSize)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		result, err := poll.ThreadList(ctx, pageSize, cursor)
		if err != nil {
			return nil, err
		}
		threads = append(threads, appserver.ThreadsFromList(result)...)
		nextCursor, _ := result["nextCursor"].(string)
		nextCursor = strings.TrimSpace(nextCursor)
		if nextCursor == "" {
			return threads, nil
		}
		if _, exists := seenCursors[nextCursor]; exists {
			return nil, errors.New("thread/list returned a repeated cursor")
		}
		seenCursors[nextCursor] = struct{}{}
		cursor = nextCursor
	}
}

func sortSyncActivationThreads(threads []model.Thread) {
	sort.Slice(threads, func(i, j int) bool {
		leftPriority := syncActivationThreadPriority(threads[i])
		rightPriority := syncActivationThreadPriority(threads[j])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if threads[i].UpdatedAt != threads[j].UpdatedAt {
			return threads[i].UpdatedAt > threads[j].UpdatedAt
		}
		return threads[i].ID < threads[j].ID
	})
}

func syncActivationThreadPriority(thread model.Thread) int {
	switch syncCodexStatus(thread) {
	case "waiting", "running":
		return 0
	case "unknown":
		return 1
	case "interrupted":
		return 2
	case "completed":
		return 3
	default:
		return 1
	}
}

func renderSyncStatusAt(snapshot appserver.ThreadReadSnapshot, now time.Time) model.RenderedMessage {
	now = syncStatusRenderTime(snapshot, now)
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
	header := syncStatusHeader + " " + status
	if duration, _ := runTimingValue(&snapshot, now); duration != "" {
		header += " · " + duration
	}

	blocks := syncStatusBlocks(snapshot.DetailItems)
	if len(blocks) == 0 {
		return model.RenderedMessage{Text: header}
	}
	body := renderSyncStatusBlocks(snapshot, blocks, now)
	body = trimSyncStatusBody(header, body)
	message := model.RenderedMessage{Text: header + "\n" + body}
	if isTerminalStatus(snapshot.LatestTurnStatus) && body != "" {
		message.Entities = []model.MessageEntity{{
			Type:   "expandable_blockquote",
			Offset: syncUTF16Len(header + "\n"),
			Length: syncUTF16Len(body),
		}}
	}
	return message
}

func syncStatusRenderTime(snapshot appserver.ThreadReadSnapshot, now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if isTerminalStatus(snapshot.LatestTurnStatus) {
		return now
	}
	startedAt := parseTime(model.TimeString(snapshot.LatestTurnStartedAt))
	if startedAt.IsZero() {
		return now.Truncate(syncStatusTimerTick)
	}
	elapsed := now.Sub(startedAt)
	if elapsed <= 0 {
		return startedAt
	}
	return startedAt.Add(elapsed.Truncate(syncStatusTimerTick))
}

func syncStatusBlocks(items []model.DetailItem) []model.DetailItem {
	blocks := make([]model.DetailItem, 0, len(items))
	for _, item := range items {
		if (item.Kind == model.DetailItemCommentary || item.Kind == model.DetailItemPlan) && strings.TrimSpace(item.Text) != "" {
			blocks = append(blocks, item)
		}
	}
	return blocks
}

func renderSyncStatusBlocks(snapshot appserver.ThreadReadSnapshot, blocks []model.DetailItem, now time.Time) string {
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

func trimSyncStatusBody(header, body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	budget := tgformat.TelegramMessageLimit - syncUTF16Len(header+"\n")
	if budget <= 0 {
		return ""
	}
	if syncUTF16Len(body) <= budget {
		return body
	}
	lines := strings.Split(body, "\n")
	for removed := 1; removed < len(lines); removed++ {
		candidate := fmt.Sprintf("… удалено строк: %d\n%s", removed, strings.Join(lines[removed:], "\n"))
		if syncUTF16Len(candidate) <= budget {
			return candidate
		}
	}
	marker := fmt.Sprintf("… удалено строк: %d", len(lines))
	remaining := budget - syncUTF16Len(marker+"\n")
	if remaining <= 0 {
		return syncUTF16Suffix(marker, budget)
	}
	tail := syncUTF16Suffix(lines[len(lines)-1], remaining)
	if tail == "" {
		return marker
	}
	return marker + "\n" + tail
}

func syncUTF16Len(text string) int {
	return len(utf16.Encode([]rune(text)))
}

func syncUTF16Suffix(text string, limit int) string {
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

func syncFingerprint(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:12])
}

func syncUserTextFingerprint(turnID, text string) string {
	return syncFingerprint(strings.TrimSpace(turnID) + "\x00" + strings.TrimSpace(text))
}
func countConnectedSyncTopics(topics []model.SyncTopic) int {
	n := 0
	for _, topic := range topics {
		if topic.TelegramState == model.SyncTopicConnected {
			n++
		}
	}
	return n
}

func countReadySyncDrafts(drafts []model.SyncTopicDraft) int {
	n := 0
	for _, draft := range drafts {
		if draft.State == model.SyncDraftReady {
			n++
		}
	}
	return n
}

func syncDuplicateReceiptResponse(receipt model.SyncMessageReceipt) *DirectResponse {
	switch receipt.State {
	case model.SyncReceiptDispatched:
		return &DirectResponse{Text: "This Telegram message was already dispatched; it will not be replayed."}
	case model.SyncReceiptUnknown:
		return &DirectResponse{Text: "This Telegram message has an unknown dispatch outcome and will never be replayed automatically."}
	case model.SyncReceiptRejected:
		return &DirectResponse{Text: "This Telegram message was already rejected and will not be replayed."}
	default:
		return &DirectResponse{Text: "This Telegram message was already accepted; duplicate delivery was ignored."}
	}
}

func syncDispatchAmbiguous(err error, turnID string) bool {
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

func syncEventTurnID(event appserver.Event) string {
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
