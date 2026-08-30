package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func newTestService(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	service, err := New(config.Config{
		Paths: config.Paths{
			Home: root, DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs"),
			DBPath: filepath.Join(root, "data", "state.sqlite"),
		},
		AllowedUserIDs: []int64{123456789},
		SyncGroupID:    -1001,
		DefaultCWD:     `C:\Users\you\Projects\Codex`,
	})
	if err != nil {
		t.Fatalf("daemon.New failed: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	return service
}

type recordedMessage struct {
	chatID    int64
	topicID   int64
	messageID int64
	text      string
	entities  []model.MessageEntity
	buttons   [][]model.ButtonSpec
	options   model.SendOptions
}

type recordedDocument struct {
	chatID   int64
	topicID  int64
	fileName string
	data     []byte
	caption  string
	options  model.SendOptions
}

type recordingSender struct {
	messages  []recordedMessage
	documents []recordedDocument
	edits     []recordedMessage
	deletes   []recordedMessage
	editErr   error
	sendErr   error
	sendErrs  []error
}

func (s *recordingSender) SendMessage(_ context.Context, chatID, topicID int64, text string, buttons [][]model.ButtonSpec, options model.SendOptions) (int64, error) {
	if len(s.sendErrs) > 0 {
		err := s.sendErrs[0]
		s.sendErrs = s.sendErrs[1:]
		if err != nil {
			return 0, err
		}
	}
	if s.sendErr != nil {
		return 0, s.sendErr
	}
	messageID := int64(len(s.messages) + 1)
	s.messages = append(s.messages, recordedMessage{chatID: chatID, topicID: topicID, messageID: messageID, text: text, buttons: buttons, options: options})
	return messageID, nil
}

func (s *recordingSender) SendRenderedMessages(_ context.Context, chatID, topicID int64, messages []model.RenderedMessage, buttons [][]model.ButtonSpec, options model.SendOptions) ([]int64, error) {
	ids := make([]int64, 0, len(messages))
	for _, message := range messages {
		messageID := int64(len(s.messages) + 1)
		s.messages = append(s.messages, recordedMessage{chatID: chatID, topicID: topicID, messageID: messageID, text: message.Text, entities: message.Entities, buttons: buttons, options: options})
		ids = append(ids, messageID)
	}
	return ids, nil
}

func (s *recordingSender) EditMessage(_ context.Context, chatID, topicID, messageID int64, text string, buttons [][]model.ButtonSpec) error {
	if s.editErr != nil {
		return s.editErr
	}
	s.edits = append(s.edits, recordedMessage{chatID: chatID, topicID: topicID, messageID: messageID, text: text, buttons: buttons})
	return nil
}

func (s *recordingSender) EditRenderedMessage(_ context.Context, chatID, topicID, messageID int64, rendered model.RenderedMessage, buttons [][]model.ButtonSpec) error {
	if s.editErr != nil {
		return s.editErr
	}
	s.edits = append(s.edits, recordedMessage{chatID: chatID, topicID: topicID, messageID: messageID, text: rendered.Text, entities: rendered.Entities, buttons: buttons})
	return nil
}

func (s *recordingSender) DeleteMessage(_ context.Context, chatID, topicID, messageID int64) error {
	s.deletes = append(s.deletes, recordedMessage{chatID: chatID, topicID: topicID, messageID: messageID})
	return nil
}

func (s *recordingSender) SendDocumentData(_ context.Context, chatID, topicID int64, fileName string, data []byte, caption string, options model.SendOptions) (int64, error) {
	s.documents = append(s.documents, recordedDocument{chatID: chatID, topicID: topicID, fileName: fileName, data: append([]byte(nil), data...), caption: caption, options: options})
	return int64(len(s.documents)), nil
}

type stubSession struct {
	startCalls             int
	startErr               error
	closeCalls             int
	threadReads            map[string]map[string]any
	threadListResult       map[string]any
	threadListCalls        int
	threadListLimit        int
	threadListCursor       string
	threadListErr          error
	threadReadID           string
	threadReadIncludeTurns bool
	models                 []appserver.ModelOption
	collaborationModes     []appserver.CollaborationModeOption
	threadReadErr          error
	threadResumeErr        error
	threadStartErr         error
	threadStartResult      map[string]any
	turnStartErr           error
	turnSteerErr           error
	turnSteerErrs          []error
	threadStartCalls       []string
	threadStartOptions     []appserver.ThreadStartOptions
	threadSetNameCalls     []threadSetNameCall
	threadResumeCalls      []threadResumeCall
	turnSteerCalls         []turnCall
	turnStartCalls         []turnCall
	turnInterruptCalls     []turnCall
	respondRequestCalls    []respondRequestCall
	stderrTail             []string
}

type threadResumeCall struct{ threadID, cwd string }
type threadSetNameCall struct{ threadID, name string }
type turnCall struct {
	threadID, turnID, message, cwd, collaborationMode, model, reasoningEffort string
	approvalPolicy, approvalsReviewer, sandboxMode                            string
}
type respondRequestCall struct {
	requestID string
	result    map[string]any
}

func (s *stubSession) Start(context.Context) error       { s.startCalls++; return s.startErr }
func (s *stubSession) Close() error                      { s.closeCalls++; return nil }
func (s *stubSession) Subscribe() <-chan appserver.Event { return nil }
func (s *stubSession) ThreadList(_ context.Context, limit int, cursor string) (map[string]any, error) {
	s.threadListCalls++
	s.threadListLimit, s.threadListCursor = limit, cursor
	return s.threadListResult, s.threadListErr
}
func (s *stubSession) ThreadRead(_ context.Context, threadID string, includeTurns bool) (map[string]any, error) {
	s.threadReadID, s.threadReadIncludeTurns = threadID, includeTurns
	if s.threadReadErr != nil {
		return nil, s.threadReadErr
	}
	if payload, ok := s.threadReads[threadID]; ok {
		return payload, nil
	}
	return nil, nil
}
func (s *stubSession) ThreadResume(_ context.Context, threadID, cwd string) (map[string]any, error) {
	s.threadResumeCalls = append(s.threadResumeCalls, threadResumeCall{threadID, cwd})
	return nil, s.threadResumeErr
}
func (s *stubSession) ThreadStart(_ context.Context, cwd string, options appserver.ThreadStartOptions) (map[string]any, error) {
	s.threadStartCalls = append(s.threadStartCalls, cwd)
	s.threadStartOptions = append(s.threadStartOptions, options)
	if s.threadStartErr != nil {
		return nil, s.threadStartErr
	}
	return s.threadStartResult, nil
}
func (s *stubSession) ThreadSetName(_ context.Context, threadID, name string) (map[string]any, error) {
	s.threadSetNameCalls = append(s.threadSetNameCalls, threadSetNameCall{threadID, name})
	return map[string]any{}, nil
}
func (s *stubSession) TurnStart(_ context.Context, threadID, message, cwd string, options appserver.TurnStartOptions) (map[string]any, error) {
	if s.turnStartErr != nil {
		return nil, s.turnStartErr
	}
	s.turnStartCalls = append(s.turnStartCalls, turnCall{threadID: threadID, message: message, cwd: cwd, collaborationMode: options.CollaborationMode, model: options.Model, reasoningEffort: options.ReasoningEffort, approvalPolicy: options.ApprovalPolicy, approvalsReviewer: options.ApprovalsReviewer, sandboxMode: options.SandboxMode})
	return map[string]any{"turn": map[string]any{"id": "started-turn"}}, nil
}
func (s *stubSession) TurnSteer(_ context.Context, threadID, turnID, message string) (map[string]any, error) {
	s.turnSteerCalls = append(s.turnSteerCalls, turnCall{threadID: threadID, turnID: turnID, message: message})
	if len(s.turnSteerErrs) > 0 {
		err := s.turnSteerErrs[0]
		s.turnSteerErrs = s.turnSteerErrs[1:]
		if err != nil {
			return nil, err
		}
	}
	if s.turnSteerErr != nil {
		return nil, s.turnSteerErr
	}
	return map[string]any{"turn": map[string]any{"id": turnID}}, nil
}
func (s *stubSession) TurnInterrupt(_ context.Context, threadID, turnID string) error {
	s.turnInterruptCalls = append(s.turnInterruptCalls, turnCall{threadID: threadID, turnID: turnID})
	return nil
}
func (s *stubSession) ModelList(context.Context, bool) ([]appserver.ModelOption, error) {
	if s.models != nil {
		return s.models, nil
	}
	return []appserver.ModelOption{{ID: "gpt-default", IsDefault: true, SupportedReasoningEffort: []string{"low", "medium", "high"}}}, nil
}
func (s *stubSession) CollaborationModeList(context.Context) ([]appserver.CollaborationModeOption, error) {
	return s.collaborationModes, nil
}
func (s *stubSession) RespondServerRequest(_ context.Context, requestID string, result map[string]any) error {
	s.respondRequestCalls = append(s.respondRequestCalls, respondRequestCall{requestID, result})
	return nil
}
func (s *stubSession) StderrTail() []string { return s.stderrTail }

type startCountingSession struct {
	stubSession
	mu               sync.Mutex
	started, unblock chan struct{}
	once             sync.Once
	starts           int
	signaled         bool
}

func newStartCountingSession() *startCountingSession {
	return &startCountingSession{started: make(chan struct{}), unblock: make(chan struct{})}
}
func (s *startCountingSession) Start(ctx context.Context) error {
	s.mu.Lock()
	s.starts++
	if !s.signaled {
		close(s.started)
		s.signaled = true
	}
	s.mu.Unlock()
	select {
	case <-s.unblock:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *startCountingSession) ThreadList(context.Context, int, string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (s *startCountingSession) waitStarted(t *testing.T, role string) {
	t.Helper()
	select {
	case <-s.started:
	case <-time.After(time.Second):
		t.Fatalf("%s session did not start", role)
	}
}
func (s *startCountingSession) release()        { s.once.Do(func() { close(s.unblock) }) }
func (s *startCountingSession) StartCalls() int { s.mu.Lock(); defer s.mu.Unlock(); return s.starts }

func callbackTokenForButton(rows [][]model.ButtonSpec, label string) string {
	for _, row := range rows {
		for _, button := range row {
			if strings.Contains(button.Text, label) {
				return button.CallbackData
			}
		}
	}
	return ""
}
