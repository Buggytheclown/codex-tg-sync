package daemon

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const syncPromptTitleMaxRunes = 72

func (s *Service) handleSyncDraftMessage(ctx context.Context, draft model.SyncTopicDraft, messageID int64, text string) (*DirectResponse, error) {
	if text == "" {
		return &DirectResponse{Text: "Sync topic requires a non-empty plain-text prompt."}, nil
	}
	if strings.HasPrefix(text, "/") {
		return &DirectResponse{Text: "This Sync draft accepts its first plain-text prompt. Use lifecycle commands in Control."}, nil
	}
	return s.dispatchSyncDraftMessage(ctx, draft, messageID, text)
}

func (s *Service) dispatchSyncDraftMessage(ctx context.Context, draft model.SyncTopicDraft, messageID int64, text string) (*DirectResponse, error) {
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	claimed, receipt, created, err := s.store.ClaimSyncTopicDraftMessage(ctx, draft.ChatID, draft.TopicID, messageID)
	if err != nil {
		if claimed.State == model.SyncDraftStarting || claimed.State == model.SyncDraftUnknown {
			return &DirectResponse{Text: "This Sync draft is already starting or ownership-unknown. The new message was not queued."}, nil
		}
		return nil, err
	}
	if !created {
		return syncDuplicateReceiptResponse(receipt), nil
	}
	return s.startClaimedSyncDraftLocked(ctx, claimed, receipt, text, text, telegramThreadStartOptions(), appserver.TurnStartOptions{})
}

func (s *Service) startClaimedSyncDraftLocked(ctx context.Context, draft model.SyncTopicDraft, receipt model.SyncMessageReceipt, text, titleText string, permissions appserver.ThreadStartOptions, execution appserver.TurnStartOptions) (*DirectResponse, error) {
	lease, err := s.syncWriter.ReserveProcess(ctx, "draft:"+randomToken())
	if err != nil {
		_ = s.store.ResetSyncTopicDraftMessage(ctx, draft, receipt)
		return &DirectResponse{Text: fmt.Sprintf("Sync could not reserve the shared writer; this message was not dispatched: %v", err)}, nil
	}
	s.installSyncWriterLocked(lease)
	threadPayload, startErr := lease.Process.ThreadStart(ctx, draft.CWD, permissions)
	if startErr != nil {
		if syncDispatchAmbiguous(startErr, "") {
			_ = s.syncWriter.MarkUnknown(lease)
			_ = s.store.MarkSyncTopicDraftUnknown(ctx, draft, receipt)
			return &DirectResponse{Text: "Sync thread creation outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		_ = s.syncWriter.Abort(lease)
		_ = s.store.ResetSyncTopicDraftMessage(ctx, draft, receipt)
		return &DirectResponse{Text: fmt.Sprintf("Sync rejected thread creation; send a new message to retry: %v", startErr)}, nil
	}
	state := pendingNewThreadState{ProjectName: draft.ProjectName, DirectoryName: draft.DirectoryName, CWD: draft.CWD}
	thread := threadFromStartPayload(threadPayload, state)
	if strings.TrimSpace(thread.ID) == "" {
		_ = s.syncWriter.MarkUnknown(lease)
		_ = s.store.MarkSyncTopicDraftUnknown(ctx, draft, receipt)
		return &DirectResponse{Text: "Sync thread creation returned no thread id; ownership is unknown."}, nil
	}
	lease, err = s.syncWriter.ClaimThread(lease, thread.ID)
	if err != nil {
		_ = s.syncWriter.MarkUnknown(lease)
		_ = s.store.MarkSyncTopicDraftUnknown(ctx, draft, receipt)
		return &DirectResponse{Text: fmt.Sprintf("Sync created a thread but could not claim it; ownership is unknown: %v", err)}, nil
	}
	title := syncPromptTopicTitle(titleText)
	thread.Title = title
	if err := s.store.UpsertThread(ctx, thread); err != nil {
		_ = s.syncWriter.MarkUnknown(lease)
		_ = s.store.MarkSyncTopicDraftUnknown(ctx, draft, receipt)
		return nil, err
	}
	if err := s.store.MaterializeSyncTopicDraft(ctx, draft, receipt, thread.ID, draft.Title, lease.Generation); err != nil {
		_ = s.syncWriter.MarkUnknown(lease)
		_ = s.store.MarkSyncTopicDraftUnknown(ctx, draft, receipt)
		return nil, err
	}
	receipt.ThreadID = thread.ID
	s.syncLeases[thread.ID] = lease
	s.installSyncWriterLocked(lease)
	if forum := s.getSyncForum(); forum != nil {
		if forum.RenameSyncTopic(ctx, draft.TopicID, title) == nil {
			_ = s.store.UpdateSyncTopicTitle(ctx, draft.SessionID, draft.TopicID, title)
		}
	}
	if namer, ok := any(lease.Process).(interface {
		ThreadSetName(context.Context, string, string) (map[string]any, error)
	}); ok {
		_, _ = namer.ThreadSetName(ctx, thread.ID, title)
	}
	turnOptions := s.turnStartOptions(ctx, "", &thread)
	turnOptions.ApprovalPolicy = permissions.ApprovalPolicy
	turnOptions.ApprovalsReviewer = permissions.ApprovalsReviewer
	turnOptions.SandboxMode = permissions.SandboxMode
	if strings.TrimSpace(execution.Model) != "" || strings.TrimSpace(execution.ReasoningEffort) != "" {
		turnOptions.CollaborationMode = collaborationModeDefault
		turnOptions.Model = strings.TrimSpace(execution.Model)
		turnOptions.ReasoningEffort = strings.TrimSpace(execution.ReasoningEffort)
	}
	result, turnErr := lease.Process.TurnStart(ctx, thread.ID, text, thread.CWD, turnOptions)
	turnID := appserverThreadTurnID(result)
	if turnErr != nil || strings.TrimSpace(turnID) == "" {
		if syncDispatchAmbiguous(turnErr, turnID) {
			_ = s.syncWriter.MarkUnknown(lease)
			_ = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation)
			return &DirectResponse{Text: "Sync first-turn outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		delete(s.syncLeases, thread.ID)
		_ = s.syncWriter.Abort(lease)
		if err := s.store.DematerializeSyncTopicDraft(ctx, draft, receipt, thread.ID, title, lease.Generation); err != nil {
			return nil, err
		}
		return &DirectResponse{Text: fmt.Sprintf("Sync rejected the first prompt; send a new message to retry: %v", turnErr)}, nil
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
	_ = s.markTelegramOriginTurnFromTelegram(ctx, thread.ID, turnID, draft.ChatID, draft.TopicID)
	s.ensureStartedTurnSnapshot(ctx, &thread, turnID)
	s.startSyncTelegramOriginHotPoll(ctx, thread.ID, turnID)
	return &DirectResponse{Text: fmt.Sprintf("Sync turn started: %s", turnID), ThreadID: thread.ID, TurnID: turnID}, nil
}

func syncPromptTopicTitle(text string) string {
	title := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if title == "" {
		return "New task"
	}
	if utf8.RuneCountInString(title) <= syncPromptTitleMaxRunes {
		return title
	}
	runes := []rune(title)
	short := strings.TrimSpace(string(runes[:syncPromptTitleMaxRunes]))
	if cut := strings.LastIndexByte(short, ' '); cut >= syncPromptTitleMaxRunes/2 {
		short = strings.TrimSpace(short[:cut])
	}
	return strings.TrimRight(short, ".,;:!?") + "…"
}
