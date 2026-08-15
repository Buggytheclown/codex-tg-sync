package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"

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
