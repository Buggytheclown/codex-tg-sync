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
	prepareErr  error
	prepares    int
	nextTopicID int64
	createErrAt int
	renameErr   error
	creates     []string
	renames     []fakeAFCRename
	deletes     []int64
	sends       []fakeAFCSend
	edits       []fakeAFCEdit
	actions     []fakeAFCAction
	onDelete    func()
	onCreate    func(string)
}

type fakeAFCSend struct {
	topicID, messageID int64
	text               string
	silent             bool
}
type fakeAFCRename struct {
	topicID int64
	title   string
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
func (f *fakeAFCForum) PrepareAFCControl(context.Context) error {
	f.prepares++
	return f.prepareErr
}
func (f *fakeAFCForum) CreateAFCTopic(_ context.Context, title string) (int64, error) {
	f.creates = append(f.creates, title)
	if f.onCreate != nil {
		f.onCreate(title)
	}
	if f.createErrAt > 0 && len(f.creates) == f.createErrAt {
		return 0, errors.New("create failed")
	}
	f.nextTopicID++
	return f.nextTopicID, nil
}
func (f *fakeAFCForum) RenameAFCTopic(_ context.Context, topicID int64, title string) error {
	f.renames = append(f.renames, fakeAFCRename{topicID: topicID, title: title})
	return f.renameErr
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
	if forum.prepares != 1 {
		t.Fatalf("Control prepare calls = %d, want 1", forum.prepares)
	}
	if len(forum.creates) != 2 || forum.creates[0] != "New" || forum.creates[1] != "Old" {
		t.Fatalf("creates = %v", forum.creates)
	}
	if !strings.Contains(response.Text, "1. New\nTelegram: connected") || !strings.Contains(response.Text, "2. Old\nTelegram: create outcome unknown") || strings.Index(response.Text, "1. New") > strings.Index(response.Text, "2. Old") {
		t.Fatalf("ordered activation summary = %q", response.Text)
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

func TestAFCActivationFailsClosedWhenControlPreparationFails(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	service.poll = &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "thread-1", "title": "One", "updatedAt": float64(10)},
	}}}
	service.pollConnected = true
	forum := &fakeAFCForum{prepareErr: errors.New("cannot rename General")}
	service.SetAFCForum(forum)

	response, err := service.HandleMessage(context.Background(), -1001, 1, 123456789, "/afc on", 0)
	if err == nil || !strings.Contains(err.Error(), "prepare AFC Control") {
		t.Fatalf("response=%#v err=%v, want Control preparation failure", response, err)
	}
	if forum.prepares != 1 || len(forum.creates) != 0 {
		t.Fatalf("forum prepares=%d creates=%v, want fail before topic creation", forum.prepares, forum.creates)
	}
	state, stateErr := service.store.GetAFCState(context.Background())
	if stateErr != nil || state.State != model.AFCStateOff {
		t.Fatalf("state=%#v err=%v, want off", state, stateErr)
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

func TestAFCPresentationCreatesFreshStatusForEachObservedTurn(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": afcRunningPayloadWithCommentary("thread-1", "turn-1", "first progress"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)

	service.syncAFC(ctx)
	if len(forum.sends) != 1 || forum.sends[0].topicID != 11 || !forum.sends[0].silent {
		t.Fatalf("first turn sends=%#v, want one silent status in topic 11", forum.sends)
	}
	firstStatusID := forum.sends[0].messageID
	firstTopic, err := service.store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || firstTopic == nil || firstTopic.StatusTurnID != "turn-1" || firstTopic.StatusMessageID != firstStatusID {
		t.Fatalf("first turn delivery=%#v err=%v", firstTopic, err)
	}

	poll.threadReads["thread-1"] = afcRunningPayloadWithCommentary("thread-1", "turn-1", "updated progress")
	service.syncAFC(ctx)
	if len(forum.sends) != 1 {
		t.Fatalf("same turn created another status: %#v", forum.sends)
	}
	if len(forum.edits) != 1 || forum.edits[0].messageID != firstStatusID || !strings.Contains(forum.edits[0].text, "updated progress") {
		t.Fatalf("same turn edits=%#v, want existing status %d", forum.edits, firstStatusID)
	}

	poll.threadReads["thread-1"] = afcRunningPayloadWithCommentary("thread-1", "turn-2", "second turn progress")
	service.syncAFC(ctx)
	if len(forum.sends) != 2 {
		t.Fatalf("new turn sends=%#v, want a fresh status message", forum.sends)
	}
	if forum.sends[1].messageID == firstStatusID || !strings.Contains(forum.sends[1].text, "second turn progress") {
		t.Fatalf("new turn status=%#v, want new message after previous turn", forum.sends[1])
	}
	secondTopic, err := service.store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || secondTopic == nil || secondTopic.StatusTurnID != "turn-2" || secondTopic.StatusMessageID != forum.sends[1].messageID {
		t.Fatalf("second turn delivery=%#v err=%v", secondTopic, err)
	}
}

func TestAFCPassiveSyncReconcilesCodexThreadTitleToTopic(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	payload := afcRunningPayloadWithCommentary("thread-1", "turn-1", "progress")
	payload["thread"].(map[string]any)["title"] = "Readable task title"
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": payload}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)

	service.syncAFC(ctx)
	if len(forum.renames) != 1 || forum.renames[0].topicID != 11 || forum.renames[0].title != "Readable task title" {
		t.Fatalf("renames=%#v", forum.renames)
	}
	topic, err := service.store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.Title != "Readable task title" {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}

func TestAFCPresentationIgnoresStalePollTurnWhileAFCWriterIsActive(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	writer := &afcWriterSession{stubSession: &stubSession{threadReads: map[string]map[string]any{
		"thread-1": afcRunningPayloadWithCommentary("thread-1", "started-turn", "live progress"),
	}}, events: make(chan appserver.Event, 2)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	service.handleAFCWriterEvent(ctx, writer, appserver.Event{Method: "item/updated", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn",
	}}, service.afcWriter.Snapshot().Generation)
	if len(forum.sends) != 1 || !strings.Contains(forum.sends[0].text, "live progress") {
		t.Fatalf("live sends=%#v", forum.sends)
	}

	service.mu.Lock()
	service.poll = &stubSession{threadReads: map[string]map[string]any{
		"thread-1": afcRunningPayloadWithCommentary("thread-1", "older-turn", "stale progress"),
	}}
	service.pollConnected = true
	service.mu.Unlock()
	service.syncAFC(ctx)
	if len(forum.sends) != 1 || len(forum.edits) != 0 {
		t.Fatalf("stale poll mutated active presentation: sends=%#v edits=%#v", forum.sends, forum.edits)
	}
	topic, err := service.store.GetActiveAFCTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusTurnID != "started-turn" {
		t.Fatalf("topic=%#v err=%v", topic, err)
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

func TestAFCOffCleansReadyDraftTopic(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	if err := service.store.CreateAFCTopicDraft(ctx, model.AFCTopicDraft{SessionID: "s", ChatID: -1001, TopicID: 21,
		Rank: 3, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 908, 123456789, "/afc off", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "deleted 3 topic") {
		t.Fatalf("response=%#v err=%v deletes=%#v", response, err, forum.deletes)
	}
	if len(forum.deletes) != 3 {
		t.Fatalf("deletes=%#v", forum.deletes)
	}
	drafts, err := service.store.ListAFCTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 0 {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
}

func TestAFCSafeOffRefusesStartingDraft(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	if err := service.store.CreateAFCTopicDraft(ctx, model.AFCTopicDraft{SessionID: "s", ChatID: -1001, TopicID: 21,
		Rank: 3, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.store.ClaimAFCTopicDraftMessage(ctx, -1001, 21, 909); err != nil {
		t.Fatal(err)
	}
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 910, 123456789, "/afc off", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "off refused") || !strings.Contains(response.Text, "New task") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(forum.deletes) != 0 {
		t.Fatalf("deletes=%#v", forum.deletes)
	}
}

func TestAFCDraftRejectsSecondMessageFromCurrentDurableState(t *testing.T) {
	service := activeAFCService(t)
	ctx := context.Background()
	draft := model.AFCTopicDraft{SessionID: "s", ChatID: -1001, TopicID: 21, Rank: 3, Title: "New task", CWD: "/tmp/project"}
	if err := service.store.CreateAFCTopicDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	stale, err := service.store.GetActiveAFCTopicDraft(ctx, -1001, 21)
	if err != nil || stale == nil || stale.State != model.AFCDraftReady {
		t.Fatalf("stale=%#v err=%v", stale, err)
	}
	if _, _, _, err := service.store.ClaimAFCTopicDraftMessage(ctx, -1001, 21, 911); err != nil {
		t.Fatal(err)
	}
	response, err := service.handleAFCDraftMessage(ctx, *stale, 912, "second message")
	if err != nil || response == nil || !strings.Contains(response.Text, "already starting") {
		t.Fatalf("response=%#v err=%v", response, err)
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

func TestAFCTransientInterruptedEventKeepsWriterAndRecoversProgress(t *testing.T) {
	service := activeAFCService(t)
	reads := map[string]map[string]any{
		"thread-1": afcInterruptedPayload("thread-1", "started-turn"),
	}
	writer := &afcWriterSession{stubSession: &stubSession{threadReads: reads}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if !service.isTelegramOriginTurn(ctx, "thread-1", "started-turn") {
		t.Fatal("AFC turn was not marked as Telegram-origin")
	}

	generation := service.afcWriter.Snapshot().Generation
	event := appserver.Event{Method: "turn/completed", Params: map[string]any{"threadId": "thread-1", "turnId": "started-turn"}}
	service.handleAFCWriterEvent(ctx, writer, event, generation)
	if snapshot := service.afcWriter.Snapshot(); snapshot.Active != 1 || snapshot.State != appserver.WriterRunning {
		t.Fatalf("transient interrupted released writer: %#v", snapshot)
	}
	if writer.closeCalls != 0 || len(forum.sends) != 0 {
		t.Fatalf("transient interrupted became visible/terminal: closes=%d sends=%#v", writer.closeCalls, forum.sends)
	}

	reads["thread-1"] = afcRunningPayload("thread-1", "started-turn")
	service.handleAFCWriterEvent(ctx, writer, appserver.Event{Method: "item/updated", Params: map[string]any{"threadId": "thread-1", "turnId": "started-turn"}}, generation)
	if len(forum.sends) != 1 || !strings.Contains(forum.sends[0].text, "inProgress") {
		t.Fatalf("recovered progress not delivered: %#v", forum.sends)
	}
	reads["thread-1"] = afcCompletedPayload("thread-1", "started-turn", "done")
	service.handleAFCWriterEvent(ctx, writer, event, generation)
	if snapshot := service.afcWriter.Snapshot(); snapshot.State != appserver.WriterStopped || snapshot.Active != 0 {
		t.Fatalf("confirmed terminal did not release writer: %#v", snapshot)
	}
	if writer.closeCalls != 1 {
		t.Fatalf("writer close calls=%d, want 1", writer.closeCalls)
	}
}

func TestAFCPollDefersTransientInterrupted(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.poll = &stubSession{threadReads: map[string]map[string]any{
		"thread-1": afcInterruptedPayload("thread-1", "started-turn"),
	}}
	service.pollConnected = true
	service.mu.Unlock()

	service.syncAFC(ctx)
	if snapshot := service.afcWriter.Snapshot(); snapshot.Active != 1 || snapshot.State != appserver.WriterRunning {
		t.Fatalf("poll released transient interrupted writer: %#v", snapshot)
	}
	if writer.closeCalls != 0 {
		t.Fatalf("writer close calls=%d, want 0", writer.closeCalls)
	}
}

func TestAFCExplicitStopInterruptedBypassesTerminalGrace(t *testing.T) {
	service := activeAFCService(t)
	writer := &afcWriterSession{stubSession: &stubSession{threadReads: map[string]map[string]any{
		"thread-1": afcInterruptedPayload("thread-1", "started-turn"),
	}}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 502, 123456789, "/stop", 0); err != nil {
		t.Fatal(err)
	}
	service.handleAFCWriterEvent(ctx, writer, appserver.Event{Method: "turn/completed", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn",
	}}, service.afcWriter.Snapshot().Generation)
	if snapshot := service.afcWriter.Snapshot(); snapshot.State != appserver.WriterStopped || snapshot.Active != 0 {
		t.Fatalf("explicit stop did not bypass grace: %#v", snapshot)
	}
	if writer.closeCalls != 1 {
		t.Fatalf("writer close calls=%d, want 1", writer.closeCalls)
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

func TestAFCProjectPickerCreatesThreadThenTopicThenDurableBinding(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "new-thread", "title": "New task", "cwd": "/tmp/project", "updatedAt": float64(100)}}}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	forum := &fakeAFCForum{nextTopicID: 20}
	service.SetAFCForum(forum)
	menu, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/projects", 0)
	if err != nil || menu == nil || len(menu.Buttons) == 0 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	token := menu.Buttons[0][0].CallbackData
	created, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, token)
	if err != nil || created == nil || !strings.Contains(created.Text, "ready") {
		t.Fatalf("created=%#v err=%v", created, err)
	}
	if len(writer.threadStartCalls) != 0 || len(writer.turnStartCalls) != 0 {
		t.Fatalf("thread starts=%#v turn starts=%#v", writer.threadStartCalls, writer.turnStartCalls)
	}
	drafts, err := service.store.ListAFCTopicDrafts(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].TopicID != 21 || drafts[0].State != model.AFCDraftReady {
		t.Fatalf("drafts=%#v", drafts)
	}
	duplicate, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, token)
	if err != nil || duplicate == nil || !strings.Contains(duplicate.CallbackText, "stale") || len(writer.threadStartCalls) != 0 {
		t.Fatalf("duplicate=%#v calls=%#v err=%v", duplicate, writer.threadStartCalls, err)
	}
	firstPrompt, err := service.HandleMessageWithID(ctx, -1001, 21, 901, 123456789, "first prompt", 0)
	if err != nil || firstPrompt == nil || !strings.Contains(firstPrompt.Text, "started") {
		t.Fatalf("firstPrompt=%#v err=%v", firstPrompt, err)
	}
	if len(writer.turnStartCalls) != 1 || writer.turnStartCalls[0].message != "first prompt" {
		t.Fatalf("turn starts=%#v", writer.turnStartCalls)
	}
	if len(writer.threadStartCalls) != 1 {
		t.Fatalf("thread starts=%#v", writer.threadStartCalls)
	}
	topics, err := service.store.ListAFCTopics(ctx, "s")
	if err != nil || len(topics) != 3 || topics[2].ThreadID != "new-thread" || topics[2].TopicID != 21 {
		t.Fatalf("topics=%#v err=%v", topics, err)
	}
	if len(forum.renames) != 1 || forum.renames[0].topicID != 21 || forum.renames[0].title != "first prompt" {
		t.Fatalf("renames=%#v", forum.renames)
	}
	if len(writer.threadSetNameCalls) != 1 || writer.threadSetNameCalls[0].threadID != "new-thread" || writer.threadSetNameCalls[0].name != "first prompt" {
		t.Fatalf("thread names=%#v", writer.threadSetNameCalls)
	}
}

func TestAFCExistingEmptyTopicRecoversNoRolloutOnNextPrompt(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{
		threadResumeErr:   errors.New("map[code:-32600 message:no rollout found for thread id thread-1]"),
		threadStartResult: map[string]any{"thread": map[string]any{"id": "replacement-thread", "cwd": "/tmp/project"}},
	}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)

	response, err := service.HandleMessageWithID(context.Background(), -1001, 11, 902, 123456789, "replacement prompt", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "started") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.threadResumeCalls) != 1 || writer.threadResumeCalls[0].threadID != "thread-1" {
		t.Fatalf("resume calls=%#v", writer.threadResumeCalls)
	}
	if len(writer.threadStartCalls) != 1 || len(writer.turnStartCalls) != 1 || writer.turnStartCalls[0].threadID != "replacement-thread" {
		t.Fatalf("start calls=%#v turn calls=%#v", writer.threadStartCalls, writer.turnStartCalls)
	}
	topic, err := service.store.GetActiveAFCTopic(context.Background(), -1001, 11)
	if err != nil || topic == nil || topic.ThreadID != "replacement-thread" {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	if len(forum.renames) != 1 || forum.renames[0].title != "replacement prompt" {
		t.Fatalf("renames=%#v", forum.renames)
	}
}

func TestAFCExistingEmptyTopicDoesNotRecoverUnrelatedResumeError(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{
		threadResumeErr:   errors.New("permission denied"),
		threadStartResult: map[string]any{"thread": map[string]any{"id": "must-not-start"}},
	}
	service.liveFactory = func() Session { return writer }
	service.SetAFCForum(&fakeAFCForum{})

	response, err := service.HandleMessageWithID(context.Background(), -1001, 11, 904, 123456789, "do not recover", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "could not resume") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.threadStartCalls) != 0 {
		t.Fatalf("unrelated resume error created replacement: %#v", writer.threadStartCalls)
	}
}

func TestAFCDraftDefinitiveThreadStartFailureCanRetryWithNewMessage(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{threadStartErr: errors.New("invalid cwd")}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{nextTopicID: 20}
	service.SetAFCForum(forum)
	ctx := context.Background()
	menu, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/newchat", 0)
	if err != nil || menu == nil || len(menu.Buttons) == 0 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	if _, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, menu.Buttons[0][0].CallbackData); err != nil {
		t.Fatal(err)
	}
	failed, err := service.HandleMessageWithID(ctx, -1001, 21, 905, 123456789, "first attempt", 0)
	if err != nil || failed == nil || !strings.Contains(failed.Text, "send a new message to retry") {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	drafts, err := service.store.ListAFCTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 1 || drafts[0].State != model.AFCDraftReady || drafts[0].SourceMessageID != 0 {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	receipt, err := service.store.GetAFCReceipt(ctx, 21, 905)
	if err != nil || receipt == nil || receipt.State != model.AFCReceiptRejected {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}

	writer.threadStartErr = nil
	writer.threadStartResult = map[string]any{"thread": map[string]any{"id": "retry-thread", "cwd": "/tmp/project"}}
	retried, err := service.HandleMessageWithID(ctx, -1001, 21, 906, 123456789, "second attempt", 0)
	if err != nil || retried == nil || !strings.Contains(retried.Text, "started") {
		t.Fatalf("retried=%#v err=%v", retried, err)
	}
	if len(writer.threadStartCalls) != 2 || len(writer.turnStartCalls) != 1 {
		t.Fatalf("thread starts=%#v turn starts=%#v", writer.threadStartCalls, writer.turnStartCalls)
	}
}

func TestAFCDraftDefinitiveFirstTurnFailureReturnsTopicToDraft(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "empty-thread", "cwd": "/tmp/project"}},
		turnStartErr:      errors.New("invalid input"),
	}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{nextTopicID: 20}
	service.SetAFCForum(forum)
	ctx := context.Background()
	menu, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/projects", 0)
	if err != nil || menu == nil || len(menu.Buttons) == 0 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	if _, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, menu.Buttons[0][0].CallbackData); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleMessageWithID(ctx, -1001, 21, 907, 123456789, "bad first turn", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "send a new message to retry") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	drafts, err := service.store.ListAFCTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 1 || drafts[0].TopicID != 21 || drafts[0].State != model.AFCDraftReady {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	topic, err := service.store.GetActiveAFCTopic(ctx, -1001, 21)
	if err != nil || topic != nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	receipt, err := service.store.GetAFCReceipt(ctx, 21, 907)
	if err != nil || receipt == nil || receipt.State != model.AFCReceiptRejected {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestAFCControlHelpListsNewTaskCommands(t *testing.T) {
	service := activeAFCService(t)
	response, err := service.HandleMessageWithID(context.Background(), -1001, 1, 903, 123456789, "/threads", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "/projects") || !strings.Contains(response.Text, "/newchat") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	status, err := service.HandleMessageWithID(context.Background(), -1001, 1, 904, 123456789, "/status", 0)
	if err != nil || status == nil || !strings.Contains(status.Text, "New task commands: /projects, /newchat") {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestAFCPromptTopicTitleKeepsUnicodeAndBoundsLength(t *testing.T) {
	if got := afcPromptTopicTitle("  Собери   информацию про Codex server  "); got != "Собери информацию про Codex server" {
		t.Fatalf("title=%q", got)
	}
	got := afcPromptTopicTitle(strings.Repeat("длинное название ", 20))
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > afcPromptTitleMaxRunes+1 {
		t.Fatalf("bounded title=%q runes=%d", got, len([]rune(got)))
	}
}

func TestAFCNewTaskDoesNotCreateThreadWhenTopicCreationFails(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "orphan-thread", "title": "Safe partial", "cwd": "/tmp/project"}}}
	service.liveFactory = func() Session { return writer }
	forum := &fakeAFCForum{createErrAt: 1}
	service.SetAFCForum(forum)
	ctx := context.Background()
	menu, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/newchat", 0)
	if err != nil || menu == nil || len(menu.Buttons) == 0 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	response, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, menu.Buttons[0][0].CallbackData)
	if err != nil || response == nil || !strings.Contains(response.Text, "topic creation failed") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	thread, _ := service.store.GetThread(ctx, "orphan-thread")
	if thread != nil || len(writer.threadStartCalls) != 0 {
		t.Fatalf("topic failure created Codex state: thread=%#v calls=%#v", thread, writer.threadStartCalls)
	}
	topics, _ := service.store.ListAFCTopics(ctx, "s")
	for _, topic := range topics {
		if topic.ThreadID == "orphan-thread" {
			t.Fatalf("failed topic got binding: %#v", topic)
		}
	}
	if service.afcWriter.Snapshot().State != appserver.WriterStopped {
		t.Fatalf("writer=%#v", service.afcWriter.Snapshot())
	}
	duplicate, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, menu.Buttons[0][0].CallbackData)
	if err != nil || duplicate == nil || !strings.Contains(duplicate.CallbackText, "stale") || len(writer.threadStartCalls) != 0 {
		t.Fatalf("duplicate=%#v starts=%#v err=%v", duplicate, writer.threadStartCalls, err)
	}
}

func TestAFCProjectsFailClosedWhileOff(t *testing.T) {
	service := newTestService(t)
	service.cfg.AFCGroupID = -1001
	forum := &fakeAFCForum{}
	service.SetAFCForum(forum)
	response, err := service.HandleMessageWithID(context.Background(), -1001, 1, 700, 123456789, "/projects", 0)
	if err != nil || response == nil || len(response.Buttons) != 0 || len(forum.creates) != 0 {
		t.Fatalf("response=%#v creates=%v err=%v", response, forum.creates, err)
	}
	if service.afcWriter.Snapshot().State != appserver.WriterStopped {
		t.Fatalf("writer=%#v", service.afcWriter.Snapshot())
	}
}

func TestAFCProjectCallbackFromOldSessionFailsBeforeThreadStart(t *testing.T) {
	service := activeAFCService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "must-not-start"}}}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	menu, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/projects", 0)
	if err != nil || menu == nil || len(menu.Buttons) == 0 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	if _, err := service.store.MarkAFCOff(ctx, "s"); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, menu.Buttons[0][0].CallbackData)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "stale") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.threadStartCalls) != 0 {
		t.Fatalf("stale callback started thread: %#v", writer.threadStartCalls)
	}
}

func afcRunningPayload(threadID, turnID string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "inProgress", "turns": []any{map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}}}
}

func afcRunningPayloadWithCommentary(threadID, turnID, commentary string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "inProgress", "turns": []any{map[string]any{
		"id": turnID, "status": "inProgress", "items": []any{map[string]any{"id": turnID + "-commentary", "type": "agentMessage", "phase": "commentary", "text": commentary}},
	}}}}
}

func afcInterruptedPayload(threadID, turnID string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "interrupted", "turns": []any{map[string]any{"id": turnID, "status": "interrupted", "items": []any{}}}}}
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
