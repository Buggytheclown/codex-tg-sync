package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/control"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

type Session = control.RuntimeSession

type Sender interface {
	SendMessage(ctx context.Context, chatID, topicID int64, text string, buttons [][]model.ButtonSpec, options model.SendOptions) (int64, error)
	SendRenderedMessages(ctx context.Context, chatID, topicID int64, messages []model.RenderedMessage, buttons [][]model.ButtonSpec, options model.SendOptions) ([]int64, error)
	EditMessage(ctx context.Context, chatID, topicID, messageID int64, text string, buttons [][]model.ButtonSpec) error
	EditRenderedMessage(ctx context.Context, chatID, topicID, messageID int64, rendered model.RenderedMessage, buttons [][]model.ButtonSpec) error
	DeleteMessage(ctx context.Context, chatID, topicID, messageID int64) error
	SendDocumentData(ctx context.Context, chatID, topicID int64, fileName string, data []byte, caption string, options model.SendOptions) (int64, error)
}

type ExternalReplySender interface {
	SendExternalReply(ctx context.Context, chatID string, replyMessageID, threadID int64, text string) (int64, error)
}

type DirectResponse struct {
	Text         string
	CallbackText string
	Buttons      [][]model.ButtonSpec
	ThreadID     string
	TurnID       string
	ItemID       string
	EventID      string
}

func silentSendOptions() model.SendOptions {
	return model.SendOptions{Silent: true}
}

func notifySendOptions() model.SendOptions {
	return model.SendOptions{}
}

type Service struct {
	cfg   config.Config
	store *storage.Store

	liveFactory  func() Session
	pollFactory  func() Session
	threadClaims *appserver.ThreadClaimRegistry
	syncWriter   *appserver.WriterManager[Session]

	sessionMu                    sync.Mutex
	mu                           sync.RWMutex
	poll                         Session
	runCtx                       context.Context
	pollGeneration               uint64
	cancel                       context.CancelFunc
	wg                           sync.WaitGroup
	externalRequestMu            sync.Mutex
	externalLaunchWake           chan struct{}
	syncMu                       sync.Mutex
	syncForum                    SyncForum
	syncLeases                   map[string]appserver.WriterLease[Session]
	syncEventProcess             Session
	syncEventGeneration          uint64
	syncEventCancel              context.CancelFunc
	syncSubscribedPollGeneration uint64
	syncSubscribedThreads        map[string]struct{}
	syncPollEventProcess         Session
	syncPollEventGeneration      uint64
	syncPollEventCancel          context.CancelFunc
	syncReconcileWake            chan struct{}
	syncDeliveryMu               sync.Mutex
	syncDeliveryJobs             chan string
	syncFinalJobs                chan syncDeliveryJob
	syncDeliveryPending          map[string][]syncDeliveryJob
	syncDeliveryQueued           map[string]bool
	sender                       Sender
	externalReplySender          ExternalReplySender
	logger                       *log.Logger
	diagnosticMu                 sync.Mutex
	healthMu                     sync.Mutex
	diagnosticWin                time.Time
	diagnosticN                  int
	diagnosticBy                 map[string]int
	diagnosticLast               map[string]time.Time
	now                          func() time.Time
	started                      bool
	startedAt                    time.Time
	ready                        bool
	phase                        string
	lastError                    string
	pollConnected                bool
	startupCleanupSessionID      string
	startupFinished              bool
	startupDone                  chan struct{}
	lastPollHeartbeat            time.Time
}

const (
	syncRecentThreadLimit     = 50
	collaborationModeDefault  = "default"
	telegramOriginHotPollMax  = 75 * time.Second
	telegramOriginHotPollTick = 3 * time.Second
)

var (
	codexThreadIDPattern        = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	codexThreadIDExtractPattern = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
)

func New(cfg config.Config) (*Service, error) {
	if err := cfg.Paths.Ensure(); err != nil {
		return nil, err
	}
	store, err := storage.Open(cfg.Paths.DBPath)
	if err != nil {
		return nil, err
	}
	service := &Service{
		cfg:                 cfg,
		store:               store,
		externalLaunchWake:  make(chan struct{}, 1),
		syncReconcileWake:   make(chan struct{}, 1),
		syncDeliveryJobs:    make(chan string, 256),
		syncFinalJobs:       make(chan syncDeliveryJob, 256),
		syncDeliveryPending: map[string][]syncDeliveryJob{},
		syncDeliveryQueued:  map[string]bool{},
		logger:              discardDiagnosticLogger(),
		diagnosticBy:        map[string]int{},
		diagnosticLast:      map[string]time.Time{},
		now:                 time.Now,
		phase:               "created",
	}
	transport := appserver.TransportConfig{
		Mode:       appserver.TransportMode(cfg.AppServerMode),
		ListenURL:  cfg.AppServerListen,
		SocketPath: cfg.AppServerSocket,
	}
	service.liveFactory = func() Session {
		return appserver.NewClientWithTransport(cfg.CodexBin, transport, cfg.DefaultCWD, cfg.RequestTimeout)
	}
	service.pollFactory = func() Session {
		return appserver.NewClientWithTransport(cfg.CodexBin, transport, cfg.DefaultCWD, cfg.RequestTimeout)
	}
	service.threadClaims = appserver.NewThreadClaimRegistry()
	service.syncWriter = appserver.NewWriterManager("sync", service.threadClaims, func() (Session, error) {
		return service.liveFactory(), nil
	})
	service.syncLeases = map[string]appserver.WriterLease[Session]{}
	service.syncSubscribedThreads = map[string]struct{}{}
	service.poll = service.pollFactory()
	return service, nil
}

func (s *Service) usesSharedAppServer() bool {
	return appserver.IsSharedTransportMode(s.cfg.AppServerMode)
}

func (s *Service) Close() error {
	s.mu.Lock()
	cancel := s.cancel
	started := s.started
	s.started = false
	s.cancel = nil
	s.mu.Unlock()
	if started && cancel != nil {
		cancel()
	}
	s.wg.Wait()
	s.syncMu.Lock()
	syncEventCancel := s.syncEventCancel
	syncPollEventCancel := s.syncPollEventCancel
	s.syncEventCancel = nil
	s.syncPollEventCancel = nil
	s.syncMu.Unlock()
	if syncEventCancel != nil {
		syncEventCancel()
	}
	if syncPollEventCancel != nil {
		syncPollEventCancel()
	}
	var syncCloseErr error
	if s.syncWriter != nil {
		syncCloseErr = s.syncWriter.ForceClose()
	}
	s.sessionMu.Lock()
	s.mu.Lock()
	poll := s.poll
	s.poll = nil
	s.runCtx = nil
	s.pollConnected = false
	s.pollGeneration++
	s.mu.Unlock()
	s.sessionMu.Unlock()
	if poll != nil {
		_ = poll.Close()
	}
	return errors.Join(syncCloseErr, s.store.Close())
}

func (s *Service) SetSender(sender Sender) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sender = sender
}

func (s *Service) SetExternalReplySender(sender ExternalReplySender) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.externalReplySender = sender
}

func (s *Service) SetSyncForum(forum SyncForum) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.syncForum = forum
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.runCtx = runCtx
	s.started = true
	s.startedAt = time.Now().UTC()
	s.ready = true
	s.phase = "ready"
	s.lastError = ""
	s.pollConnected = false
	s.startupCleanupSessionID = ""
	s.startupFinished = false
	s.startupDone = make(chan struct{})
	s.mu.Unlock()

	_ = s.store.SetState(runCtx, "daemon.phase", "ready")
	_ = s.store.SetState(runCtx, "daemon.ready", "true")
	_ = s.store.SetState(runCtx, "daemon.started_at", s.startedAt.Format(time.RFC3339Nano))
	_ = s.store.SetState(runCtx, "daemon.last_error", "")
	_ = s.store.DeleteState(runCtx, "appserver.live_connected")
	_ = s.store.SetState(runCtx, "appserver.poll_connected", "false")
	repairResetErr := s.resetRepairRequestOnStartup(runCtx)
	_, deliveryRetirementErr := s.store.RetireUnsupportedTelegramDeliveries(runCtx, s.cfg.SyncGroupID)
	_, recoveryErr := s.store.RecoverStartingExternalLaunchRequests(runCtx)
	_, terminalRecoveryErr := s.reconcileStoredExternalLaunchTerminals(runCtx)
	_, actionCardRecoveryErr := s.store.RefreshExternalLaunchActionCards(runCtx)
	_, ackRecoveryErr := s.store.RecoverSendingExternalAcks(runCtx)
	_, replyRecoveryErr := s.store.RecoverSendingExternalReplies(runCtx)
	cleanupSessionID, resetErr := s.store.ResetSyncOnStartup(runCtx)
	err := errors.Join(repairResetErr, deliveryRetirementErr, recoveryErr, terminalRecoveryErr, actionCardRecoveryErr, ackRecoveryErr, replyRecoveryErr, resetErr)
	if err != nil {
		cancel()
		s.mu.Lock()
		s.started = false
		s.cancel = nil
		s.runCtx = nil
		s.ready = false
		s.phase = "startup_failed"
		s.lastError = sanitizeDiagnosticString(err.Error())
		s.mu.Unlock()
		_ = s.store.SetState(context.Background(), "daemon.phase", "startup_failed")
		_ = s.store.SetState(context.Background(), "daemon.ready", "false")
		_ = s.store.SetState(context.Background(), "daemon.last_error", sanitizeDiagnosticString(err.Error()))
		return fmt.Errorf("recover durable startup state: %w", err)
	}
	s.mu.Lock()
	s.startupCleanupSessionID = cleanupSessionID
	s.mu.Unlock()
	s.spawn(runCtx, s.ensureSessions)
	s.spawn(runCtx, s.indexLoop)
	s.spawn(runCtx, s.pollLoop)
	s.spawn(runCtx, s.externalLaunchLoop)
	s.spawn(runCtx, s.externalReplyLoop)
	s.spawn(runCtx, s.telegramDeliveryLoop)
	s.spawn(runCtx, s.syncDeliveryLoop)
	s.spawn(runCtx, s.syncDeliveryLoop)
	s.spawn(runCtx, s.syncFinalDeliveryLoop)
	s.spawn(runCtx, s.controlLoop)
	return nil
}

func (s *Service) Doctor(ctx context.Context) (map[string]any, error) {
	backlog, _ := s.store.DeliveryQueueBacklog(ctx)
	externalReplyBacklog, _ := s.store.ExternalReplyBacklog(ctx)
	state, _ := s.store.ListState(ctx)
	return map[string]any{
		"config":                 s.cfg,
		"delivery_backlog":       backlog,
		"external_reply_backlog": externalReplyBacklog,
		"daemon_state":           state,
	}, nil
}

func (s *Service) ControlThreadList(ctx context.Context, limit int, cursor string) (map[string]any, error) {
	session, err := s.controlReadSession(ctx)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	return session.ThreadList(ctx, limit, cursor)
}

func (s *Service) ControlThreadRead(ctx context.Context, threadID string, includeTurns bool) (map[string]any, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil, errors.New("thread id is required")
	}
	session, err := s.controlReadSession(ctx)
	if err != nil {
		return nil, err
	}
	return session.ThreadRead(ctx, threadID, includeTurns)
}

func (s *Service) controlReadSession(ctx context.Context) (Session, error) {
	s.ensurePollSession(ctx)
	s.mu.RLock()
	session := s.poll
	s.mu.RUnlock()
	if session == nil {
		return nil, errors.New("app-server poll session unavailable")
	}
	return session, nil
}

func (s *Service) HandleMessage(ctx context.Context, chatID, topicID, userID int64, text string, replyToMessageID int64) (*DirectResponse, error) {
	return s.HandleMessageWithID(ctx, chatID, topicID, 0, userID, text, replyToMessageID)
}

func (s *Service) HandleMessageWithID(ctx context.Context, chatID, topicID, messageID, userID int64, text string, replyToMessageID int64) (*DirectResponse, error) {
	if !s.IsAllowed(userID, chatID) {
		return nil, nil
	}
	return s.handleSyncMessage(ctx, topicID, messageID, userID, text)
}

func (s *Service) HandleCallback(ctx context.Context, chatID, topicID, messageID, userID int64, token string) (*DirectResponse, error) {
	if !s.IsAllowed(userID, chatID) {
		return nil, nil
	}
	route, err := s.store.GetCallbackRoute(ctx, token)
	if err != nil {
		return nil, err
	}
	if route != nil && strings.HasPrefix(route.Action, "external_launch_") {
		return s.handleExternalLaunchCallback(ctx, chatID, topicID, messageID, route)
	}
	return s.handleSyncCallback(ctx, topicID, messageID, token)
}

func (s *Service) RegisterDirectDelivery(ctx context.Context, chatID, topicID, messageID int64, response *DirectResponse) error {
	if response != nil {
		s.reanchorSyncDirectDelivery(ctx, chatID, topicID, response)
	}
	return nil
}

func (s *Service) RequestRepair(ctx context.Context, reason string) error {
	if strings.TrimSpace(reason) == "" {
		reason = "manual"
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_ = s.store.SetState(ctx, "repair.last_reason", reason)
	_ = s.store.SetState(ctx, "repair.last_at", now)
	s.logLifecycle("repair_requested", lifecycleFields{"reason": reason})
	return s.store.SetState(ctx, "control.repair_request", fmt.Sprintf("%s|%s", now, reason))
}

func (s *Service) resetRepairRequestOnStartup(ctx context.Context) error {
	return s.store.SetState(ctx, "control.repair_request", "")
}

func (s *Service) IsAllowed(userID, chatID int64) bool {
	return s.isSyncGroup(chatID) && len(s.cfg.AllowedUserIDs) == 1 && s.cfg.AllowedUserIDs[0] == userID
}

func (s *Service) spawn(ctx context.Context, fn func(context.Context)) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		fn(ctx)
	}()
}

func (s *Service) ensureSessions(ctx context.Context) {
	s.ensurePollSession(ctx)
}

func (s *Service) ensurePollSession(ctx context.Context) {
	s.sessionMu.Lock()
	defer s.sessionMu.Unlock()
	s.ensurePollSessionLocked(ctx)
}

func (s *Service) ensurePollSessionLocked(ctx context.Context) {
	s.mu.RLock()
	client := s.poll
	connected := s.pollConnected
	s.mu.RUnlock()
	if client == nil || connected {
		return
	}
	sessionCtx, cancel := context.WithTimeout(ctx, s.cfg.RequestTimeout)
	defer cancel()
	started := time.Now()
	s.logLifecycle("appserver_session_start", lifecycleFields{"role": "poll"})
	if err := client.Start(sessionCtx); err != nil {
		_ = s.store.SetState(ctx, "appserver.poll.last_error", sanitizeDiagnosticString(err.Error()))
		s.logLifecycle("appserver_session_start_failed", lifecycleFields{
			"role":        "poll",
			"duration_ms": time.Since(started).Milliseconds(),
			"error":       err,
			"stderr_tail": sanitizedStderrTail(client),
		})
		s.setError(ctx, err)
		return
	}
	s.mu.Lock()
	s.pollConnected = true
	s.pollGeneration++
	generation := s.pollGeneration
	s.mu.Unlock()
	_ = s.store.SetState(ctx, "appserver.poll_connected", "true")
	_ = s.store.SetState(ctx, "appserver.poll.generation", strconv.FormatUint(generation, 10))
	_ = s.store.SetState(ctx, "appserver.poll.last_started_at", time.Now().UTC().Format(time.RFC3339Nano))
	_ = s.store.SetState(ctx, "appserver.poll.last_error", "")
	s.logLifecycle("appserver_session_started", lifecycleFields{
		"role":        "poll",
		"generation":  generation,
		"duration_ms": time.Since(started).Milliseconds(),
	})
	s.installSyncPollEvents(client, generation)
}

func mergeLiveToolSnapshot(current *appserver.ThreadReadSnapshot, liveTool appserver.ThreadReadSnapshot) bool {
	if current == nil || strings.TrimSpace(liveTool.LatestToolFP) == "" {
		return false
	}
	turnID := strings.TrimSpace(liveTool.LatestTurnID)
	if turnID == "" {
		turnID = strings.TrimSpace(current.LatestTurnID)
	}
	if turnID == "" {
		return false
	}
	if current.LatestTurnID != "" && current.LatestTurnID != turnID {
		if !isTerminalStatus(current.LatestTurnStatus) || !turnIDAfter(turnID, current.LatestTurnID) {
			return false
		}
	}
	if current.LatestTurnID == turnID && isTerminalStatus(current.LatestTurnStatus) && strings.TrimSpace(current.LatestFinalFP) != "" {
		return false
	}
	if current.LatestTurnID == turnID && liveToolIsOlderThanCurrentSameTurn(*current, liveTool) {
		return false
	}
	if current.LatestTurnID == turnID &&
		sameToolSnapshot(*current, liveTool) &&
		terminalToolStatus(current.LatestToolStatus) &&
		!terminalToolStatus(liveTool.LatestToolStatus) {
		return false
	}
	current.LatestTurnID = turnID
	current.LatestTurnStatus = firstNonEmpty(liveTool.LatestTurnStatus, current.LatestTurnStatus, "inProgress")
	current.Thread.ActiveTurnID = turnID
	current.Thread.Status = firstNonEmpty(liveTool.Thread.Status, current.Thread.Status, "inProgress")
	current.LatestToolID = liveTool.LatestToolID
	current.LatestToolKind = liveTool.LatestToolKind
	current.LatestToolLabel = liveTool.LatestToolLabel
	current.LatestToolStatus = liveTool.LatestToolStatus
	current.LatestToolOutput = liveTool.LatestToolOutput
	current.LatestToolFP = liveTool.LatestToolFP
	current.LatestToolLiveCurrent = liveTool.LatestToolLiveCurrent
	current.LatestProgressText = liveTool.LatestProgressText
	current.LatestProgressFP = liveTool.LatestProgressFP
	current.DetailItems = upsertLiveToolDetails(current.DetailItems, liveTool.DetailItems)
	return true
}

func (s *Service) preserveTelegramOriginLiveCurrentTool(ctx context.Context, current *appserver.ThreadReadSnapshot, previous *model.ThreadSnapshotState) {
	if current == nil || previous == nil || len(previous.CompactJSON) == 0 {
		return
	}
	if isTerminalStatus(current.LatestTurnStatus) {
		return
	}
	var prev appserver.ThreadReadSnapshot
	if err := json.Unmarshal(previous.CompactJSON, &prev); err != nil {
		return
	}
	turnID := strings.TrimSpace(current.LatestTurnID)
	if turnID == "" || turnID != strings.TrimSpace(prev.LatestTurnID) {
		return
	}
	if !prev.LatestToolLiveCurrent || isTerminalStatus(prev.LatestTurnStatus) || terminalToolStatus(prev.LatestToolStatus) {
		return
	}
	threadID := strings.TrimSpace(firstNonEmpty(current.Thread.ID, prev.Thread.ID))
	if threadID == "" || !s.isTelegramOriginTurn(ctx, threadID, turnID) {
		return
	}
	label := strings.TrimSpace(cleanTelegramNilLiteral(prev.LatestToolLabel))
	if label == "" || strings.TrimSpace(prev.LatestToolFP) == "" {
		return
	}
	if !shouldPreserveTelegramOriginLiveCurrentTool(*current, prev) {
		return
	}
	current.LatestToolID = prev.LatestToolID
	current.LatestToolKind = prev.LatestToolKind
	current.LatestToolLabel = prev.LatestToolLabel
	current.LatestToolStatus = prev.LatestToolStatus
	current.LatestToolOutput = prev.LatestToolOutput
	current.LatestToolFP = prev.LatestToolFP
	current.LatestToolLiveCurrent = prev.LatestToolLiveCurrent
	current.LatestProgressText = prev.LatestProgressText
	current.LatestProgressFP = prev.LatestProgressFP
	current.LatestToolStartedAt = prev.LatestToolStartedAt
	current.LatestToolUpdatedAt = prev.LatestToolUpdatedAt
	current.DetailItems = upsertLiveToolDetails(current.DetailItems, toolOutputDetailItems(prev.DetailItems))
}

func shouldPreserveTelegramOriginLiveCurrentTool(current, previous appserver.ThreadReadSnapshot) bool {
	if !snapshotHasToolEvidence(current) {
		return true
	}
	if sameToolSnapshot(current, previous) {
		return !terminalToolStatus(current.LatestToolStatus)
	}
	previousIndex := latestToolDetailIndex(previous.DetailItems, previous.LatestToolID, previous.LatestToolLabel)
	currentIndex := latestToolDetailIndex(previous.DetailItems, current.LatestToolID, current.LatestToolLabel)
	return previousIndex >= 0 && currentIndex >= 0 && currentIndex < previousIndex
}

func snapshotHasToolEvidence(snapshot appserver.ThreadReadSnapshot) bool {
	if strings.TrimSpace(cleanTelegramNilLiteral(snapshot.LatestToolID)) != "" ||
		strings.TrimSpace(cleanTelegramNilLiteral(snapshot.LatestToolLabel)) != "" ||
		strings.TrimSpace(cleanTelegramNilLiteral(snapshot.LatestToolOutput)) != "" ||
		strings.TrimSpace(snapshot.LatestToolFP) != "" {
		return true
	}
	for _, item := range snapshot.DetailItems {
		switch item.Kind {
		case model.DetailItemTool, model.DetailItemOutput:
			return true
		}
	}
	return false
}

func toolOutputDetailItems(items []model.DetailItem) []model.DetailItem {
	if len(items) == 0 {
		return nil
	}
	out := make([]model.DetailItem, 0, len(items))
	for _, item := range items {
		switch item.Kind {
		case model.DetailItemTool, model.DetailItemOutput:
			out = append(out, item)
		}
	}
	return out
}

func turnIDAfter(candidate, current string) bool {
	candidate = strings.TrimSpace(candidate)
	current = strings.TrimSpace(current)
	if candidate == "" || current == "" || candidate == current {
		return false
	}
	if !codexThreadIDPattern.MatchString(candidate) || !codexThreadIDPattern.MatchString(current) {
		return false
	}
	return strings.Compare(candidate, current) > 0
}

func liveToolIsOlderThanCurrentSameTurn(current, liveTool appserver.ThreadReadSnapshot) bool {
	currentIndex := latestToolDetailIndex(current.DetailItems, current.LatestToolID, current.LatestToolLabel)
	liveIndex := latestToolDetailIndex(current.DetailItems, liveTool.LatestToolID, liveTool.LatestToolLabel)
	return currentIndex >= 0 && liveIndex >= 0 && liveIndex < currentIndex
}

func latestToolDetailIndex(items []model.DetailItem, toolID, label string) int {
	toolID = strings.TrimSpace(toolID)
	label = strings.TrimSpace(label)
	if toolID == "" && label == "" {
		return -1
	}
	for i := len(items) - 1; i >= 0; i-- {
		item := items[i]
		if item.Kind != model.DetailItemTool {
			continue
		}
		if toolID != "" && strings.TrimSpace(item.ID) == toolID {
			return i
		}
		if toolID == "" && label != "" && strings.TrimSpace(item.Label) == label {
			return i
		}
	}
	return -1
}

func upsertLiveToolDetails(items []model.DetailItem, liveItems []model.DetailItem) []model.DetailItem {
	if len(liveItems) == 0 {
		return items
	}
	remove := map[string]struct{}{}
	for _, item := range liveItems {
		if id := strings.TrimSpace(item.ID); id != "" {
			remove[id] = struct{}{}
		}
	}
	out := make([]model.DetailItem, 0, len(items)+len(liveItems))
	for _, item := range items {
		if _, ok := remove[strings.TrimSpace(item.ID)]; ok {
			continue
		}
		out = append(out, item)
	}
	out = append(out, liveItems...)
	return out
}

func sameToolSnapshot(left, right appserver.ThreadReadSnapshot) bool {
	leftID := strings.TrimSpace(left.LatestToolID)
	rightID := strings.TrimSpace(right.LatestToolID)
	if leftID != "" && rightID != "" {
		return leftID == rightID
	}
	leftLabel := strings.TrimSpace(left.LatestToolLabel)
	rightLabel := strings.TrimSpace(right.LatestToolLabel)
	return leftLabel != "" && leftLabel == rightLabel
}

func terminalToolStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "failed", "interrupted", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func (s *Service) indexLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.IndexRefreshInterval)
	defer ticker.Stop()
	for {
		s.syncThreads(ctx, 200)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) pollLoop(ctx context.Context) {
	interval := s.cfg.SyncPollInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.reconcileSync(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.syncReconcileWake:
		case <-ticker.C:
		}
	}
}

func (s *Service) wakeSyncReconcile() {
	select {
	case s.syncReconcileWake <- struct{}{}:
	default:
	}
}

func (s *Service) externalLaunchLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		s.processExternalLaunchRequests(ctx)
		select {
		case <-ctx.Done():
			return
		case <-s.externalLaunchWake:
		case <-ticker.C:
		}
	}
}

func (s *Service) externalReplyLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		s.processExternalReplyBatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) telegramDeliveryLoop(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	for {
		s.processDeliveryBatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) wakeExternalLaunchRenderer() {
	select {
	case s.externalLaunchWake <- struct{}{}:
	default:
	}
}

func (s *Service) controlLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		value, _ := s.store.GetState(ctx, "control.repair_request")
		if strings.TrimSpace(value) != "" {
			at, reason := parseRepairRequest(value)
			s.repairSessions(ctx, reason)
			s.logLifecycle("repair_completed", lifecycleFields{
				"reason":       reason,
				"requested_at": at,
			})
			current, _ := s.store.GetState(ctx, "control.repair_request")
			if current == value {
				_ = s.store.SetState(ctx, "control.repair_request", "")
			}
		} else {
			s.reconcileSessions(ctx)
			s.heartbeatPollSession(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) heartbeatPollSession(ctx context.Context) {
	if !s.usesSharedAppServer() {
		return
	}
	s.mu.Lock()
	if time.Since(s.lastPollHeartbeat) < 10*time.Second {
		s.mu.Unlock()
		return
	}
	s.lastPollHeartbeat = time.Now()
	poll, connected, generation := s.poll, s.pollConnected, s.pollGeneration
	s.mu.Unlock()
	if poll == nil || !connected {
		return
	}
	timeout := s.cfg.RequestTimeout
	if timeout <= 0 || timeout > 5*time.Second {
		timeout = 5 * time.Second
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	_, err := poll.ThreadList(requestCtx, 1, "")
	cancel()
	if err == nil {
		_ = s.store.SetState(ctx, "appserver.poll.last_heartbeat_at", time.Now().UTC().Format(time.RFC3339Nano))
		s.reportHealthRecovered(ctx, "appserver.transport", "Shared Codex App Server connection recovered")
		return
	}
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.poll == poll {
		s.pollConnected = false
	}
	s.mu.Unlock()
	_ = s.store.SetState(ctx, "appserver.poll_connected", "false")
	_ = s.store.SetState(ctx, "appserver.poll.last_error", sanitizeDiagnosticString(err.Error()))
	s.softResetSyncAfterTransportLoss(ctx, err)
	s.notePollSessionError(ctx, "heartbeat", poll, generation, err)
}

func (s *Service) softResetSyncAfterTransportLoss(ctx context.Context, cause error) {
	if !s.usesSharedAppServer() || ctx.Err() != nil {
		return
	}
	summary := "The shared App Server connection was lost. Sync was reset to off; unfinished Telegram receipts are unknown and no request was replayed."
	if cause != nil {
		summary += " " + sanitizeDiagnosticString(cause.Error())
	}
	s.reportHealthFailure(ctx, "appserver.transport", "Shared Codex App Server connection lost", summary, "Wait for reconnect, then run /sync on again.")
	s.syncMu.Lock()
	state, stateErr := s.store.GetSyncState(ctx)
	if stateErr != nil || state.State == model.SyncStateOff || strings.TrimSpace(state.SessionID) == "" {
		s.syncMu.Unlock()
		if stateErr != nil {
			s.logLifecycle("sync_transport_reset_failed", lifecycleFields{"error": stateErr})
		}
		return
	}
	sessionID, resetErr := s.store.ResetSyncOnTransportLoss(ctx)
	if resetErr == nil {
		if s.syncEventCancel != nil {
			s.syncEventCancel()
			s.syncEventCancel = nil
		}
		s.syncEventProcess = nil
		s.syncEventGeneration = 0
		s.syncSubscribedPollGeneration = 0
		s.syncSubscribedThreads = map[string]struct{}{}
		s.syncLeases = map[string]appserver.WriterLease[Session]{}
	}
	s.syncMu.Unlock()
	if resetErr != nil {
		s.logLifecycle("sync_transport_reset_failed", lifecycleFields{"error": resetErr})
		return
	}
	_ = s.syncWriter.ForceClose()
	s.cleanupSyncTopics(ctx, sessionID)
	s.logLifecycle("sync_transport_reset", lifecycleFields{"session_id": sessionID, "error": cause})
}

func (s *Service) reconcileSessions(ctx context.Context) {
	s.ensurePollSession(ctx)
}

func (s *Service) repairSessions(ctx context.Context, reason string) {
	s.sessionMu.Lock()
	s.mu.Lock()
	oldPoll := s.poll
	s.pollConnected = false
	s.poll = s.pollFactory()
	s.pollGeneration++
	pollGeneration := s.pollGeneration
	s.lastError = ""
	s.mu.Unlock()
	s.logLifecycle("appserver_session_repair_start", lifecycleFields{
		"reason":          reason,
		"poll_generation": pollGeneration,
	})
	if oldPoll != nil {
		started := time.Now()
		err := oldPoll.Close()
		s.logAppServerCall("Close", started, err, oldPoll, lifecycleFields{"role": "poll", "operation": "repair"})
	}
	_ = s.store.SetState(ctx, "appserver.poll_connected", "false")
	_ = s.store.SetState(ctx, "appserver.poll.generation", strconv.FormatUint(pollGeneration, 10))
	s.ensurePollSessionLocked(ctx)
	s.sessionMu.Unlock()
	s.bootstrapTrackedState(ctx)
	s.wakeSyncReconcile()
}

func (s *Service) bootstrapTrackedState(ctx context.Context) {
	s.syncThreads(ctx, 200)
}

func (s *Service) syncThreads(ctx context.Context, limit int) {
	s.mu.RLock()
	poll := s.poll
	pollConnected := s.pollConnected
	pollGeneration := s.pollGeneration
	s.mu.RUnlock()
	var client Session
	if pollConnected {
		client = poll
	}
	if client == nil {
		return
	}
	if limit <= 0 {
		limit = 100
	}
	cursor := ""
	remaining := limit
	pageSize := 25
	for remaining > 0 {
		requestCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		result, err := client.ThreadList(requestCtx, min(pageSize, remaining), cursor)
		cancel()
		if err != nil {
			s.notePollSessionError(ctx, "thread_list", client, pollGeneration, err)
			return
		}
		threads := appserver.ThreadsFromList(result)
		if len(threads) == 0 {
			return
		}
		for _, thread := range threads {
			_ = s.store.UpsertThread(ctx, thread)
		}
		remaining -= len(threads)
		nextCursor, _ := result["nextCursor"].(string)
		if strings.TrimSpace(nextCursor) == "" {
			return
		}
		cursor = nextCursor
	}
}

func (s *Service) processDeliveryBatch(ctx context.Context) {
	s.mu.RLock()
	sender := s.sender
	s.mu.RUnlock()
	if sender == nil {
		return
	}
	items, err := s.store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(items) == 0 {
		return
	}
	for _, item := range items {
		var payload model.DeliveryPayload
		if err := json.Unmarshal([]byte(item.PayloadJSON), &payload); err != nil {
			safeError := sanitizeDiagnosticString(err.Error())
			_ = s.store.RecordDeliveryAttempt(ctx, item.ID, item.RetryCount+1, "decode_error", safeError)
			_ = s.store.FailDelivery(ctx, item.ID, item.RetryCount+1, time.Now().UTC().Add(s.cfg.DeliveryRetryBase), safeError, true)
			continue
		}
		if item.Kind == "health" && !s.healthDeliveryIsCurrent(ctx, item, payload) {
			_ = s.store.RecordDeliveryAttempt(ctx, item.ID, item.RetryCount+1, "superseded", "")
			_ = s.store.SupersedeDelivery(ctx, item.ID)
			continue
		}
		s.logTelegramRenderContainsNil(payload.ThreadID, payload.TurnID, "delivery", 0, payload.Text)
		options := silentSendOptions()
		if item.Kind == "health" {
			options = notifySendOptions()
			options.Background = true
		} else if item.Kind == externalTerminalDeliveryKind {
			options = notifySendOptions()
		}
		deliveryTopicID := item.TopicID
		_, err := sender.SendMessage(ctx, item.ChatID, deliveryTopicID, payload.Text, payload.Buttons, options)
		if err != nil && item.Kind == "health" && deliveryTopicID != syncGeneralSendTopicID && isMessageThreadNotFoundError(err) {
			attempt := item.RetryCount + 1
			_ = s.store.RecordDeliveryAttempt(ctx, item.ID, attempt, "general_fallback", sanitizeDiagnosticString(err.Error()))
			deliveryTopicID = syncGeneralSendTopicID
			_, err = sender.SendMessage(ctx, item.ChatID, deliveryTopicID, payload.Text, payload.Buttons, options)
		}
		if err != nil {
			attempt := item.RetryCount + 1
			safeError := sanitizeDiagnosticString(err.Error())
			_ = s.store.RecordDeliveryAttempt(ctx, item.ID, attempt, "send_error", safeError)
			dead := attempt >= s.cfg.DeliveryMaxAttempts
			backoff := s.cfg.DeliveryRetryBase * time.Duration(1<<min(attempt-1, 4))
			_ = s.store.FailDelivery(ctx, item.ID, attempt, time.Now().UTC().Add(backoff), safeError, dead)
			s.setError(ctx, err)
			continue
		}
		_ = s.store.RecordDeliveryAttempt(ctx, item.ID, item.RetryCount+1, "delivered", "")
		_ = s.store.CompleteDelivery(ctx, item.ID)
	}
}

func (s *Service) healthDeliveryIsCurrent(ctx context.Context, item model.DeliveryQueueItem, payload model.DeliveryPayload) bool {
	key := healthKey(payload.HealthKey)
	episodeID := strings.TrimSpace(payload.HealthEpisodeID)
	state := strings.TrimSpace(payload.HealthState)
	if key == "" || episodeID == "" || state == "" {
		return true
	}
	episode := s.loadHealthEpisode(ctx, "health."+key)
	if episode.EpisodeID != episodeID {
		return false
	}
	switch state {
	case "open":
		return episode.Open
	case "recovered":
		if episode.Open {
			return false
		}
		openEventID := "health:" + key + ":" + episodeID + ":open"
		status, err := s.store.DeliveryStatusForEvent(ctx, openEventID, item.ChatKey)
		return err == nil && status == model.DeliveryStatusDelivered
	default:
		return false
	}
}

func isMessageThreadNotFoundError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "message thread not found")
}

func shortButtonLabel(label string) string {
	label = strings.TrimSpace(label)
	const limit = 60
	if len(label) <= limit {
		return label
	}
	return strings.TrimSpace(label[:limit-3]) + "..."
}

func (s *Service) turnStartOptions(_ context.Context, collaborationMode string, thread *model.Thread) appserver.TurnStartOptions {
	options := appserver.TurnStartOptions{
		CollaborationMode: strings.TrimSpace(collaborationMode),
		ApprovalPolicy:    telegramApprovalPolicy,
		ApprovalsReviewer: telegramApprovalsReviewer,
		SandboxMode:       telegramSandboxMode,
	}
	if thread != nil {
		options.Model = strings.TrimSpace(thread.PreferredModel)
	}
	return options
}

const (
	telegramApprovalPolicy    = "on-request"
	telegramApprovalsReviewer = "auto_review"
	telegramSandboxMode       = "workspace-write"
)

func telegramThreadStartOptions() appserver.ThreadStartOptions {
	return appserver.ThreadStartOptions{
		ApprovalPolicy:    telegramApprovalPolicy,
		ApprovalsReviewer: telegramApprovalsReviewer,
		SandboxMode:       telegramSandboxMode,
	}
}

func (s *Service) ensureStartedTurnSnapshot(ctx context.Context, thread *model.Thread, turnID string) {
	turnID = strings.TrimSpace(turnID)
	if thread == nil || turnID == "" {
		return
	}
	previous, err := s.store.GetSnapshot(ctx, thread.ID)
	if err == nil && previous != nil && strings.TrimSpace(previous.LastSeenTurnID) == turnID {
		return
	}
	startedThread := *thread
	startedThread.Status = "inProgress"
	startedThread.ActiveTurnID = turnID
	if startedThread.UpdatedAt == 0 {
		startedThread.UpdatedAt = time.Now().UTC().Unix()
	}
	current := appserver.ThreadReadSnapshot{
		Thread:           startedThread,
		LatestTurnID:     turnID,
		LatestTurnStatus: "inProgress",
	}
	nextSnapshot := appserver.CompactSnapshot(previous, current, time.Now().UTC())
	nextSnapshot.NextPollAfter = model.TimeString(time.Now().UTC().Add(syncTerminalPollInterval(s.cfg.SyncPollInterval)).Format(time.RFC3339Nano))
	_ = s.store.UpsertThread(ctx, startedThread)
	_ = s.store.UpsertSnapshot(ctx, startedThread.ID, nextSnapshot)
	s.logLifecycle("telegram_started_turn_snapshot_seeded", lifecycleFields{
		"thread_id": startedThread.ID,
		"turn_id":   turnID,
	})
}

func boundedTurnHotPollLoop(ctx context.Context, maxDuration, tick time.Duration, pollOnce func() bool) {
	timer := time.NewTimer(maxDuration)
	defer timer.Stop()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if !pollOnce() {
				return
			}
		}
	}
}

func threadLooksActiveForInput(thread *model.Thread) bool {
	if thread == nil {
		return false
	}
	if strings.TrimSpace(thread.ActiveTurnID) != "" {
		return true
	}
	status := strings.ToLower(strings.TrimSpace(thread.Status))
	return status == "active" || strings.HasPrefix(status, "active[") || strings.Contains(status, "waitingon") || strings.Contains(status, "inprogress") || strings.Contains(status, "running")
}

func steerFailureImpliesActive(err error) bool {
	if err == nil {
		return false
	}
	if steerFailureMeansNoActiveTurn(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "active turn") ||
		strings.Contains(msg, "activeturn") ||
		strings.Contains(msg, "already active") ||
		strings.Contains(msg, "in-flight") ||
		strings.Contains(msg, "not steerable")
}

func steerFailureMeansNoActiveTurn(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no active turn") ||
		strings.Contains(msg, "no active run") ||
		strings.Contains(msg, "turn is not active")
}

func activeTurnIDFromSteerMismatch(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "expected active turn id") || !strings.Contains(lower, "found") {
		return ""
	}
	matches := codexThreadIDExtractPattern.FindAllString(msg, -1)
	if len(matches) == 0 {
		return ""
	}
	// The app-server error lists expected first and authoritative active turn last.
	return matches[len(matches)-1]
}

func activeThreadReplyText(thread *model.Thread, steerErr error) string {
	label := "Thread"
	turnID := ""
	if thread != nil {
		label = thread.Label()
		turnID = strings.TrimSpace(thread.ActiveTurnID)
	}
	if steerErr != nil {
		if turnID != "" {
			return fmt.Sprintf("%s is already active, but Codex did not accept input for active turn %s: %v. I did not start a parallel turn.", label, turnID, steerErr)
		}
		return fmt.Sprintf("%s is already active, but Codex did not accept input: %v. I did not start a parallel turn.", label, steerErr)
	}
	if turnID != "" {
		return fmt.Sprintf("%s is already active. Reply to the current live turn card to steer turn %s, or wait for completion. I did not start a parallel turn.", label, turnID)
	}
	return fmt.Sprintf("%s is already active, but the active turn id is not available yet. Wait for completion or use /stop. I did not start a parallel turn.", label)
}

func (s *Service) notePollSessionError(ctx context.Context, operation string, poll Session, generation uint64, err error) {
	if err == nil {
		return
	}
	s.sessionMu.Lock()
	s.mu.RLock()
	currentPoll := s.poll
	currentGeneration := s.pollGeneration
	current := currentPoll == poll && currentGeneration == generation
	s.mu.RUnlock()
	if !current {
		s.sessionMu.Unlock()
		s.logLifecycle("appserver_session_error_stale", lifecycleFields{
			"operation":          operation,
			"error":              err,
			"generation":         generation,
			"current_generation": currentGeneration,
		})
		return
	}
	s.logLifecycle("appserver_session_error", lifecycleFields{"operation": operation, "error": err})
	s.setError(ctx, fmt.Errorf("%s: %w", operation, err))
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		_ = s.RequestRepair(ctx, operation)
	}
	s.sessionMu.Unlock()
}

func (s *Service) setError(ctx context.Context, err error) {
	if err == nil {
		return
	}
	message := sanitizeDiagnosticString(err.Error())
	s.mu.Lock()
	s.lastError = message
	s.mu.Unlock()
	_ = s.store.SetState(ctx, "daemon.last_error", message)
}

func randomToken() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func parseTime(value model.TimeString) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, string(value))
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func maxDuration(left, right time.Duration) time.Duration {
	if left > right {
		return left
	}
	return right
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func threadIDFromEvent(event appserver.Event) string {
	if event.Params == nil {
		return ""
	}
	if value, ok := event.Params["threadId"].(string); ok {
		return value
	}
	if thread, ok := event.Params["thread"].(map[string]any); ok {
		if value, ok := thread["id"].(string); ok {
			return value
		}
	}
	return ""
}

func appserverThreadTurnID(payload map[string]any) string {
	turn, _ := payload["turn"].(map[string]any)
	if turn == nil {
		return ""
	}
	if id, ok := turn["id"].(string); ok {
		return id
	}
	return ""
}

func trimPreview(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 120 {
		return value
	}
	return value[:117] + "..."
}
