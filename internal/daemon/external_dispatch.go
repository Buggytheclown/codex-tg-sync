package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Service) dispatchExternalLaunchRequest(ctx context.Context, requestID string) {
	if _, loaded := s.externalDispatchInFlight.LoadOrStore(requestID, struct{}{}); loaded {
		return
	}
	defer s.externalDispatchInFlight.Delete(requestID)
	ctx = context.WithValue(ctx, syncExternalRequestKey{}, requestID)
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	s.taskLaunchMu.Lock()
	defer s.taskLaunchMu.Unlock()
	request, err := s.store.GetExternalLaunchRequest(ctx, requestID)
	if err != nil || request == nil || request.Status != model.ExternalLaunchStarting {
		return
	}
	defer s.finishExternalLaunchDispatch()
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return
	}
	// A previously admitted launch needs no Telegram IO or presentation readiness.
	if state.State != model.SyncStateActive || state.ChatID != s.cfg.SyncGroupID || state.SecurityState != model.SyncSecurityValid {
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, request.ID, "sync_inactive", continuationRetryText(*request, "Sync is inactive; enable Sync and press Start again.", "Sync is inactive; the request will start after Sync is enabled."))
		return
	}
	if request.DispatchPhase != model.ExternalDispatchNotSent && request.DispatchPhase != model.ExternalDispatchThreadReady {
		return
	}
	if request.TargetThreadID != "" || request.ThreadID != "" {
		s.dispatchExternalContinuationLocked(ctx, *request)
		return
	}
	permissions := appserver.ThreadStartOptions{ApprovalPolicy: frozenExternalSetting(request.ApprovalPolicy, s.cfg.ExternalApprovalPolicy), ApprovalsReviewer: frozenExternalSetting(request.ApprovalsReviewer, s.cfg.ExternalApprovalsReviewer), SandboxMode: frozenExternalSetting(request.SandboxMode, s.cfg.ExternalSandboxMode)}
	lease, err := s.syncWriter.ReserveProcess(ctx, "external:"+request.ID)
	if err != nil {
		s.failExternalDispatch(ctx, *request, "writer_unavailable", err, false)
		return
	}
	s.installSyncWriterLocked(lease)
	if err = s.store.SetExternalDispatchPhase(ctx, request.ID, model.ExternalDispatchNotSent, model.ExternalDispatchThreadSending); err != nil {
		_ = s.syncWriter.Abort(lease)
		s.failExternalDispatch(ctx, *request, "journal_persistence", err, false)
		return
	}
	rpcCtx := s.syncRPCContextLocked(ctx, state.SessionID)
	payload, startErr := lease.Process.ThreadStart(rpcCtx, request.CWD, permissions)
	if startErr != nil {
		unknown := syncDispatchAmbiguous(startErr, "")
		if unknown {
			_ = s.syncWriter.MarkUnknown(lease)
		} else {
			_ = s.syncWriter.Abort(lease)
			_ = s.store.SetExternalDispatchPhase(ctx, request.ID, model.ExternalDispatchThreadSending, model.ExternalDispatchNotSent)
		}
		s.failExternalDispatch(ctx, *request, "thread_start", startErr, unknown)
		return
	}
	project, directory := model.ProjectNameFromCWD(request.CWD)
	thread := threadFromStartPayload(payload, pendingNewThreadState{CWD: request.CWD, ProjectName: project, DirectoryName: directory})
	if thread.ID == "" {
		_ = s.syncWriter.MarkUnknown(lease)
		s.failExternalDispatch(ctx, *request, "thread_start", fmt.Errorf("thread/start returned no id"), true)
		return
	}
	request.ThreadID = thread.ID
	lease, err = s.syncWriter.ClaimThread(lease, thread.ID)
	if err != nil {
		_ = s.syncWriter.MarkUnknown(lease)
		s.failExternalDispatch(ctx, *request, "ownership_unknown", err, true)
		return
	}
	s.syncLeases[thread.ID] = lease
	s.installSyncWriterLocked(lease)
	if err = s.store.RecordExternalThread(ctx, request.ID, thread.ID); err != nil {
		s.abortExternalLease(ctx, lease)
		s.failExternalDispatch(ctx, *request, "thread_persistence", err, true)
		return
	}
	request.DispatchPhase = model.ExternalDispatchThreadReady
	thread.Title = syncPromptTopicTitle(externalLaunchTelegramPreview(*request))
	if err = s.store.UpsertThread(ctx, thread); err != nil {
		s.abortExternalLease(ctx, lease)
		s.failExternalDispatch(ctx, *request, "thread_persistence", err, false)
		return
	}
	s.startExternalTurnLocked(rpcCtx, *request, thread, lease)
}

func (s *Service) abortExternalLease(ctx context.Context, lease appserver.WriterLease[Session]) {
	delete(s.syncLeases, lease.ThreadID)
	delete(s.boardContinuationLeases, lease.ThreadID)
	s.unsubscribeSyncWriterLeaseLocked(ctx, lease)
	_ = s.syncWriter.Abort(lease)
}
func (s *Service) failExternalDispatch(ctx context.Context, r model.ExternalLaunchRequest, kind string, err error, unknown bool) {
	status := model.ExternalLaunchFailed
	if unknown {
		status = model.ExternalLaunchOutcomeUnknown
	}
	_, _ = s.store.CompleteExternalLaunchRequest(ctx, r.ID, status, r.ThreadID, r.TurnID, kind, sanitizeDiagnosticString(err.Error()))
}
func (s *Service) finishExternalLaunchDispatch() {
	s.wakeExternalLaunchRenderer()
	s.wakeSyncReconcile()
}
func frozenExternalSetting(value *string, fallback string) string {
	if value != nil {
		return *value
	}
	return fallback
}

func (s *Service) dispatchExternalContinuationLocked(ctx context.Context, r model.ExternalLaunchRequest) {
	threadID := strings.TrimSpace(r.ThreadID)
	if threadID == "" {
		threadID = strings.TrimSpace(r.TargetThreadID)
	}
	if r.TargetThreadID != "" && threadID != r.TargetThreadID {
		s.failExternalDispatch(ctx, r, "target_thread", fmt.Errorf("continuation target changed"), false)
		return
	}
	state, err := s.store.GetSyncState(ctx)
	if err != nil {
		return
	}
	ctx = s.syncRPCContextLocked(ctx, state.SessionID)
	busy := func() {
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, r.ID, "thread_busy", continuationRetryText(r, "The target chat is busy; press Start again when it is idle.", "The target chat is busy; the prompt will start automatically when it is idle."))
	}
	s.mu.RLock()
	poll, connected := s.poll, s.pollConnected
	s.mu.RUnlock()
	if poll == nil || !connected {
		_, _ = s.store.ResetExternalLaunchRequestPending(ctx, r.ID, "appserver_unavailable", "App Server is unavailable; press Start after connectivity returns.")
		return
	}
	current, err := readAuthoritativeSyncSnapshot(ctx, poll, threadID)
	if err != nil {
		s.failExternalDispatch(ctx, r, "target_read", err, false)
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
	s.syncLeases[threadID] = lease
	s.installSyncWriterLocked(lease)
	if _, err = lease.Process.ThreadResume(ctx, threadID, r.CWD); err != nil {
		s.abortExternalLease(ctx, lease)
		s.failExternalDispatch(ctx, r, "target_resume", err, false)
		return
	}
	current, err = readAuthoritativeSyncSnapshot(ctx, lease.Process, threadID)
	if err != nil {
		s.abortExternalLease(ctx, lease)
		s.failExternalDispatch(ctx, r, "target_read", err, false)
		return
	}
	if activeTurnIDFromSyncSnapshot(current) != "" || threadLooksActiveForInput(&current.Thread) {
		s.abortExternalLease(ctx, lease)
		busy()
		return
	}
	r.ThreadID = threadID
	if err = s.store.RecordExternalThread(ctx, r.ID, threadID); err != nil {
		s.abortExternalLease(ctx, lease)
		s.failExternalDispatch(ctx, r, "thread_persistence", err, false)
		return
	}
	if r.CWD == "" {
		r.CWD = current.Thread.CWD
	}
	r.DispatchPhase = model.ExternalDispatchThreadReady
	s.startExternalTurnLocked(ctx, r, current.Thread, lease)
}

func (s *Service) startExternalTurnLocked(ctx context.Context, r model.ExternalLaunchRequest, thread model.Thread, lease appserver.WriterLease[Session]) {
	if err := s.store.SetExternalDispatchPhase(ctx, r.ID, model.ExternalDispatchThreadReady, model.ExternalDispatchTurnSending); err != nil {
		s.abortExternalLease(ctx, lease)
		s.failExternalDispatch(ctx, r, "journal_persistence", err, false)
		return
	}
	s.boardContinuationLeases[r.ThreadID] = boardContinuationLease{Lease: lease, RequestID: r.ID}
	options := s.turnStartOptions(ctx, "", &thread)
	options.Model = r.Model
	options.ReasoningEffort = r.ReasoningEffort
	options.ApprovalPolicy = frozenExternalSetting(r.ApprovalPolicy, s.cfg.ExternalApprovalPolicy)
	options.ApprovalsReviewer = frozenExternalSetting(r.ApprovalsReviewer, s.cfg.ExternalApprovalsReviewer)
	options.SandboxMode = frozenExternalSetting(r.SandboxMode, s.cfg.ExternalSandboxMode)
	result, err := lease.Process.TurnStart(ctx, r.ThreadID, r.Prompt, r.CWD, options)
	turnID := appserverThreadTurnID(result)
	if err != nil || turnID == "" {
		unknown := syncDispatchAmbiguous(err, turnID)
		if unknown {
			_ = s.syncWriter.MarkUnknown(lease)
		} else {
			s.abortExternalLease(ctx, lease)
			_ = s.store.SetExternalDispatchPhase(ctx, r.ID, model.ExternalDispatchTurnSending, model.ExternalDispatchThreadReady)
		}
		if err == nil {
			err = fmt.Errorf("turn/start returned no id")
		}
		s.failExternalDispatch(ctx, r, "dispatch_rejected", err, unknown)
		return
	}
	r.TurnID = turnID
	s.boardContinuationLeases[r.ThreadID] = boardContinuationLease{Lease: lease, RequestID: r.ID, TurnID: turnID}
	if err = s.store.RecordExternalTurn(ctx, r.ID, r.ThreadID, turnID); err != nil {
		_ = s.syncWriter.MarkUncertain(lease)
		s.failExternalDispatch(ctx, r, "persistence_unknown", err, true)
		return
	}
	_ = s.markTelegramOriginTurn(ctx, r.ThreadID, turnID)
	if err = s.syncWriter.MarkActive(lease); err != nil {
		_ = s.syncWriter.MarkUncertain(lease)
		s.logLifecycle("external_ownership_confirmation_failed", lifecycleFields{"request_id": r.ID, "thread_id": r.ThreadID, "turn_id": turnID, "error": err})
	}
	if _, err = s.store.CompleteExternalLaunchRequest(ctx, r.ID, model.ExternalLaunchSessionStarted, r.ThreadID, turnID, "", ""); err != nil {
		_ = s.syncWriter.MarkUncertain(lease)
		s.logLifecycle("external_completion_persistence_failed", lifecycleFields{"request_id": r.ID, "thread_id": r.ThreadID, "turn_id": turnID, "error": err})
		return
	}
	state, _ := s.store.GetSyncState(ctx)
	s.attachExternalTopicOwnerLocked(ctx, state.SessionID, r.ThreadID)
	s.ensureStartedTurnSnapshot(ctx, &thread, turnID)
	s.enqueueSyncDelivery(r.ThreadID)
	s.markSyncThreadDirty(r.ThreadID)
	s.queueExternalReplyFromStoredSnapshot(ctx, r.ThreadID)
}
func continuationRetryText(r model.ExternalLaunchRequest, manual, auto string) string {
	if r.AutoStart {
		return auto
	}
	return manual
}
