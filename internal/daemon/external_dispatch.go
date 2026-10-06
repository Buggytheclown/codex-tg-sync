package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) dispatchExternalLaunchRequest(ctx context.Context, requestID string) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()

	request, err := s.store.GetExternalLaunchRequest(ctx, requestID)
	if err != nil || request == nil || request.Status != model.ExternalLaunchStarting {
		return
	}
	if strings.TrimSpace(request.TargetThreadID) != "" {
		s.dispatchExternalContinuationLocked(ctx, *request)
		return
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return
	}
	if state.State != model.SyncStateActive || state.ChatID != s.cfg.SyncGroupID || s.syncMutationErrorLocked(ctx) != nil {
		summary := "Sync is inactive; enable Sync and press Start again."
		if request.AutoStart {
			summary = "Sync is inactive; the request will start automatically after Sync is enabled."
		}
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "sync_inactive", summary)
		s.finishExternalLaunchDispatch()
		return
	}
	forum := s.getSyncForum()
	if forum == nil {
		summary := "Telegram transport is unavailable; press Start to retry."
		if request.AutoStart {
			summary = "Telegram transport is unavailable; the request will retry automatically."
		}
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "telegram_unavailable", summary)
		s.finishExternalLaunchDispatch()
		return
	}

	telegramPreview := externalLaunchTelegramPreview(*request)
	title := syncPromptTopicTitle(telegramPreview)
	topicID, err := forum.CreateLaunchTopic(ctx, title)
	if err != nil {
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "telegram_topic", "Telegram could not create the session topic.")
		s.finishExternalLaunchDispatch()
		return
	}
	topics, _ := s.store.ListSyncTopics(ctx, state.SessionID)
	drafts, _ := s.store.ListSyncTopicDrafts(ctx, state.SessionID)
	projectName, directoryName := model.ProjectNameFromCWD(request.CWD)
	draft := model.SyncTopicDraft{
		SessionID: state.SessionID, ChatID: state.ChatID, TopicID: topicID, Rank: len(topics) + len(drafts) + 1,
		Title: title, CWD: request.CWD, ProjectName: projectName, DirectoryName: directoryName,
	}
	if err := s.store.CreateSyncTopicDraft(ctx, draft); err != nil {
		_ = forum.DeleteSyncTopic(ctx, topicID)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "sync_draft", "Sync could not persist the new session topic.")
		s.finishExternalLaunchDispatch()
		return
	}
	sourceMessageID, err := forum.SendSyncMessage(ctx, topicID, model.RenderedMessage{Text: syncUserHeader + "\n" + telegramPreview}, model.SendOptions{Silent: true})
	if err != nil {
		_ = forum.DeleteSyncTopic(ctx, topicID)
		_ = s.store.DeleteSyncTopic(ctx, state.SessionID, topicID)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "telegram_prompt", "Telegram could not write the initial request into the session topic.")
		s.finishExternalLaunchDispatch()
		return
	}
	claimed, receipt, created, err := s.store.ClaimSyncTopicDraftMessage(ctx, state.ChatID, topicID, sourceMessageID)
	if err != nil || !created {
		_ = forum.DeleteSyncTopic(ctx, topicID)
		_ = s.store.DeleteSyncTopic(ctx, state.SessionID, topicID)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "sync_claim", "Sync could not claim the new session topic.")
		s.finishExternalLaunchDispatch()
		return
	}
	// Persist the route to the existing durable dispatch receipt before any
	// App Server mutation. Restart can bind a confirmed launch without replay.
	receiptJSON, _ := json.Marshal(externalLaunchReceiptRoute{TopicID: topicID, MessageID: sourceMessageID})
	if err := s.store.SetState(ctx, externalLaunchReceiptKey(request.ID), string(receiptJSON)); err != nil {
		_ = s.store.ResetSyncTopicDraftMessage(ctx, claimed, receipt)
		_ = forum.DeleteSyncTopic(ctx, topicID)
		_ = s.store.DeleteSyncTopic(ctx, state.SessionID, topicID)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "receipt_persistence", "Could not persist the launch dispatch receipt; no Codex work was started.")
		s.finishExternalLaunchDispatch()
		return
	}
	permissions := appserver.ThreadStartOptions{
		ApprovalPolicy:    frozenExternalSetting(request.ApprovalPolicy, s.cfg.ExternalApprovalPolicy),
		ApprovalsReviewer: frozenExternalSetting(request.ApprovalsReviewer, s.cfg.ExternalApprovalsReviewer),
		SandboxMode:       frozenExternalSetting(request.SandboxMode, s.cfg.ExternalSandboxMode),
	}
	execution := appserver.TurnStartOptions{Model: request.Model, ReasoningEffort: request.ReasoningEffort}
	response, dispatchErr := s.startClaimedSyncDraftLocked(ctx, claimed, receipt, request.Prompt, telegramPreview, permissions, execution)
	storedReceipt, receiptErr := s.store.GetSyncReceipt(ctx, topicID, sourceMessageID)
	if response != nil && strings.TrimSpace(response.ThreadID) != "" && strings.TrimSpace(response.TurnID) != "" && dispatchErr == nil {
		changed, completeErr := s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted,
			response.ThreadID, response.TurnID, "", "")
		if completeErr != nil || !changed {
			_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchOutcomeUnknown,
				response.ThreadID, response.TurnID, "persistence_unknown", "Codex started but task/request confirmation is uncertain; this request will not be replayed.")
		}
		s.pruneSyncTopics(ctx, state.SessionID)
		s.queueExternalReplyFromStoredSnapshot(ctx, response.ThreadID)
		s.finishExternalLaunchDispatch()
		return
	}

	threadID := ""
	if storedReceipt != nil {
		threadID = storedReceipt.ThreadID
	}
	status := model.ExternalLaunchOutcomeUnknown
	errorType := "dispatch_unknown"
	errorSummary := "Sync dispatch outcome is unknown; this request will not be replayed automatically."
	if receiptErr == nil && storedReceipt != nil && storedReceipt.State == model.SyncReceiptRejected && dispatchErr == nil {
		status = model.ExternalLaunchFailed
		errorType = "dispatch_rejected"
		errorSummary = "Sync rejected the request before its first turn could start."
	}
	_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, status, threadID, "", errorType, errorSummary)
	if status == model.ExternalLaunchFailed {
		_ = forum.DeleteSyncTopic(ctx, topicID)
		_ = s.store.DeleteSyncTopic(ctx, state.SessionID, topicID)
	}
	s.finishExternalLaunchDispatch()
}

func (s *Service) finishExternalLaunchDispatch() {
	s.wakeExternalLaunchRenderer()
}

func frozenExternalSetting(value *string, fallback string) string {
	if value != nil {
		return *value
	}
	return fallback
}

// The durable starting request is the dispatch receipt. Once TurnStart may have
// reached App Server it can only become started or unknown, never pending again.
func (s *Service) dispatchExternalContinuationLocked(ctx context.Context, request model.ExternalLaunchRequest) {
	defer s.finishExternalLaunchDispatch()
	threadID := strings.TrimSpace(request.TargetThreadID)
	initialState, stateErr := s.store.GetSyncState(ctx)
	if stateErr != nil || initialState.State != model.SyncStateActive || s.syncMutationErrorLocked(ctx) != nil {
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "sync_inactive", "Sync is inactive; enable Sync and press Start again.")
		return
	}

	fail := func(kind, message string) {
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", kind, message)
	}
	busy := func() {
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "thread_busy", "The target chat is busy; press Start again after its current turn completes.")
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if poll == nil || !connected {
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "appserver_unavailable", "App Server is unavailable; press Start after connectivity returns.")
		return
	}
	current, err := readAuthoritativeSyncSnapshot(ctx, poll, threadID)
	if err != nil {
		fail("target_read", "Could not read the original chat; no replacement chat was created: "+sanitizeDiagnosticString(err.Error()))
		return
	}
	if activeTurnIDFromSyncSnapshot(current) != "" || threadLooksActiveForInput(&current.Thread) {
		busy()
		return
	}
	if _, exists := s.syncLeases[threadID]; exists {
		busy()
		return
	}
	lease, err := s.syncWriter.Reserve(ctx, threadID)
	if err != nil {
		busy()
		return
	}
	abort := func() {
		delete(s.syncLeases, threadID)
		delete(s.boardContinuationLeases, threadID)
		_ = s.syncWriter.Abort(lease)
	}
	s.syncLeases[threadID] = lease
	s.installSyncWriterLocked(lease)
	cwd := strings.TrimSpace(request.CWD)
	if cwd == "" {
		cwd = current.Thread.CWD
	}
	if _, err := lease.Process.ThreadResume(ctx, threadID, cwd); err != nil {
		abort()
		fail("target_resume", "Could not resume the original chat; no replacement chat was created: "+sanitizeDiagnosticString(err.Error()))
		return
	}
	// Resume may attach to a chat that became busy after the first read.
	current, err = readAuthoritativeSyncSnapshot(ctx, lease.Process, threadID)
	if err != nil {
		abort()
		fail("target_read", "Could not verify the original chat before starting: "+sanitizeDiagnosticString(err.Error()))
		return
	}
	if activeTurnIDFromSyncSnapshot(current) != "" || threadLooksActiveForInput(&current.Thread) {
		abort()
		busy()
		return
	}
	var receipt *model.SyncMessageReceipt
	state, stateErr := s.store.GetSyncState(ctx)
	if stateErr == nil && state.State == model.SyncStateActive && s.syncAccessErrorLocked(ctx) == nil {
		topic, topicErr := s.store.GetActiveSyncTopicByThread(ctx, state.SessionID, threadID)
		forum := s.getSyncForum()
		if topicErr != nil {
			abort()
			fail("target_topic", "Could not verify the original chat's Telegram route.")
			return
		}
		if topic != nil && forum != nil {
			if topic.ActiveTurnState == model.SyncTurnStarting || topic.ActiveTurnState == model.SyncTurnUnknown {
				abort()
				busy()
				return
			}
			messageID, err := forum.SendSyncMessage(ctx, topic.TopicID, model.RenderedMessage{Text: syncUserHeader + "\n" + externalLaunchTelegramPreview(request)}, model.SendOptions{Silent: true})
			if err != nil {
				abort()
				fail("telegram_prompt", "Could not write the continuation into the original chat topic.")
				return
			}
			accepted, created, err := s.store.AcceptSyncMessage(ctx, topic.ChatID, topic.TopicID, messageID)
			if err != nil || !created {
				abort()
				fail("sync_claim", "Could not claim the continuation in its existing chat topic.")
				return
			}
			if err := s.store.MarkSyncStarting(ctx, accepted, lease.Generation); err != nil {
				abort()
				_ = s.store.MarkSyncReceiptState(ctx, accepted, model.SyncReceiptRejected)
				busy()
				return
			}
			receipt = &accepted
		}
	}
	options := s.turnStartOptions(ctx, "", &current.Thread)
	options.CollaborationMode = collaborationModeDefault
	options.Model, options.ReasoningEffort = request.Model, request.ReasoningEffort
	options.ApprovalPolicy = frozenExternalSetting(request.ApprovalPolicy, s.cfg.ExternalApprovalPolicy)
	options.ApprovalsReviewer = frozenExternalSetting(request.ApprovalsReviewer, s.cfg.ExternalApprovalsReviewer)
	options.SandboxMode = frozenExternalSetting(request.SandboxMode, s.cfg.ExternalSandboxMode)
	result, turnErr := lease.Process.TurnStart(ctx, threadID, request.Prompt, cwd, options)
	turnID := appserverThreadTurnID(result)
	if turnErr != nil || turnID == "" {
		if syncDispatchAmbiguous(turnErr, turnID) {
			_ = s.syncWriter.MarkUnknown(lease)
			if receipt != nil {
				_ = s.store.MarkSyncDispatchFailure(ctx, *receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation)
			}
			_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchOutcomeUnknown, threadID, turnID, "dispatch_unknown", "Continuation outcome is unknown; this request will never be replayed automatically.")
		} else {
			abort()
			if receipt != nil {
				_ = s.store.MarkSyncDispatchFailure(ctx, *receipt, model.SyncReceiptRejected, model.SyncTurnTerminal, lease.Generation)
			}
			fail("dispatch_rejected", fmt.Sprintf("The original chat rejected the continuation: %v", turnErr))
		}
		return
	}
	s.boardContinuationLeases[threadID] = boardContinuationLease{Lease: lease, RequestID: request.ID, TurnID: turnID}
	if err := s.syncWriter.MarkActive(lease); err != nil {
		_ = s.syncWriter.MarkUncertain(lease)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchOutcomeUnknown, threadID, turnID, "ownership_unknown", "Continuation started but ownership confirmation failed; it will not be replayed.")
		return
	}
	if receipt != nil {
		if err := s.store.MarkSyncDispatchStateWithTelegramUser(ctx, *receipt, model.SyncReceiptDispatched, turnID, model.SyncTurnActive, lease.Generation, syncUserTextFingerprint(turnID, request.Prompt)); err != nil {
			_ = s.syncWriter.MarkUncertain(lease)
			_ = s.store.MarkSyncDispatchFailure(ctx, *receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation)
			_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchOutcomeUnknown, threadID, turnID, "receipt_unknown", "Continuation started but its Telegram receipt could not be confirmed; it will not be replayed.")
			return
		}
	}
	changed, err := s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted, threadID, turnID, "", "")
	if err != nil || !changed {
		_ = s.syncWriter.MarkUncertain(lease)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchOutcomeUnknown, threadID, turnID, "persistence_unknown", "Continuation started but persistence is uncertain; it will not be replayed.")
		return
	}
	s.markSyncThreadDirty(threadID)
	s.queueExternalReplyFromStoredSnapshot(ctx, threadID)
}
