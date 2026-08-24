package daemon

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const afcPromptTitleMaxRunes = 72

func (s *Service) handleAFCDraftMessage(ctx context.Context, draft model.AFCTopicDraft, messageID int64, text string) (*DirectResponse, error) {
	if text == "" {
		return &DirectResponse{Text: "AFC topic requires a non-empty plain-text prompt."}, nil
	}
	if strings.HasPrefix(text, "/") {
		return &DirectResponse{Text: "This AFC draft accepts its first plain-text prompt. Use lifecycle commands in Control."}, nil
	}
	return s.dispatchAFCDraftMessage(ctx, draft, messageID, text)
}

func (s *Service) dispatchAFCDraftMessage(ctx context.Context, draft model.AFCTopicDraft, messageID int64, text string) (*DirectResponse, error) {
	s.afcMu.Lock()
	defer s.afcMu.Unlock()
	claimed, receipt, created, err := s.store.ClaimAFCTopicDraftMessage(ctx, draft.ChatID, draft.TopicID, messageID)
	if err != nil {
		if claimed.State == model.AFCDraftStarting || claimed.State == model.AFCDraftUnknown {
			return &DirectResponse{Text: "This AFC draft is already starting or ownership-unknown. The new message was not queued."}, nil
		}
		return nil, err
	}
	if !created {
		return afcDuplicateReceiptResponse(receipt), nil
	}
	return s.startClaimedAFCDraftLocked(ctx, claimed, receipt, text, text, appserver.ThreadStartOptions{}, appserver.TurnStartOptions{})
}

func (s *Service) startClaimedAFCDraftLocked(ctx context.Context, draft model.AFCTopicDraft, receipt model.AFCMessageReceipt, text, titleText string, permissions appserver.ThreadStartOptions, execution appserver.TurnStartOptions) (*DirectResponse, error) {
	lease, err := s.afcWriter.ReserveProcess(ctx, "draft:"+randomToken())
	if err != nil {
		_ = s.store.ResetAFCTopicDraftMessage(ctx, draft, receipt)
		return &DirectResponse{Text: fmt.Sprintf("AFC could not reserve the shared writer; this message was not dispatched: %v", err)}, nil
	}
	s.installAFCWriterLocked(lease)
	threadPayload, startErr := lease.Process.ThreadStart(ctx, draft.CWD, permissions)
	if startErr != nil {
		if afcDispatchAmbiguous(startErr, "") {
			_ = s.afcWriter.MarkUnknown(lease)
			_ = s.store.MarkAFCTopicDraftUnknown(ctx, draft, receipt)
			return &DirectResponse{Text: "AFC thread creation outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		_ = s.afcWriter.Abort(lease)
		_ = s.store.ResetAFCTopicDraftMessage(ctx, draft, receipt)
		return &DirectResponse{Text: fmt.Sprintf("AFC rejected thread creation; send a new message to retry: %v", startErr)}, nil
	}
	state := pendingNewThreadState{ProjectName: draft.ProjectName, DirectoryName: draft.DirectoryName, CWD: draft.CWD}
	thread := threadFromStartPayload(threadPayload, state)
	if strings.TrimSpace(thread.ID) == "" {
		_ = s.afcWriter.MarkUnknown(lease)
		_ = s.store.MarkAFCTopicDraftUnknown(ctx, draft, receipt)
		return &DirectResponse{Text: "AFC thread creation returned no thread id; ownership is unknown."}, nil
	}
	lease, err = s.afcWriter.ClaimThread(lease, thread.ID)
	if err != nil {
		_ = s.afcWriter.MarkUnknown(lease)
		_ = s.store.MarkAFCTopicDraftUnknown(ctx, draft, receipt)
		return &DirectResponse{Text: fmt.Sprintf("AFC created a thread but could not claim it; ownership is unknown: %v", err)}, nil
	}
	title := afcPromptTopicTitle(titleText)
	thread.Title = title
	if err := s.store.UpsertThread(ctx, thread); err != nil {
		_ = s.afcWriter.MarkUnknown(lease)
		_ = s.store.MarkAFCTopicDraftUnknown(ctx, draft, receipt)
		return nil, err
	}
	if err := s.store.MaterializeAFCTopicDraft(ctx, draft, receipt, thread.ID, draft.Title, lease.Generation); err != nil {
		_ = s.afcWriter.MarkUnknown(lease)
		_ = s.store.MarkAFCTopicDraftUnknown(ctx, draft, receipt)
		return nil, err
	}
	receipt.ThreadID = thread.ID
	s.afcLeases[thread.ID] = lease
	s.installAFCWriterLocked(lease)
	if forum := s.getAFCForum(); forum != nil {
		if forum.RenameAFCTopic(ctx, draft.TopicID, title) == nil {
			_ = s.store.UpdateAFCTopicTitle(ctx, draft.SessionID, draft.TopicID, title)
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
		if afcDispatchAmbiguous(turnErr, turnID) {
			_ = s.afcWriter.MarkUnknown(lease)
			_ = s.store.MarkAFCDispatchFailure(ctx, receipt, model.AFCReceiptUnknown, model.AFCTurnUnknown, lease.Generation)
			return &DirectResponse{Text: "AFC first-turn outcome is unknown. This Telegram message will never be replayed automatically."}, nil
		}
		delete(s.afcLeases, thread.ID)
		_ = s.afcWriter.Abort(lease)
		if err := s.store.DematerializeAFCTopicDraft(ctx, draft, receipt, thread.ID, title, lease.Generation); err != nil {
			return nil, err
		}
		return &DirectResponse{Text: fmt.Sprintf("AFC rejected the first prompt; send a new message to retry: %v", turnErr)}, nil
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
	_ = s.markTelegramOriginTurnFromTelegram(ctx, thread.ID, turnID, draft.ChatID, draft.TopicID)
	s.ensureStartedTurnSnapshot(ctx, &thread, turnID)
	s.startAFCTelegramOriginHotPoll(ctx, thread.ID, turnID)
	return &DirectResponse{Text: fmt.Sprintf("AFC turn started: %s", turnID), ThreadID: thread.ID, TurnID: turnID}, nil
}

func afcPromptTopicTitle(text string) string {
	title := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if title == "" {
		return "New task"
	}
	if utf8.RuneCountInString(title) <= afcPromptTitleMaxRunes {
		return title
	}
	runes := []rune(title)
	short := strings.TrimSpace(string(runes[:afcPromptTitleMaxRunes]))
	if cut := strings.LastIndexByte(short, ' '); cut >= afcPromptTitleMaxRunes/2 {
		short = strings.TrimSpace(short[:cut])
	}
	return strings.TrimRight(short, ".,;:!?") + "…"
}
