package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

type fakeAFCForum struct {
	validateErr error
	nextTopicID int64
	createErrAt int
	creates     []string
	deletes     []int64
	sends       []fakeAFCSend
	edits       []fakeAFCEdit
	actions     []fakeAFCAction
	onDelete    func()
}

type fakeAFCSend struct {
	topicID, messageID int64
	text               string
	silent             bool
}
type fakeAFCEdit struct {
	topicID, messageID int64
	text               string
}
type fakeAFCAction struct {
	topicID, messageID int64
	text               string
	buttons            [][]model.ButtonSpec
}

type afcWriterSession struct {
	*stubSession
	events chan appserver.Event
}

func (s *afcWriterSession) Subscribe() <-chan appserver.Event { return s.events }

func (f *fakeAFCForum) ValidateAFCGroup(context.Context, int64) error { return f.validateErr }
func (f *fakeAFCForum) CreateAFCTopic(_ context.Context, title string) (int64, error) {
	f.creates = append(f.creates, title)
	if f.createErrAt > 0 && len(f.creates) == f.createErrAt {
		return 0, errors.New("create failed")
	}
	f.nextTopicID++
	return f.nextTopicID, nil
}
func (f *fakeAFCForum) DeleteAFCTopic(_ context.Context, topicID int64) error {
	if f.onDelete != nil {
		f.onDelete()
	}
	f.deletes = append(f.deletes, topicID)
	return nil
}
func (f *fakeAFCForum) SendAFCMessage(_ context.Context, topicID int64, text string, silent bool) (int64, error) {
	id := int64(100 + len(f.sends))
	f.sends = append(f.sends, fakeAFCSend{topicID: topicID, messageID: id, text: text, silent: silent})
	return id, nil
}
func (f *fakeAFCForum) EditAFCMessage(_ context.Context, topicID, messageID int64, text string) error {
	f.edits = append(f.edits, fakeAFCEdit{topicID: topicID, messageID: messageID, text: text})
	return nil
}
func (f *fakeAFCForum) SendAFCActionMessage(_ context.Context, topicID int64, text string, buttons [][]model.ButtonSpec) (int64, error) {
	id := int64(200 + len(f.actions))
	f.actions = append(f.actions, fakeAFCAction{topicID: topicID, messageID: id, text: text, buttons: buttons})
	return id, nil
}

func TestAFCPartialActivationOwnsGroupAndDisablesLegacyObserver(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	poll := &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "thread-old", "title": "Old", "updatedAt": float64(10)},
		map[string]any{"id": "thread-new", "title": "New", "updatedAt": float64(20)},
		map[string]any{"id": "thread-archived", "title": "Archived", "updatedAt": float64(30), "archived": true},
	}}}
	service.poll = poll
	service.pollConnected = true
	forum := &fakeAFCForum{nextTopicID: 10, createErrAt: 2}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if err := service.store.SetGlobalObserverTarget(ctx, 99, 0, true); err != nil {
		t.Fatal(err)
	}

	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/afc on", 0)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || !strings.Contains(response.Text, "active: 1") {
		t.Fatalf("response = %#v", response)
	}
	if len(forum.creates) != 2 || forum.creates[0] != "New" || forum.creates[1] != "Old" {
		t.Fatalf("creates = %v", forum.creates)
	}
	state, _ := service.store.GetAFCState(ctx)
	if state.State != model.AFCStateActive {
		t.Fatalf("state = %#v", state)
	}
	observer, _ := service.store.GetState(ctx, "observer.global_enabled")
	if observer != "false" {
		t.Fatalf("observer = %q", observer)
	}
	if poll.threadResumeCalls != nil || poll.turnStartCalls != nil {
		t.Fatalf("passive activation mutated app-server: %#v %#v", poll.threadResumeCalls, poll.turnStartCalls)
	}
	if service.legacyWriter.Snapshot().State != "stopped" {
		t.Fatalf("legacy writer = %#v", service.legacyWriter.Snapshot())
	}
}

func TestAFCUnknownTopicAndCallbacksFailClosed(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	response, err := service.HandleMessage(context.Background(), -1001, 999, 123456789, "hello", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "AFC") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	response, err = service.HandleCallback(context.Background(), -1001, 999, 5, 123456789, "legacy-token")
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "AFC") {
		t.Fatalf("callback=%#v err=%v", response, err)
	}
}

func TestAFCZeroTopicActivationLeavesObserverEnabled(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	service.poll = &stubSession{threadListResult: map[string]any{"data": []any{map[string]any{"id": "thread-1", "title": "One", "updatedAt": float64(10)}}}}
	service.pollConnected = true
	service.SetAFCForum(&fakeAFCForum{createErrAt: 1})
	ctx := context.Background()
	if err := service.store.SetGlobalObserverTarget(ctx, 99, 0, true); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/afc on", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "active: 0") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	state, _ := service.store.GetAFCState(ctx)
	if state.State != model.AFCStateOff {
		t.Fatalf("state=%#v", state)
	}
	observer, _ := service.store.GetState(ctx, "observer.global_enabled")
	if observer != "true" {
		t.Fatalf("observer=%q, want unchanged", observer)
	}
}

func TestAFCPassiveSyncSendsSilentStatusAndNotifyingFinal(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	ctx := context.Background()
	if err := service.store.BeginAFCActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	if err := service.store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "s", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", Title: "One", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.FinishAFCActivation(ctx, "s", `{}`, true); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": {
			"thread": map[string]any{
				"id": "thread-1", "title": "One", "status": "completed",
				"turns": []any{map[string]any{
					"id": "turn-1", "status": "completed",
					"items": []any{map[string]any{"id": "final-1", "type": "agentMessage", "phase": "final_answer", "text": "done"}},
				}},
			},
		},
	}}
	service.poll, service.pollConnected = poll, true
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	service.syncAFC(ctx)
	if len(forum.sends) != 2 || !forum.sends[0].silent || forum.sends[1].silent {
		t.Fatalf("sends = %#v", forum.sends)
	}
	if !strings.Contains(forum.sends[1].text, "done") {
		t.Fatalf("final = %q", forum.sends[1].text)
	}
	if len(poll.threadResumeCalls) != 0 || len(poll.turnStartCalls) != 0 {
		t.Fatal("passive sync attempted a mutation")
	}
}

func TestAFCOffMarksOffBeforeCleanupAndDoesNotRestoreLegacy(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	ctx := context.Background()
	if err := service.store.BeginAFCActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	if err := service.store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "s", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.FinishAFCActivation(ctx, "s", `{}`, true); err != nil {
		t.Fatal(err)
	}
	forum := &fakeAFCForum{onDelete: func() {
		state, _ := service.store.GetAFCState(ctx)
		if state.State != model.AFCStateOff {
			t.Fatalf("delete observed state %q", state.State)
		}
	}}
	service.SetAFCForum(forum)
	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/afc off", 0)
	if err != nil || response == nil {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(forum.deletes) != 1 {
		t.Fatalf("deletes=%v", forum.deletes)
	}
	observer, _ := service.store.GetState(ctx, "observer.global_enabled")
	if observer != "false" {
		t.Fatalf("observer restored: %q", observer)
	}
	if service.legacyWriter.Snapshot().State != "stopped" {
		t.Fatalf("legacy writer restarted: %#v", service.legacyWriter.Snapshot())
	}
}

func TestAFCConcurrentTopicsShareWriterAndDuplicateDoesNotReplay(t *testing.T) {
	service := activeAFCService(t)
	writer := &afcWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 8)}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()

	first, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "first prompt", 0)
	if err != nil || first == nil || !strings.Contains(first.Text, "started") {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	duplicate, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "must not replay", 0)
	if err != nil || duplicate == nil || !strings.Contains(duplicate.Text, "already dispatched") {
		t.Fatalf("duplicate=%#v err=%v", duplicate, err)
	}
	sameTopic, err := service.HandleMessageWithID(ctx, -1001, 11, 502, 123456789, "second same topic", 0)
	if err != nil || sameTopic == nil || !strings.Contains(sameTopic.Text, "already active") {
		t.Fatalf("sameTopic=%#v err=%v", sameTopic, err)
	}
	second, err := service.HandleMessageWithID(ctx, -1001, 12, 601, 123456789, "parallel prompt", 0)
	if err != nil || second == nil || !strings.Contains(second.Text, "started") {
		t.Fatalf("second=%#v err=%v", second, err)
	}

	if writer.startCalls != 1 {
		t.Fatalf("writer starts=%d, want one shared process", writer.startCalls)
	}
	if len(writer.threadResumeCalls) != 2 || len(writer.turnStartCalls) != 2 {
		t.Fatalf("resume=%#v starts=%#v", writer.threadResumeCalls, writer.turnStartCalls)
	}
	if writer.turnStartCalls[0].message != "first prompt" || writer.turnStartCalls[1].message != "parallel prompt" {
		t.Fatalf("prompts=%#v", writer.turnStartCalls)
	}
	snapshot := service.afcWriter.Snapshot()
	if snapshot.Active != 2 || snapshot.Generation == 0 {
		t.Fatalf("writer snapshot=%#v", snapshot)
	}
}

func TestAFCLegacyClaimConflictRejectsBeforeMutation(t *testing.T) {
	service := activeAFCService(t)
	legacy := &stubSession{}
	service.liveFactory = func() Session { return legacy }
	lease, err := service.legacyWriter.Reserve(context.Background(), "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.legacyWriter.MarkActive(lease); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleMessageWithID(context.Background(), -1001, 11, 501, 123456789, "blocked", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "owned by legacy") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(legacy.threadResumeCalls) != 0 || len(legacy.turnStartCalls) != 0 {
		t.Fatalf("mutation occurred: %#v %#v", legacy.threadResumeCalls, legacy.turnStartCalls)
	}
	if err := service.legacyWriter.MarkTerminal(lease); err != nil {
		t.Fatal(err)
	}
}

func TestAFCOwnershipBlocksLaterLegacyLaunchBeforeMutation(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "AFC owns this", 0); err != nil {
		t.Fatal(err)
	}
	beforeResume, beforeStart := len(writer.threadResumeCalls), len(writer.turnStartCalls)
	if _, err := service.sendInputToThreadTurn(ctx, 123456789, 0, "thread-1", "", "legacy must fail", ""); !errors.Is(err, appserver.ErrThreadClaimed) {
		t.Fatalf("legacy error=%v, want ErrThreadClaimed", err)
	}
	if len(writer.threadResumeCalls) != beforeResume || len(writer.turnStartCalls) != beforeStart {
		t.Fatalf("legacy mutated AFC process: resume=%#v starts=%#v", writer.threadResumeCalls, writer.turnStartCalls)
	}
}

func TestAFCAmbiguousTurnStartIsUnknownAndNeverReplayed(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{turnStartErr: errors.New("request timeout for turn/start")}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	response, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "once", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "unknown") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	response, err = service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "replay", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "unknown") {
		t.Fatalf("duplicate=%#v err=%v", response, err)
	}
	if len(writer.turnStartCalls) != 0 {
		t.Fatalf("stub records successful calls only, got %#v", writer.turnStartCalls)
	}
	if service.afcWriter.Snapshot().Unknown != 1 {
		t.Fatalf("writer=%#v", service.afcWriter.Snapshot())
	}
}

func TestAFCTerminalEventsRoutePerTopicAndCloseAfterLastTurn(t *testing.T) {
	service := activeAFCService(t)
	writer := &afcWriterSession{stubSession: &stubSession{threadReads: map[string]map[string]any{
		"thread-1": afcCompletedPayload("thread-1", "started-turn", "done one"),
		"thread-2": afcCompletedPayload("thread-2", "started-turn", "done two"),
	}}, events: make(chan appserver.Event, 8)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleMessageWithID(ctx, -1001, 12, 601, 123456789, "two", 0); err != nil {
		t.Fatal(err)
	}
	generation := service.afcWriter.Snapshot().Generation
	eventOne := appserver.Event{Method: "turn/completed", Params: map[string]any{"threadId": "thread-1", "turnId": "started-turn"}}
	service.handleAFCWriterEvent(ctx, writer, eventOne, generation+1)
	if len(forum.sends) != 0 {
		t.Fatalf("stale generation routed sends=%#v", forum.sends)
	}
	service.handleAFCWriterEvent(ctx, writer, eventOne, generation)
	if service.afcWriter.Snapshot().Active != 1 || writer.closeCalls != 0 {
		t.Fatalf("after first: writer=%#v closes=%d", service.afcWriter.Snapshot(), writer.closeCalls)
	}
	for _, send := range forum.sends {
		if send.topicID != 11 {
			t.Fatalf("first event crossed topic: %#v", forum.sends)
		}
	}
	eventTwo := appserver.Event{Method: "turn/completed", Params: map[string]any{"threadId": "thread-2", "turnId": "started-turn"}}
	service.handleAFCWriterEvent(ctx, writer, eventTwo, generation)
	if service.afcWriter.Snapshot().State != appserver.WriterStopped || writer.closeCalls != 1 {
		t.Fatalf("after last: writer=%#v closes=%d", service.afcWriter.Snapshot(), writer.closeCalls)
	}
	seenTwo := false
	for _, send := range forum.sends {
		if send.topicID == 12 {
			seenTwo = true
		}
	}
	if !seenTwo {
		t.Fatalf("second topic received no routed event: %#v", forum.sends)
	}
}

func TestAFCPollTerminalEvidenceClosesWriterWhenEventWasMissed(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": afcCompletedPayload("thread-1", "started-turn", "done")}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	service.syncAFC(ctx)
	if snapshot := service.afcWriter.Snapshot(); snapshot.State != appserver.WriterStopped || snapshot.Active != 0 {
		t.Fatalf("writer=%#v", snapshot)
	}
	if writer.closeCalls != 1 {
		t.Fatalf("writer close calls=%d", writer.closeCalls)
	}
}

func TestAFCApprovalCallbackIsGuardedByTopicTurnAndGeneration(t *testing.T) {
	service := activeAFCService(t)
	writer := &afcWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "needs approval", 0); err != nil {
		t.Fatal(err)
	}
	generation := service.afcWriter.Snapshot().Generation
	event := appserver.Event{Channel: "server_request", Method: "item/commandExecution/requestApproval", ID: "request-1", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn", "itemId": "item-1", "question": "Run it?",
	}}
	service.handleAFCWriterEvent(ctx, writer, event, generation)
	if len(forum.actions) != 1 || forum.actions[0].topicID != 11 || len(forum.actions[0].buttons) != 2 {
		t.Fatalf("actions=%#v", forum.actions)
	}
	token := forum.actions[0].buttons[0][0].CallbackData
	stale, err := service.HandleCallback(ctx, -1001, 12, forum.actions[0].messageID, 123456789, token)
	if err != nil || stale == nil || !strings.Contains(stale.CallbackText, "stale") {
		t.Fatalf("cross-topic=%#v err=%v", stale, err)
	}
	if len(writer.respondRequestCalls) != 0 {
		t.Fatal("cross-topic callback reached App Server")
	}
	response, err := service.HandleCallback(ctx, -1001, 11, forum.actions[0].messageID, 123456789, token)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "sent") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.respondRequestCalls) != 1 || writer.respondRequestCalls[0].requestID != "request-1" || writer.respondRequestCalls[0].result["decision"] != "accept" {
		t.Fatalf("responses=%#v", writer.respondRequestCalls)
	}
	response, err = service.HandleCallback(ctx, -1001, 11, forum.actions[0].messageID, 123456789, token)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "stale") {
		t.Fatalf("duplicate callback=%#v err=%v", response, err)
	}
}

func TestAFCDesktopOriginApprovalIsNotActionable(t *testing.T) {
	service := activeAFCService(t)
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	process := &afcWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 1)}
	event := appserver.Event{Channel: "server_request", Method: "item/requestApproval", ID: "desktop-request", Params: map[string]any{"threadId": "thread-1", "turnId": "desktop-turn"}}
	service.handleAFCWriterEvent(context.Background(), process, event, 99)
	if len(forum.actions) != 0 {
		t.Fatalf("desktop approval became actionable: %#v", forum.actions)
	}
}

func TestAFCStructuredUserInputCallbackReturnsGuardedAnswers(t *testing.T) {
	service := activeAFCService(t)
	writer := &afcWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "ask", 0); err != nil {
		t.Fatal(err)
	}
	event := appserver.Event{Channel: "server_request", Method: "item/tool/requestUserInput", ID: "input-1", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn", "questions": []any{map[string]any{"id": "target", "question": "Where?", "options": []any{map[string]any{"label": "staging"}, map[string]any{"label": "production"}}}},
	}}
	service.handleAFCWriterEvent(ctx, writer, event, service.afcWriter.Snapshot().Generation)
	if len(forum.actions) != 1 || len(forum.actions[0].buttons) != 2 {
		t.Fatalf("actions=%#v", forum.actions)
	}
	token := forum.actions[0].buttons[0][0].CallbackData
	response, err := service.HandleCallback(ctx, -1001, 11, forum.actions[0].messageID, 123456789, token)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "sent") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.respondRequestCalls) != 1 {
		t.Fatalf("responses=%#v", writer.respondRequestCalls)
	}
	answers, _ := writer.respondRequestCalls[0].result["answers"].(map[string]any)
	target, _ := answers["target"].(map[string]any)
	values, _ := target["answers"].([]any)
	if len(values) != 1 || values[0] != "staging" {
		t.Fatalf("answer payload=%#v", writer.respondRequestCalls[0].result)
	}
}

func TestAFCStopInterruptsOnlyCurrentTopicTurn(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleMessageWithID(ctx, -1001, 12, 601, 123456789, "two", 0); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleMessageWithID(ctx, -1001, 11, 700, 123456789, "/stop", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "Stop requested") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnInterruptCalls) != 1 || writer.turnInterruptCalls[0].threadID != "thread-1" {
		t.Fatalf("interrupts=%#v", writer.turnInterruptCalls)
	}
}

func TestAFCSafeOffRefusesActiveTurnsWithoutCleanup(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/afc off", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "refused") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	state, _ := service.store.GetAFCState(ctx)
	if state.State != model.AFCStateActive || len(forum.deletes) != 0 {
		t.Fatalf("state=%#v deletes=%v", state, forum.deletes)
	}
}

func TestAFCForceOffInterruptsAllAndWaitsForTerminalBeforeCleanup(t *testing.T) {
	service := activeAFCService(t)
	service.cfg.RequestTimeout = 200 * time.Millisecond
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleMessageWithID(ctx, -1001, 12, 601, 123456789, "two", 0); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": afcCompletedPayload("thread-1", "started-turn", "one done"), "thread-2": afcCompletedPayload("thread-2", "started-turn", "two done"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/afc off --force", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "AFC off") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnInterruptCalls) != 2 {
		t.Fatalf("interrupts=%#v", writer.turnInterruptCalls)
	}
	state, _ := service.store.GetAFCState(ctx)
	if state.State != model.AFCStateOff || len(forum.deletes) != 2 {
		t.Fatalf("state=%#v deletes=%v", state, forum.deletes)
	}
	observer, _ := service.store.GetState(ctx, "observer.global_enabled")
	if observer != "false" || service.legacyWriter.Snapshot().State != appserver.WriterStopped {
		t.Fatalf("observer=%q legacy=%#v", observer, service.legacyWriter.Snapshot())
	}
}

func TestAFCForceOffTimeoutStaysDrainingAndDoesNotCleanup(t *testing.T) {
	service := activeAFCService(t)
	service.cfg.RequestTimeout = 30 * time.Millisecond
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": afcRunningPayload("thread-1", "started-turn")}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/afc off --force", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "remains draining") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	state, _ := service.store.GetAFCState(ctx)
	if state.State != model.AFCStateDraining || len(forum.deletes) != 0 || service.afcWriter.Snapshot().Accepting {
		t.Fatalf("state=%#v deletes=%v writer=%#v", state, forum.deletes, service.afcWriter.Snapshot())
	}
}

func TestAFCRestartUnknownOwnershipBlocksSafeAndForceCleanup(t *testing.T) {
	service := activeAFCService(t)
	service.cfg.RequestTimeout = 25 * time.Millisecond
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	receipt, _, err := service.store.AcceptAFCMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkAFCStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := service.store.RecoverAFCWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	safe, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/afc off", 0)
	if err != nil || safe == nil || !strings.Contains(safe.Text, "refused") {
		t.Fatalf("safe=%#v err=%v", safe, err)
	}
	forced, err := service.HandleMessageWithID(ctx, -1001, 1, 701, 123456789, "/afc off --force", 0)
	if err != nil || forced == nil || !strings.Contains(forced.Text, "remains draining") {
		t.Fatalf("forced=%#v err=%v", forced, err)
	}
	if len(forum.deletes) != 0 {
		t.Fatalf("unknown ownership was cleaned up: %v", forum.deletes)
	}
}

func afcRunningPayload(threadID, turnID string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "inProgress", "turns": []any{map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}}}
}

func afcCompletedPayload(threadID, turnID, finalText string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "completed", "turns": []any{map[string]any{
		"id": turnID, "status": "completed", "items": []any{map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": finalText}},
	}}}}
}

func activeAFCService(t *testing.T) *Service {
	t.Helper()
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	ctx := context.Background()
	if err := service.store.BeginAFCActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	for index, topicID := range []int64{11, 12} {
		threadID := fmt.Sprintf("thread-%d", index+1)
		if err := service.store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "s", ChatID: -1001, TopicID: topicID, ThreadID: threadID, Title: "Topic", TelegramState: model.AFCTopicConnected}); err != nil {
			t.Fatal(err)
		}
		if err := service.store.UpsertThread(ctx, model.Thread{ID: threadID, Title: "Topic", CWD: "/tmp/project", Status: "idle"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.store.FinishAFCActivation(ctx, "s", `{}`, true); err != nil {
		t.Fatal(err)
	}
	service.SetAFCForum(&fakeAFCForum{})
	return service
}
