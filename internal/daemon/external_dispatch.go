package daemon

import (
	"context"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) dispatchExternalLaunchRequest(ctx context.Context, requestID string) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()

	request, err := s.store.GetExternalLaunchRequest(ctx, requestID)
	if err != nil || request == nil || request.Status != model.ExternalLaunchStarting {
		return
	}
	state, err := s.store.GetAFCState(ctx)
	if err != nil {
		return
	}
	if state.State != model.AFCStateActive || state.ChatID != s.cfg.AFCGroupID {
		summary := "AFC is inactive; enable AFC and press Start again."
		if request.AutoStart {
			summary = "AFC is inactive; the request will start automatically after AFC is enabled."
		}
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "afc_inactive", summary)
		s.finishExternalLaunchDispatch(ctx, request.AutoStart)
		return
	}
	forum := s.getAFCForum()
	if forum == nil {
		summary := "Telegram transport is unavailable; press Start to retry."
		if request.AutoStart {
			summary = "Telegram transport is unavailable; the request will retry automatically."
		}
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "telegram_unavailable", summary)
		s.finishExternalLaunchDispatch(ctx, request.AutoStart)
		return
	}

	telegramPreview := externalLaunchTelegramPreview(*request)
	title := afcPromptTopicTitle(telegramPreview)
	topicID, err := forum.CreateAFCTopic(ctx, title)
	if err != nil {
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "telegram_topic", "Telegram could not create the session topic.")
		s.finishExternalLaunchDispatch(ctx, request.AutoStart)
		return
	}
	topics, _ := s.store.ListAFCTopics(ctx, state.SessionID)
	drafts, _ := s.store.ListAFCTopicDrafts(ctx, state.SessionID)
	projectName, directoryName := model.ProjectNameFromCWD(request.CWD)
	draft := model.AFCTopicDraft{
		SessionID: state.SessionID, ChatID: state.ChatID, TopicID: topicID, Rank: len(topics) + len(drafts) + 1,
		Title: title, CWD: request.CWD, ProjectName: projectName, DirectoryName: directoryName,
	}
	if err := s.store.CreateAFCTopicDraft(ctx, draft); err != nil {
		_ = forum.DeleteAFCTopic(ctx, topicID)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "afc_draft", "AFC could not persist the new session topic.")
		s.finishExternalLaunchDispatch(ctx, request.AutoStart)
		return
	}
	sourceMessageID, err := forum.SendAFCMessage(ctx, topicID, model.RenderedMessage{Text: afcUserHeader + "\n" + telegramPreview}, true)
	if err != nil {
		_ = forum.DeleteAFCTopic(ctx, topicID)
		_ = s.store.DeleteAFCTopic(ctx, state.SessionID, topicID)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "telegram_prompt", "Telegram could not write the initial request into the session topic.")
		s.finishExternalLaunchDispatch(ctx, request.AutoStart)
		return
	}
	claimed, receipt, created, err := s.store.ClaimAFCTopicDraftMessage(ctx, state.ChatID, topicID, sourceMessageID)
	if err != nil || !created {
		_ = forum.DeleteAFCTopic(ctx, topicID)
		_ = s.store.DeleteAFCTopic(ctx, state.SessionID, topicID)
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchFailed, "", "", "afc_claim", "AFC could not claim the new session topic.")
		s.finishExternalLaunchDispatch(ctx, request.AutoStart)
		return
	}
	permissions := appserver.ThreadStartOptions{
		ApprovalPolicy:    s.cfg.ExternalApprovalPolicy,
		ApprovalsReviewer: s.cfg.ExternalApprovalsReviewer,
		SandboxMode:       s.cfg.ExternalSandboxMode,
	}
	response, dispatchErr := s.startClaimedAFCDraftLocked(ctx, claimed, receipt, request.Prompt, telegramPreview, permissions)
	storedReceipt, receiptErr := s.store.GetAFCReceipt(ctx, topicID, sourceMessageID)
	if response != nil && strings.TrimSpace(response.ThreadID) != "" && strings.TrimSpace(response.TurnID) != "" && dispatchErr == nil {
		_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, model.ExternalLaunchSessionStarted,
			response.ThreadID, response.TurnID, "", "")
		s.queueExternalReplyFromStoredSnapshot(ctx, response.ThreadID)
		s.finishExternalLaunchDispatch(ctx, request.AutoStart)
		return
	}

	threadID := ""
	if storedReceipt != nil {
		threadID = storedReceipt.ThreadID
	}
	status := model.ExternalLaunchOutcomeUnknown
	errorType := "dispatch_unknown"
	errorSummary := "AFC dispatch outcome is unknown; this request will not be replayed automatically."
	if receiptErr == nil && storedReceipt != nil && storedReceipt.State == model.AFCReceiptRejected && dispatchErr == nil {
		status = model.ExternalLaunchFailed
		errorType = "dispatch_rejected"
		errorSummary = "AFC rejected the request before its first turn could start."
		if response != nil && strings.TrimSpace(response.Text) != "" {
			errorSummary = strings.TrimSpace(response.Text)
		}
	}
	_, _ = s.store.CompleteExternalLaunchRequest(ctx, request.ID, status, threadID, "", errorType, errorSummary)
	if status == model.ExternalLaunchFailed {
		_ = forum.DeleteAFCTopic(ctx, topicID)
		_ = s.store.DeleteAFCTopic(ctx, state.SessionID, topicID)
	}
	s.finishExternalLaunchDispatch(ctx, request.AutoStart)
}

func (s *Service) finishExternalLaunchDispatch(ctx context.Context, autoStart bool) {
	if !autoStart {
		s.processExternalLaunchRequests(ctx)
		return
	}
	s.externalRequestMu.Lock()
	s.mu.RLock()
	sender := s.sender
	s.mu.RUnlock()
	s.renderExternalLaunchRequestsLocked(ctx, sender)
	s.externalRequestMu.Unlock()
}
