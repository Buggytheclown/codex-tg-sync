package daemon

import (
	"context"
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
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return
	}
	if state.State != model.SyncStateActive || state.ChatID != s.cfg.SyncGroupID {
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
	permissions := appserver.ThreadStartOptions{
		ApprovalPolicy:    s.cfg.ExternalApprovalPolicy,
		ApprovalsReviewer: s.cfg.ExternalApprovalsReviewer,
		SandboxMode:       s.cfg.ExternalSandboxMode,
	}
	execution := appserver.TurnStartOptions{Model: request.Model, ReasoningEffort: request.ReasoningEffort}
	response, dispatchErr := s.startClaimedSyncDraftLocked(ctx, claimed, receipt, request.Prompt, telegramPreview, permissions, execution)
	storedReceipt, receiptErr := s.store.GetSyncReceipt(ctx, topicID, sourceMessageID)
	if response != nil && strings.TrimSpace(response.ThreadID) != "" && strings.TrimSpace(response.TurnID) != "" && dispatchErr == nil {
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted,
			response.ThreadID, response.TurnID, "", "")
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
