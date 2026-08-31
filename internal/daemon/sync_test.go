package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tgformat"
)

type fakeSyncForum struct {
	validateErr      error
	prepareErr       error
	prepares         int
	nextTopicID      int64
	createErrAt      int
	createErr        error
	sendErrAt        int
	sendErr          error
	rejectOversize   bool
	renameErr        error
	messageDeleteErr error
	creates          []string
	renames          []fakeSyncRename
	deletes          []int64
	messageDeletes   []fakeSyncMessageDelete
	sends            []fakeSyncSend
	edits            []fakeSyncEdit
	actions          []fakeSyncAction
	onDelete         func()
	onCreate         func(string)
}

type fakeSyncSend struct {
	topicID, messageID int64
	text               string
	message            model.RenderedMessage
	silent             bool
}
type fakeSyncRename struct {
	topicID int64
	title   string
}
type fakeSyncEdit struct {
	topicID, messageID int64
	text               string
	message            model.RenderedMessage
}
type fakeSyncMessageDelete struct {
	topicID, messageID int64
}
type fakeSyncAction struct {
	topicID, messageID int64
	text               string
	buttons            [][]model.ButtonSpec
}

type syncWriterSession struct {
	*stubSession
	events chan appserver.Event
}

func (s *syncWriterSession) Subscribe() <-chan appserver.Event { return s.events }

type pagedThreadListSession struct {
	*stubSession
	pages   map[string]map[string]any
	cursors []string
}

func (s *pagedThreadListSession) ThreadList(_ context.Context, _ int, cursor string) (map[string]any, error) {
	s.cursors = append(s.cursors, cursor)
	return s.pages[cursor], nil
}

func (f *fakeSyncForum) ValidateSyncGroup(context.Context, int64) error { return f.validateErr }
func (f *fakeSyncForum) PrepareSyncControl(context.Context) error {
	f.prepares++
	return f.prepareErr
}
func (f *fakeSyncForum) CreateSyncTopic(_ context.Context, title string) (int64, error) {
	f.creates = append(f.creates, title)
	if f.onCreate != nil {
		f.onCreate(title)
	}
	if f.createErrAt > 0 && len(f.creates) == f.createErrAt {
		if f.createErr != nil {
			return 0, f.createErr
		}
		return 0, errors.New("create failed")
	}
	f.nextTopicID++
	return f.nextTopicID, nil
}

func (f *fakeSyncForum) CreateLaunchTopic(ctx context.Context, title string) (int64, error) {
	return f.CreateSyncTopic(ctx, title)
}
func (f *fakeSyncForum) RenameSyncTopic(_ context.Context, topicID int64, title string) error {
	f.renames = append(f.renames, fakeSyncRename{topicID: topicID, title: title})
	return f.renameErr
}
func (f *fakeSyncForum) DeleteSyncTopic(_ context.Context, topicID int64) error {
	if f.onDelete != nil {
		f.onDelete()
	}
	f.deletes = append(f.deletes, topicID)
	return nil
}
func (f *fakeSyncForum) DeleteSyncMessage(_ context.Context, topicID, messageID int64) error {
	f.messageDeletes = append(f.messageDeletes, fakeSyncMessageDelete{topicID: topicID, messageID: messageID})
	return f.messageDeleteErr
}
func (f *fakeSyncForum) SendSyncMessage(_ context.Context, topicID int64, message model.RenderedMessage, options model.SendOptions) (int64, error) {
	id := int64(100 + len(f.sends))
	f.sends = append(f.sends, fakeSyncSend{topicID: topicID, messageID: id, text: message.Text, message: message, silent: options.Silent})
	if f.rejectOversize && syncUTF16Len(message.Text) > tgformat.TelegramMessageLimit {
		return 0, errors.New("message is too long")
	}
	if f.sendErrAt > 0 && len(f.sends) == f.sendErrAt {
		if f.sendErr != nil {
			return 0, f.sendErr
		}
		return 0, errors.New("send failed")
	}
	return id, nil
}
func (f *fakeSyncForum) EditSyncMessage(_ context.Context, topicID, messageID int64, message model.RenderedMessage, options model.SendOptions) error {
	f.edits = append(f.edits, fakeSyncEdit{topicID: topicID, messageID: messageID, text: message.Text, message: message})
	return nil
}
func (f *fakeSyncForum) SendSyncActionMessage(_ context.Context, topicID int64, text string, buttons [][]model.ButtonSpec) (int64, error) {
	id := int64(200 + len(f.actions))
	f.actions = append(f.actions, fakeSyncAction{topicID: topicID, messageID: id, text: text, buttons: buttons})
	return id, nil
}

func TestSyncPartialActivationOwnsGroupAndDisablesLegacyObserver(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	poll := &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "thread-old", "title": "Old", "updatedAt": float64(10)},
		map[string]any{"id": "thread-new", "title": "New", "updatedAt": float64(20)},
		map[string]any{"id": "thread-archived", "title": "Archived", "updatedAt": float64(30), "archived": true},
	}}}
	service.poll = poll
	service.pollConnected = true
	forum := &fakeSyncForum{nextTopicID: 10, createErrAt: 2}
	service.SetSyncForum(forum)
	ctx := context.Background()
	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/sync on", 0)
	if err != nil || response != nil {
		t.Fatalf("response=%#v err=%v, want durable response only", response, err)
	}
	if forum.prepares != 1 {
		t.Fatalf("Control prepare calls = %d, want 1", forum.prepares)
	}
	if len(forum.creates) != 2 || forum.creates[0] != "New" || forum.creates[1] != "Old" {
		t.Fatalf("creates = %v", forum.creates)
	}
	state, _ := service.store.GetSyncState(ctx)
	if state.State != model.SyncStateActive {
		t.Fatalf("state = %#v", state)
	}
	var summary syncActivationSummary
	if err := json.Unmarshal([]byte(state.ActivationSummaryJSON), &summary); err != nil ||
		len(summary.Items) != 2 || summary.Items[0].Telegram != "connected" || summary.Items[1].Telegram != "create outcome unknown" {
		t.Fatalf("ordered activation summary=%#v err=%v", summary, err)
	}
	if poll.threadResumeCalls != nil || poll.turnStartCalls != nil {
		t.Fatalf("passive activation mutated app-server: %#v %#v", poll.threadResumeCalls, poll.turnStartCalls)
	}
}

func TestSyncActivationStopsAfterUnknownTopicOutcomeAndQueuesControlSummary(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	service.poll = &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "thread-first", "title": "First", "updatedAt": float64(30)},
		map[string]any{"id": "thread-unknown", "title": "Unknown", "updatedAt": float64(20)},
		map[string]any{"id": "thread-skipped", "title": "Skipped", "updatedAt": float64(10)},
	}}}
	service.pollConnected = true
	forum := &fakeSyncForum{
		nextTopicID: 10,
		createErrAt: 2,
		createErr:   NewSyncForumFailure(SyncForumFailureUnknown, context.DeadlineExceeded),
	}
	service.SetSyncForum(forum)
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()

	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/sync on", 0)
	if err != nil || response != nil {
		t.Fatalf("response=%#v err=%v, want durable response only", response, err)
	}
	if !reflect.DeepEqual(forum.creates, []string{"First", "Unknown"}) {
		t.Fatalf("creates=%#v, want stop after unknown outcome", forum.creates)
	}
	state, err := service.store.GetSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var summary syncActivationSummary
	if err := json.Unmarshal([]byte(state.ActivationSummaryJSON), &summary); err != nil {
		t.Fatalf("activation summary=%q: %v", state.ActivationSummaryJSON, err)
	}
	if summary.Created != 1 || len(summary.Unknown) != 1 || len(summary.Skipped) != 1 || summary.Items[2].Telegram != "skipped after unknown outcome" {
		t.Fatalf("summary=%#v, want one connected, unknown, and skipped", summary)
	}

	service.processDeliveryBatch(ctx)
	if len(sender.messages) != 1 {
		t.Fatalf("Control deliveries=%#v, want one durable summary", sender.messages)
	}
	message := sender.messages[0]
	if message.chatID != -1001 || message.topicID != syncGeneralSendTopicID ||
		!strings.Contains(message.text, "active: 1") || !strings.Contains(message.text, "unknown: 1") || !strings.Contains(message.text, "skipped: 1") {
		t.Fatalf("Control summary=%#v", message)
	}
}

func TestSyncActivationContinuesAfterDefinitiveTopicFailure(t *testing.T) {
	service := newTestService(t)
	service.poll = &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "thread-first", "title": "First", "updatedAt": float64(30)},
		map[string]any{"id": "thread-failed", "title": "Failed", "updatedAt": float64(20)},
		map[string]any{"id": "thread-third", "title": "Third", "updatedAt": float64(10)},
	}}}
	service.pollConnected = true
	forum := &fakeSyncForum{
		nextTopicID: 10,
		createErrAt: 2,
		createErr:   NewSyncForumFailure(SyncForumFailureDefinitive, errors.New("bad topic title")),
	}
	service.SetSyncForum(forum)
	ctx := context.Background()

	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/sync on", 0)
	if err != nil || response != nil {
		t.Fatalf("response=%#v err=%v, want durable response only", response, err)
	}
	if !reflect.DeepEqual(forum.creates, []string{"First", "Failed", "Third"}) {
		t.Fatalf("creates=%#v, want definitive failure not to stop activation", forum.creates)
	}
	state, err := service.store.GetSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var summary syncActivationSummary
	if err := json.Unmarshal([]byte(state.ActivationSummaryJSON), &summary); err != nil ||
		summary.Created != 2 || len(summary.Failed) != 1 || len(summary.Unknown) != 0 || len(summary.Skipped) != 0 {
		t.Fatalf("summary=%#v err=%v", summary, err)
	}
}

func TestSyncActivationUsesConfiguredInitialTopicLimit(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	service.cfg.SyncInitialTopicLimit = 5
	items := make([]any, 0, 7)
	statuses := []string{"completed", "waitingOnApproval", "notLoaded", "notLoaded", "", "completed", "notLoaded"}
	for i := 1; i <= 7; i++ {
		item := map[string]any{
			"id": fmt.Sprintf("thread-%d", i), "title": fmt.Sprintf("Thread %d", i), "updatedAt": float64(i), "status": statuses[i-1],
		}
		if turnStatus := map[int]string{3: "inProgress", 4: "failed", 7: "completed"}[i]; turnStatus != "" {
			item["turns"] = []any{map[string]any{"id": "turn-" + turnStatus, "status": turnStatus}}
		}
		items = append(items, item)
	}
	service.poll = &stubSession{threadListResult: map[string]any{"data": items}}
	service.pollConnected = true
	forum := &fakeSyncForum{nextTopicID: 10}
	service.SetSyncForum(forum)

	response, err := service.HandleMessage(context.Background(), -1001, 1, 123456789, "/sync on", 0)
	if err != nil || response != nil {
		t.Fatalf("response=%#v err=%v, want durable response only", response, err)
	}
	if len(forum.creates) != 5 {
		t.Fatalf("creates=%#v, want five", forum.creates)
	}
	want := []string{"Thread 3", "Thread 2", "Thread 5", "Thread 4", "Thread 7"}
	if !reflect.DeepEqual(forum.creates, want) {
		t.Fatalf("creates=%#v, want importance-first limit %#v", forum.creates, want)
	}
}

func TestSortSyncActivationThreadsByWorkPriorityAndRecency(t *testing.T) {
	threads := []model.Thread{
		{ID: "completed-new", Status: "completed", UpdatedAt: 900},
		{ID: "failed-new", Status: "failed", UpdatedAt: 800},
		{ID: "unknown-new", Status: "", UpdatedAt: 700},
		{ID: "running-old", Status: "notLoaded", ActiveTurnID: "active-turn", UpdatedAt: 100},
		{ID: "waiting-new", Status: "waitingOnApproval", UpdatedAt: 600},
		{ID: "running-new", Status: "running", UpdatedAt: 500},
		{ID: "completed-old", Status: "completed", UpdatedAt: 200},
		{ID: "failed-old", Status: "interrupted", UpdatedAt: 300},
		{ID: "same-b", Status: "completed", UpdatedAt: 50},
		{ID: "same-a", Status: "completed", UpdatedAt: 50},
	}

	sortSyncActivationThreads(threads)

	got := make([]string, 0, len(threads))
	for _, thread := range threads {
		got = append(got, thread.ID)
	}
	want := []string{
		"waiting-new", "running-new", "running-old",
		"unknown-new",
		"failed-new", "failed-old",
		"completed-new", "completed-old", "same-a", "same-b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("activation order = %#v, want %#v", got, want)
	}
}

func TestSyncActivationFindsRunningThreadOnLaterListPage(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	service.cfg.SyncInitialTopicLimit = 1
	poll := &pagedThreadListSession{
		stubSession: &stubSession{},
		pages: map[string]map[string]any{
			"": {
				"data":       []any{map[string]any{"id": "completed-new", "title": "Completed New", "status": "completed", "updatedAt": float64(900)}},
				"nextCursor": "page-2",
			},
			"page-2": {
				"data": []any{map[string]any{
					"id": "running-old", "title": "Running Old", "status": "notLoaded", "updatedAt": float64(100),
					"turns": []any{map[string]any{"id": "active-turn", "status": "inProgress"}},
				}},
			},
		},
	}
	service.poll = poll
	service.pollConnected = true
	forum := &fakeSyncForum{nextTopicID: 10}
	service.SetSyncForum(forum)

	response, err := service.HandleMessage(context.Background(), -1001, 1, 123456789, "/sync on", 0)
	if err != nil || response != nil {
		t.Fatalf("response=%#v err=%v, want durable response only", response, err)
	}
	if !reflect.DeepEqual(poll.cursors, []string{"", "page-2"}) {
		t.Fatalf("thread/list cursors=%#v", poll.cursors)
	}
	if !reflect.DeepEqual(forum.creates, []string{"Running Old"}) {
		t.Fatalf("creates=%#v, want running thread from later page", forum.creates)
	}
}

func TestSyncReconcileDiscoversNewDesktopThreadExactlyOnceAndResubscribesAfterReconnect(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = "daemon"
	ctx := context.Background()
	state, err := service.store.GetSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := parseTime(state.SnapshotAt).Unix()
	poll := &stubSession{
		threadListResult: map[string]any{"data": []any{
			map[string]any{"id": "thread-1", "title": "Existing", "createdAt": float64(cutoff - 1), "updatedAt": float64(cutoff + 2)},
			map[string]any{"id": "thread-new", "title": "Desktop task", "createdAt": float64(cutoff), "updatedAt": float64(cutoff + 1)},
			map[string]any{"id": "thread-old", "title": "Old untracked", "createdAt": float64(cutoff - 1), "updatedAt": float64(cutoff + 3)},
		}},
		threadReads: map[string]map[string]any{
			"thread-1":   syncRunningPayload("thread-1", "turn-1"),
			"thread-2":   syncRunningPayload("thread-2", "turn-2"),
			"thread-new": syncRunningPayloadWithCommentary("thread-new", "turn-new", "desktop progress"),
		},
	}
	service.mu.Lock()
	service.poll, service.pollConnected, service.pollGeneration = poll, true, 7
	service.mu.Unlock()
	forum := &fakeSyncForum{nextTopicID: 20}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	service.reconcileSync(ctx)

	if len(forum.creates) != 1 || forum.creates[0] != "Desktop task" {
		t.Fatalf("creates=%#v, want one new Desktop topic", forum.creates)
	}
	if topic, err := service.store.GetActiveSyncTopicByThread(ctx, "s", "thread-new"); err != nil || topic == nil || topic.TopicID != 21 {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	if topic, err := service.store.GetActiveSyncTopicByThread(ctx, "s", "thread-old"); err != nil || topic != nil {
		t.Fatalf("old topic=%#v err=%v, want activation cutoff preserved", topic, err)
	}
	if len(poll.threadResumeCalls) != 3 {
		t.Fatalf("resume calls=%#v, want each connected thread once", poll.threadResumeCalls)
	}

	service.mu.Lock()
	service.pollGeneration = 8
	service.mu.Unlock()
	service.reconcileSync(ctx)
	if len(poll.threadResumeCalls) != 6 {
		t.Fatalf("resume calls after reconnect=%#v, want one resubscribe per thread", poll.threadResumeCalls)
	}
}

func TestSyncRefreshControlCommandReportsDiscoveryAndHelpAdvertisesIt(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	state, err := service.store.GetSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := parseTime(state.SnapshotAt).Unix()
	service.mu.Lock()
	service.poll = &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "thread-new", "title": "Manual sync", "createdAt": float64(cutoff), "updatedAt": float64(cutoff + 1)},
	}}}
	service.pollConnected = true
	service.mu.Unlock()
	forum := &fakeSyncForum{nextTopicID: 30}
	service.SetSyncForum(forum)

	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/refresh", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "discovered: 1") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	help, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/unknown", 0)
	if err != nil || help == nil || !strings.Contains(help.Text, "/refresh") {
		t.Fatalf("help=%#v err=%v", help, err)
	}
}

func TestSyncActivationFailsClosedWhenControlPreparationFails(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	service.poll = &stubSession{threadListResult: map[string]any{"data": []any{
		map[string]any{"id": "thread-1", "title": "One", "updatedAt": float64(10)},
	}}}
	service.pollConnected = true
	forum := &fakeSyncForum{prepareErr: errors.New("cannot rename General")}
	service.SetSyncForum(forum)

	response, err := service.HandleMessage(context.Background(), -1001, 1, 123456789, "/sync on", 0)
	if err == nil || !strings.Contains(err.Error(), "prepare Sync Control") {
		t.Fatalf("response=%#v err=%v, want Control preparation failure", response, err)
	}
	if forum.prepares != 1 || len(forum.creates) != 0 {
		t.Fatalf("forum prepares=%d creates=%v, want fail before topic creation", forum.prepares, forum.creates)
	}
	state, stateErr := service.store.GetSyncState(context.Background())
	if stateErr != nil || state.State != model.SyncStateOff {
		t.Fatalf("state=%#v err=%v, want off", state, stateErr)
	}
}

func TestSyncUnknownTopicAndCallbacksFailClosed(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	response, err := service.HandleMessage(context.Background(), -1001, 999, 123456789, "hello", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "Sync") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	response, err = service.HandleCallback(context.Background(), -1001, 999, 5, 123456789, "legacy-token")
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "Sync") {
		t.Fatalf("callback=%#v err=%v", response, err)
	}
}

func TestSyncZeroTopicActivationReturnsToOff(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	service.poll = &stubSession{threadListResult: map[string]any{"data": []any{map[string]any{"id": "thread-1", "title": "One", "updatedAt": float64(10)}}}}
	service.pollConnected = true
	service.SetSyncForum(&fakeSyncForum{createErrAt: 1})
	sender := &recordingSender{}
	service.SetSender(sender)
	ctx := context.Background()
	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/sync on", 0)
	if err != nil || response != nil {
		t.Fatalf("response=%#v err=%v, want durable response only", response, err)
	}
	service.processDeliveryBatch(ctx)
	if len(sender.messages) != 1 || !strings.Contains(sender.messages[0].text, "active: 0") {
		t.Fatalf("Control deliveries=%#v", sender.messages)
	}
	state, _ := service.store.GetSyncState(ctx)
	if state.State != model.SyncStateOff {
		t.Fatalf("state=%#v", state)
	}
}

func TestSyncPassiveSyncSendsSilentStatusAndNotifyingFinal(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	ctx := context.Background()
	if err := service.store.BeginSyncActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	if err := service.store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "s", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", Title: "One", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.FinishSyncActivation(ctx, "s", `{}`, true); err != nil {
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
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	service.reconcileSync(ctx)
	if len(forum.sends) != 2 || !forum.sends[0].silent || forum.sends[1].silent {
		t.Fatalf("sends = %#v", forum.sends)
	}
	if !strings.HasPrefix(forum.sends[1].text, syncFinalHeader+"\n") || !strings.Contains(forum.sends[1].text, "done") {
		t.Fatalf("final = %q", forum.sends[1].text)
	}
	if len(poll.threadResumeCalls) != 0 || len(poll.turnStartCalls) != 0 {
		t.Fatal("passive sync attempted a mutation")
	}
}

func TestSyncLongFinalKeepsFinalHeaderOnEveryChunk(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	finalText := strings.Repeat("🙂", tgformat.TelegramMessageLimit/2+600)
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncCompletedPayload("thread-1", "turn-1", finalText),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{rejectOversize: true}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) < 3 {
		t.Fatalf("sends=%d, want status and multiple Final chunks", len(forum.sends))
	}
	finalSends := forum.sends[1:]
	var delivered strings.Builder
	for index, send := range finalSends {
		if got := syncUTF16Len(send.text); got > tgformat.TelegramMessageLimit {
			t.Fatalf("chunk %d UTF-16 length=%d, want <=%d", index+1, got, tgformat.TelegramMessageLimit)
		}
		prefix := syncFinalHeader + "\n"
		if !strings.HasPrefix(send.text, prefix) {
			t.Fatalf("chunk %d = %q, want Final header for topic preview", index+1, send.text)
		}
		delivered.WriteString(strings.TrimPrefix(send.text, prefix))
	}
	if got, want := delivered.String(), finalText; got != want {
		t.Fatalf("delivered Final length=%d, want exact length=%d", len(got), len(want))
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.LastFinalFP == "" {
		t.Fatalf("topic=%#v err=%v, want committed Final fingerprint", topic, err)
	}
	deliveredCount := len(forum.sends)
	service.reconcileSync(ctx)
	if len(forum.sends) != deliveredCount {
		t.Fatalf("sends=%d after retry, want deduped count=%d", len(forum.sends), deliveredCount)
	}
}

func TestSyncLongFinalFailureKeepsFingerprintPending(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	finalText := strings.Repeat("x", tgformat.TelegramMessageLimit+1200)
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncCompletedPayload("thread-1", "turn-1", finalText),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{sendErrAt: 3, sendErr: errors.New("temporary Telegram failure")}
	service.SetSyncForum(forum)
	var logs bytes.Buffer
	service.SetLogger(log.New(&logs, "", 0))

	service.reconcileSync(ctx)
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	if topic.LastFinalFP != "" {
		t.Fatalf("last_final_fp=%q, want pending after failed continuation", topic.LastFinalFP)
	}
	if got := logs.String(); !strings.Contains(got, "sync_final_delivery_failed") || !strings.Contains(got, `"chunk_index":2`) {
		t.Fatalf("logs=%q, want chunk delivery diagnostic", got)
	}
}

func TestSyncPresentationCreatesFreshStatusForEachObservedTurn(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "turn-1", "first progress"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 1 || forum.sends[0].topicID != 11 || !forum.sends[0].silent {
		t.Fatalf("first turn sends=%#v, want one silent status in topic 11", forum.sends)
	}
	firstStatusID := forum.sends[0].messageID
	firstTopic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || firstTopic == nil || firstTopic.StatusTurnID != "turn-1" || firstTopic.StatusMessageID != firstStatusID {
		t.Fatalf("first turn delivery=%#v err=%v", firstTopic, err)
	}

	poll.threadReads["thread-1"] = syncRunningPayloadWithCommentary("thread-1", "turn-1", "updated progress")
	service.reconcileSync(ctx)
	if len(forum.sends) != 1 {
		t.Fatalf("same turn created another status: %#v", forum.sends)
	}
	if len(forum.edits) != 1 || forum.edits[0].messageID != firstStatusID || !strings.Contains(forum.edits[0].text, "updated progress") {
		t.Fatalf("same turn edits=%#v, want existing status %d", forum.edits, firstStatusID)
	}

	poll.threadReads["thread-1"] = syncRunningPayloadWithCommentary("thread-1", "turn-2", "second turn progress")
	service.reconcileSync(ctx)
	if len(forum.sends) != 2 {
		t.Fatalf("new turn sends=%#v, want a fresh status message", forum.sends)
	}
	if forum.sends[1].messageID == firstStatusID || !strings.Contains(forum.sends[1].text, "second turn progress") {
		t.Fatalf("new turn status=%#v, want new message after previous turn", forum.sends[1])
	}
	secondTopic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || secondTopic == nil || secondTopic.StatusTurnID != "turn-2" || secondTopic.StatusMessageID != forum.sends[1].messageID {
		t.Fatalf("second turn delivery=%#v err=%v", secondTopic, err)
	}
}

func TestSyncPassiveSyncMirrorsDesktopUserBeforeStatusExactlyOnce(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithUser("thread-1", "turn-1", "user-1", "Desktop prompt", "working"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 2 {
		t.Fatalf("sends=%#v, want user then status", forum.sends)
	}
	if !forum.sends[0].silent || forum.sends[0].text != syncUserHeader+"\nDesktop prompt" {
		t.Fatalf("user mirror=%#v", forum.sends[0])
	}
	if !forum.sends[1].silent || !strings.HasPrefix(forum.sends[1].text, syncStatusHeader+" ") {
		t.Fatalf("status=%#v", forum.sends[1])
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.LastUserFP == "" || topic.StatusMessageID != forum.sends[1].messageID {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}

	service.reconcileSync(ctx)
	if len(forum.sends) != 2 {
		t.Fatalf("repeat poll duplicated user/status: %#v", forum.sends)
	}
}

func TestSyncSameTurnDesktopUserReanchorsStatusAfterUser(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithUser("thread-1", "turn-1", "user-1", "First prompt", "working"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	oldStatusID := forum.sends[1].messageID
	poll.threadReads["thread-1"] = syncRunningPayloadWithUsers("thread-1", "turn-1", []syncTestUser{
		{id: "user-1", text: "First prompt"},
		{id: "user-2", text: "Desktop follow-up"},
	}, "updated progress")
	service.reconcileSync(ctx)

	if len(forum.sends) != 4 || forum.sends[2].text != syncUserHeader+"\nDesktop follow-up" || !strings.HasPrefix(forum.sends[3].text, syncStatusHeader+" ") {
		t.Fatalf("sends=%#v, want follow-up user then reanchored status", forum.sends)
	}
	if len(forum.messageDeletes) != 1 || forum.messageDeletes[0].messageID != oldStatusID {
		t.Fatalf("status deletes=%#v, want old status %d", forum.messageDeletes, oldStatusID)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusMessageID != forum.sends[3].messageID {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}

func TestSyncTelegramUserIsNotEchoedByPassiveSync(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	pending := syncUserTextFingerprint("turn-1", "Telegram prompt")
	if err := service.store.UpdateSyncTopicUserDelivery(ctx, "s", 11, "", "turn-1", pending); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithUser("thread-1", "turn-1", "user-tg", "Telegram prompt", "working"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 1 || !strings.HasPrefix(forum.sends[0].text, syncStatusHeader+" ") {
		t.Fatalf("sends=%#v, want status without Telegram user echo", forum.sends)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.LastUserFP == "" || topic.PendingTelegramUserFP != "" || topic.PendingTelegramTurnID != "" {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}

func TestSyncPendingTelegramUserDefersStaleDesktopSnapshot(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	pending := syncUserTextFingerprint("turn-1", "Telegram steer")
	if err := service.store.UpdateSyncTopicUserDelivery(ctx, "s", 11, "old-user-fp", "turn-1", pending); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithUser("thread-1", "turn-1", "user-old", "Old Desktop prompt", "working"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 0 {
		t.Fatalf("stale snapshot delivered while Telegram input pending: %#v", forum.sends)
	}
	poll.threadReads["thread-1"] = syncRunningPayloadWithUsers("thread-1", "turn-1", []syncTestUser{
		{id: "user-old", text: "Old Desktop prompt"},
		{id: "user-tg", text: "Telegram steer"},
	}, "working")
	service.reconcileSync(ctx)
	if len(forum.sends) != 1 || !strings.HasPrefix(forum.sends[0].text, syncStatusHeader+" ") {
		t.Fatalf("resolved pending input sends=%#v, want status only", forum.sends)
	}
}

func TestSyncDirectDeliveryReanchorsSameTurnStatusAtTopicTail(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "turn-1", "progress"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 1 {
		t.Fatalf("initial sends=%#v, want one status", forum.sends)
	}
	oldStatusID := forum.sends[0].messageID
	if err := service.RegisterDirectDelivery(ctx, -1001, 11, 501, &DirectResponse{
		Text:     "Sync input steered to active turn: turn-1",
		ThreadID: "thread-1",
		TurnID:   "turn-1",
	}); err != nil {
		t.Fatalf("RegisterDirectDelivery failed: %v", err)
	}
	if len(forum.sends) != 2 {
		t.Fatalf("sends=%#v, want fresh tail status after direct acknowledgement", forum.sends)
	}
	newStatusID := forum.sends[1].messageID
	if newStatusID == oldStatusID || !strings.Contains(forum.sends[1].text, "progress") {
		t.Fatalf("new status=%#v, want fresh progress message after %d", forum.sends[1], oldStatusID)
	}
	if len(forum.messageDeletes) != 1 || forum.messageDeletes[0].topicID != 11 || forum.messageDeletes[0].messageID != oldStatusID {
		t.Fatalf("message deletes=%#v, want old live status %d deleted", forum.messageDeletes, oldStatusID)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusMessageID != newStatusID || topic.StatusTurnID != "turn-1" {
		t.Fatalf("topic=%#v err=%v, want new status anchor %d", topic, err, newStatusID)
	}
}

func TestSyncDirectDeliveryKeepsPreviousTurnStatusHistory(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "turn-1", "first progress"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	service.reconcileSync(ctx)
	oldStatusID := forum.sends[0].messageID

	poll.threadReads["thread-1"] = syncRunningPayloadWithCommentary("thread-1", "turn-2", "second progress")
	if err := service.RegisterDirectDelivery(ctx, -1001, 11, 502, &DirectResponse{
		Text: "Sync turn started: turn-2", ThreadID: "thread-1", TurnID: "turn-2",
	}); err != nil {
		t.Fatalf("RegisterDirectDelivery failed: %v", err)
	}
	if len(forum.messageDeletes) != 0 {
		t.Fatalf("previous-turn status was deleted: %#v", forum.messageDeletes)
	}
	if len(forum.sends) != 2 || forum.sends[1].messageID == oldStatusID || !strings.Contains(forum.sends[1].text, "second progress") {
		t.Fatalf("sends=%#v, want retained history and fresh turn-2 status", forum.sends)
	}
}

func TestSyncDirectDeliveryDeleteFailureStillCreatesTailStatus(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "turn-1", "progress"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{messageDeleteErr: errors.New("delete failed")}
	service.SetSyncForum(forum)
	service.reconcileSync(ctx)

	if err := service.RegisterDirectDelivery(ctx, -1001, 11, 503, &DirectResponse{
		Text: "Sync input steered to active turn: turn-1", ThreadID: "thread-1", TurnID: "turn-1",
	}); err != nil {
		t.Fatalf("RegisterDirectDelivery failed after best-effort delete: %v", err)
	}
	if len(forum.messageDeletes) != 1 || len(forum.sends) != 2 {
		t.Fatalf("deletes=%#v sends=%#v, want attempted delete and fresh status", forum.messageDeletes, forum.sends)
	}
}

func TestSyncPassiveSyncReconcilesCodexThreadTitleToTopic(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	payload := syncRunningPayloadWithCommentary("thread-1", "turn-1", "progress")
	payload["thread"].(map[string]any)["title"] = "Readable task title"
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": payload}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.renames) != 1 || forum.renames[0].topicID != 11 || forum.renames[0].title != "Readable task title" {
		t.Fatalf("renames=%#v", forum.renames)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.Title != "Readable task title" {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}

func TestSyncTopicRenameReanchorsActiveStatusWithoutLosingAggregate(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 24, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	payload := syncRunningPayloadWithCommentaries("thread-1", "turn-1", "first block", "second block", "third block")
	payload["thread"].(map[string]any)["title"] = "Topic"
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": payload}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 1 {
		t.Fatalf("initial sends=%#v, want one aggregate status", forum.sends)
	}
	oldStatus := forum.sends[0]
	before, err := service.store.GetSnapshot(ctx, "thread-1")
	if err != nil || before == nil {
		t.Fatalf("initial snapshot=%#v err=%v", before, err)
	}
	var beforeSnapshot appserver.ThreadReadSnapshot
	if err := json.Unmarshal(before.CompactJSON, &beforeSnapshot); err != nil {
		t.Fatal(err)
	}

	payload["thread"].(map[string]any)["title"] = "Renamed task"
	service.reconcileSync(ctx)

	if len(forum.renames) != 1 || forum.renames[0].title != "Renamed task" {
		t.Fatalf("renames=%#v", forum.renames)
	}
	if len(forum.messageDeletes) != 1 || forum.messageDeletes[0].messageID != oldStatus.messageID {
		t.Fatalf("message deletes=%#v, want old status %d", forum.messageDeletes, oldStatus.messageID)
	}
	if len(forum.sends) != 2 || forum.sends[1].messageID == oldStatus.messageID {
		t.Fatalf("sends=%#v, want fresh status after rename", forum.sends)
	}
	if forum.sends[1].text != oldStatus.text {
		t.Fatalf("reanchored status changed\nbefore: %q\nafter:  %q", oldStatus.text, forum.sends[1].text)
	}
	for _, block := range []string{"first block", "second block", "third block"} {
		if strings.Count(forum.sends[1].text, block) != 1 {
			t.Fatalf("reanchored status=%q, want exactly one %q", forum.sends[1].text, block)
		}
	}
	after, err := service.store.GetSnapshot(ctx, "thread-1")
	if err != nil || after == nil {
		t.Fatalf("updated snapshot=%#v err=%v", after, err)
	}
	var afterSnapshot appserver.ThreadReadSnapshot
	if err := json.Unmarshal(after.CompactJSON, &afterSnapshot); err != nil {
		t.Fatal(err)
	}
	if len(afterSnapshot.DetailItems) != len(beforeSnapshot.DetailItems) {
		t.Fatalf("detail items changed from %d to %d", len(beforeSnapshot.DetailItems), len(afterSnapshot.DetailItems))
	}
	for index := range beforeSnapshot.DetailItems {
		if afterSnapshot.DetailItems[index].ID != beforeSnapshot.DetailItems[index].ID ||
			afterSnapshot.DetailItems[index].Text != beforeSnapshot.DetailItems[index].Text ||
			afterSnapshot.DetailItems[index].StartedAt != beforeSnapshot.DetailItems[index].StartedAt {
			t.Fatalf("detail item %d changed: before=%#v after=%#v", index, beforeSnapshot.DetailItems[index], afterSnapshot.DetailItems[index])
		}
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusMessageID != forum.sends[1].messageID || topic.StatusTurnID != "turn-1" {
		t.Fatalf("topic=%#v err=%v, want reanchored turn-1 status", topic, err)
	}
}

func TestSyncTopicRenameKeepsPreviousTurnStatusHistory(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	payload := syncRunningPayloadWithCommentary("thread-1", "turn-1", "first turn")
	payload["thread"].(map[string]any)["title"] = "Topic"
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": payload}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	oldStatusID := forum.sends[0].messageID
	secondTurn := syncRunningPayloadWithCommentary("thread-1", "turn-2", "second turn")
	secondTurn["thread"].(map[string]any)["title"] = "Renamed task"
	poll.threadReads["thread-1"] = secondTurn
	service.reconcileSync(ctx)

	if len(forum.renames) != 1 || len(forum.messageDeletes) != 0 {
		t.Fatalf("renames=%#v deletes=%#v, want rename without deleting previous turn", forum.renames, forum.messageDeletes)
	}
	if len(forum.sends) != 2 || forum.sends[1].messageID == oldStatusID || !strings.Contains(forum.sends[1].text, "second turn") {
		t.Fatalf("sends=%#v, want retained turn-1 status and fresh turn-2 status", forum.sends)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusMessageID != forum.sends[1].messageID || topic.StatusTurnID != "turn-2" {
		t.Fatalf("topic=%#v err=%v, want turn-2 status anchor", topic, err)
	}
}

func TestSyncPresentationIgnoresStalePollTurnWhileSyncWriterIsActive(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	writer := &syncWriterSession{stubSession: &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "started-turn", "live progress"),
	}}, events: make(chan appserver.Event, 2)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	service.handleSyncWriterEvent(ctx, writer, appserver.Event{Method: "item/updated", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn",
	}}, service.syncWriter.Snapshot().Generation)
	if len(forum.sends) != 1 || !strings.Contains(forum.sends[0].text, "live progress") {
		t.Fatalf("live sends=%#v", forum.sends)
	}

	service.mu.Lock()
	service.poll = &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "older-turn", "stale progress"),
	}}
	service.pollConnected = true
	service.mu.Unlock()
	service.reconcileSync(ctx)
	if len(forum.sends) != 1 || len(forum.edits) != 0 {
		t.Fatalf("stale poll mutated active presentation: sends=%#v edits=%#v", forum.sends, forum.edits)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusTurnID != "started-turn" {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
}

func TestSyncStatusUsesCompactTimingInHeader(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	active := appserver.ThreadReadSnapshot{
		Thread:              model.Thread{Status: "inProgress", LastPreview: "must not leak into status"},
		LatestTurnID:        "turn-1",
		LatestTurnStatus:    "inProgress",
		LatestTurnStartedAt: startedAt.Format(time.RFC3339Nano),
	}
	if got := renderSyncStatusAt(active, startedAt.Add(8*time.Second)).Text; got != "⏱ [Status] inProgress · 8s" {
		t.Fatalf("active status = %q, want compact elapsed header", got)
	}

	active.LatestTurnStatus = "completed"
	active.LatestTurnUpdatedAt = startedAt.Add(2 * time.Minute).Format(time.RFC3339Nano)
	if got := renderSyncStatusAt(active, startedAt.Add(5*time.Minute)).Text; got != "⏱ [Status] completed · 2m" {
		t.Fatalf("terminal status = %q, want compact duration header", got)
	}
}

func TestSyncStatusAggregatesCommentaryBlocksInOneMessage(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentaries("thread-1", "turn-1", "first block"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 1 || !strings.Contains(forum.sends[0].text, "first block") {
		t.Fatalf("initial status sends=%#v, want first commentary block", forum.sends)
	}
	statusID := forum.sends[0].messageID

	poll.threadReads["thread-1"] = syncRunningPayloadWithCommentaries("thread-1", "turn-1", "first block", "newest block")
	service.reconcileSync(ctx)
	if len(forum.sends) != 1 {
		t.Fatalf("new commentary created another status: %#v", forum.sends)
	}
	if len(forum.edits) != 1 || forum.edits[0].messageID != statusID {
		t.Fatalf("new commentary edits=%#v, want existing status %d", forum.edits, statusID)
	}
	if !strings.Contains(forum.edits[0].text, "Блок 1 ·") || !strings.Contains(forum.edits[0].text, "first block") ||
		!strings.Contains(forum.edits[0].text, "Блок 2 ·") || !strings.Contains(forum.edits[0].text, "newest block") {
		t.Fatalf("updated status=%q, want both commentary blocks in order", forum.edits[0].text)
	}
}

func TestSyncStatusUpdatesSameBlockWithoutDuplicatingAndExcludesTools(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	snapshot := appserver.ThreadReadSnapshot{
		Thread:              model.Thread{Status: "inProgress", LastPreview: "stale preview"},
		LatestTurnID:        "turn-1",
		LatestTurnStatus:    "inProgress",
		LatestTurnStartedAt: startedAt.Format(time.RFC3339Nano),
		DetailItems: []model.DetailItem{
			{ID: "commentary-1", Kind: model.DetailItemCommentary, Text: "Expanded reasoning", StartedAt: model.TimeString(startedAt.Format(time.RFC3339Nano))},
			{ID: "tool-1", Kind: model.DetailItemTool, Label: "go test ./...", Status: "running"},
			{ID: "output-1", Kind: model.DetailItemOutput, Output: "tool output"},
			{ID: "plan-1", Kind: model.DetailItemPlan, Text: "Updated plan", StartedAt: model.TimeString(startedAt.Add(7 * time.Second).Format(time.RFC3339Nano))},
		},
	}

	message := renderSyncStatusAt(snapshot, startedAt.Add(10*time.Second))
	if strings.Count(message.Text, "Блок 1 ·") != 1 || strings.Count(message.Text, "Expanded reasoning") != 1 {
		t.Fatalf("status duplicated updated block: %q", message.Text)
	}
	if !strings.Contains(message.Text, "Updated plan") || strings.Contains(message.Text, "go test") || strings.Contains(message.Text, "tool output") || strings.Contains(message.Text, "stale preview") {
		t.Fatalf("status included wrong detail kinds: %q", message.Text)
	}
}

func TestSyncStatusBlockDurationsPartitionOverallDuration(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	snapshot := appserver.ThreadReadSnapshot{
		Thread:              model.Thread{Status: "inProgress"},
		LatestTurnID:        "turn-1",
		LatestTurnStatus:    "inProgress",
		LatestTurnStartedAt: startedAt.Format(time.RFC3339Nano),
		DetailItems: []model.DetailItem{
			{ID: "block-1", Kind: model.DetailItemCommentary, Text: "one", StartedAt: model.TimeString(startedAt.Format(time.RFC3339Nano))},
			{ID: "block-2", Kind: model.DetailItemCommentary, Text: "two", StartedAt: model.TimeString(startedAt.Add(10 * time.Second).Format(time.RFC3339Nano))},
			{ID: "block-3", Kind: model.DetailItemPlan, Text: "three", StartedAt: model.TimeString(startedAt.Add(25 * time.Second).Format(time.RFC3339Nano))},
		},
	}

	message := renderSyncStatusAt(snapshot, startedAt.Add(30*time.Second))
	for _, want := range []string{
		"⏱ [Status] inProgress · 30s",
		"Блок 1 · 10s\none",
		"Блок 2 · 15s\ntwo",
		"Блок 3 · 5s\nthree",
	} {
		if !strings.Contains(message.Text, want) {
			t.Fatalf("status %q does not contain %q", message.Text, want)
		}
	}
}

func TestSyncCompletedStatusCollapsesBodyAndKeepsHeaderVisible(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	snapshot := appserver.ThreadReadSnapshot{
		Thread:              model.Thread{Status: "completed"},
		LatestTurnID:        "turn-1",
		LatestTurnStatus:    "completed",
		LatestTurnStartedAt: startedAt.Format(time.RFC3339Nano),
		LatestTurnUpdatedAt: startedAt.Add(10 * time.Second).Format(time.RFC3339Nano),
		DetailItems: []model.DetailItem{
			{ID: "block-1", Kind: model.DetailItemCommentary, Text: "finished reasoning", StartedAt: model.TimeString(startedAt.Format(time.RFC3339Nano))},
		},
	}

	message := renderSyncStatusAt(snapshot, startedAt.Add(time.Minute))
	if !strings.HasPrefix(message.Text, "⏱ [Status] completed · 10s\n") || len(message.Entities) != 1 {
		t.Fatalf("terminal message=%#v", message)
	}
	entity := message.Entities[0]
	wantOffset := syncUTF16Len("⏱ [Status] completed · 10s\n")
	if entity.Type != "expandable_blockquote" || entity.Offset != wantOffset || entity.Length != syncUTF16Len(message.Text)-wantOffset {
		t.Fatalf("terminal entity=%#v text=%q", entity, message.Text)
	}
}

func TestSyncStatusTrimsOldLinesAndPreservesLatestTail(t *testing.T) {
	t.Parallel()

	startedAt := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	snapshot := appserver.ThreadReadSnapshot{
		Thread:              model.Thread{Status: "inProgress"},
		LatestTurnID:        "turn-1",
		LatestTurnStatus:    "inProgress",
		LatestTurnStartedAt: startedAt.Format(time.RFC3339Nano),
		DetailItems: []model.DetailItem{
			{ID: "old", Kind: model.DetailItemCommentary, Text: strings.Repeat("old line\n", 700), StartedAt: model.TimeString(startedAt.Format(time.RFC3339Nano))},
			{ID: "latest", Kind: model.DetailItemCommentary, Text: "LATEST STATUS TAIL", StartedAt: model.TimeString(startedAt.Add(time.Second).Format(time.RFC3339Nano))},
		},
	}

	message := renderSyncStatusAt(snapshot, startedAt.Add(2*time.Second))
	if syncUTF16Len(message.Text) > 4096 || !strings.HasPrefix(message.Text, "⏱ [Status] inProgress · 2s\n") ||
		!strings.Contains(message.Text, "… удалено строк:") || !strings.HasSuffix(message.Text, "LATEST STATUS TAIL") {
		t.Fatalf("trimmed status length=%d text tail=%q", syncUTF16Len(message.Text), syncUTF16Suffix(message.Text, 200))
	}
}

func TestSyncPassiveSyncTicksElapsedFromStableTurnStart(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	service.mu.Lock()
	service.poll = &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayload("thread-1", "turn-1"),
	}}
	service.pollConnected = true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	if len(forum.sends) != 1 || !strings.HasPrefix(forum.sends[0].text, syncStatusHeader+" inProgress · 0s") {
		t.Fatalf("initial status=%#v, want observed start in compact header", forum.sends)
	}

	now = now.Add(5 * time.Second)
	service.reconcileSync(ctx)
	if len(forum.edits) != 1 || !strings.HasPrefix(forum.edits[0].text, syncStatusHeader+" inProgress · 5s") {
		t.Fatalf("elapsed edits=%#v, want elapsed-only edit from stable start", forum.edits)
	}
}

func TestSyncPassiveSyncFreezesCompletedDuration(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "turn-1", "working"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	now = now.Add(10 * time.Second)
	poll.threadReads["thread-1"] = syncCompletedPayload("thread-1", "turn-1", "done")
	service.reconcileSync(ctx)
	if len(forum.edits) != 1 || !strings.HasPrefix(forum.edits[0].text, syncStatusHeader+" completed · 10s") {
		t.Fatalf("terminal edits=%#v, want compact frozen duration", forum.edits)
	}
	if strings.Contains(forum.edits[0].text, "Run duration:") {
		t.Fatalf("terminal status retained verbose footer: %q", forum.edits[0].text)
	}

	now = now.Add(5 * time.Minute)
	service.reconcileSync(ctx)
	if len(forum.edits) != 1 {
		t.Fatalf("repeated terminal poll changed frozen status: %#v", forum.edits)
	}
}

func TestSyncPassiveSyncRetainsCollapsedAggregateBeforeFinal(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 17, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentaries("thread-1", "turn-1", "first block", "second block"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

	service.reconcileSync(ctx)
	statusID := forum.sends[0].messageID
	now = now.Add(10 * time.Second)
	poll.threadReads["thread-1"] = syncCompletedPayloadWithCommentaries("thread-1", "turn-1", "done", "first block", "second block")
	service.reconcileSync(ctx)

	if len(forum.edits) != 1 || forum.edits[0].messageID != statusID || len(forum.edits[0].message.Entities) != 1 {
		t.Fatalf("terminal status edits=%#v, want retained collapsed status %d", forum.edits, statusID)
	}
	if !strings.Contains(forum.edits[0].text, "first block") || !strings.Contains(forum.edits[0].text, "second block") {
		t.Fatalf("terminal status lost aggregate: %q", forum.edits[0].text)
	}
	if len(forum.sends) != 2 || forum.sends[1].text != syncFinalHeader+"\ndone" {
		t.Fatalf("sends=%#v, want separate final after retained status", forum.sends)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.StatusMessageID != statusID || topic.LastFinalFP == "" {
		t.Fatalf("topic=%#v err=%v, want retained status and delivered final", topic, err)
	}
}

func TestSyncTelegramOriginHotPollRefreshesAndStopsAtTerminal(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "started-turn", "hot progress"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()

	if keepGoing := service.syncTelegramOriginHotPollOnce(ctx, "thread-1", "started-turn"); !keepGoing {
		t.Fatal("hot poll stopped before terminal state")
	}
	if len(forum.sends) != 1 || !strings.Contains(forum.sends[0].text, "hot progress") {
		t.Fatalf("running hot-poll delivery=%#v", forum.sends)
	}

	poll.threadReads["thread-1"] = syncInterruptedPayload("thread-1", "started-turn")
	if keepGoing := service.syncTelegramOriginHotPollOnce(ctx, "thread-1", "started-turn"); !keepGoing {
		t.Fatal("hot poll stopped on transient interrupted evidence")
	}
	if snapshot := service.syncWriter.Snapshot(); snapshot.State != appserver.WriterRunning || snapshot.Active != 1 {
		t.Fatalf("transient interrupted hot poll released writer: %#v", snapshot)
	}

	poll.threadReads["thread-1"] = syncCompletedPayload("thread-1", "started-turn", "done")
	if keepGoing := service.syncTelegramOriginHotPollOnce(ctx, "thread-1", "started-turn"); keepGoing {
		t.Fatal("hot poll continued after terminal state")
	}
	if snapshot := service.syncWriter.Snapshot(); snapshot.State != appserver.WriterStopped || snapshot.Active != 0 {
		t.Fatalf("terminal hot poll did not release writer: %#v", snapshot)
	}
}

func TestSyncLiveToolOverlaySurvivesLaggingThreadReadWithoutEnteringAggregateStatus(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	fixedNow := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixedNow }
	reads := map[string]map[string]any{
		"thread-1": syncRunningPayload("thread-1", "started-turn"),
	}
	writer := &syncWriterSession{stubSession: &stubSession{threadReads: reads}, events: make(chan appserver.Event, 2)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}

	service.handleSyncWriterEvent(ctx, writer, appserver.Event{
		Channel: "notification",
		Method:  "item/started",
		Params: map[string]any{
			"threadId": "thread-1",
			"turnId":   "started-turn",
			"item": map[string]any{
				"id":      "cmd-slow",
				"type":    "commandExecution",
				"command": "sleep 20",
				"status":  "running",
			},
		},
	}, service.syncWriter.Snapshot().Generation)
	if len(forum.sends) != 1 || strings.Contains(forum.sends[0].text, "sleep 20") {
		t.Fatalf("live tool entered aggregate status: %#v", forum.sends)
	}
	stored, err := service.store.GetSnapshot(ctx, "thread-1")
	if err != nil || stored == nil {
		t.Fatalf("stored snapshot=%#v err=%v", stored, err)
	}
	var compact appserver.ThreadReadSnapshot
	if err := json.Unmarshal(stored.CompactJSON, &compact); err != nil || compact.LatestToolLabel != "sleep 20" {
		t.Fatalf("compact snapshot=%#v err=%v, want live tool preserved", compact, err)
	}

	service.mu.Lock()
	service.poll, service.pollConnected = &stubSession{threadReads: reads}, true
	service.mu.Unlock()
	service.reconcileSync(ctx)
	stored, err = service.store.GetSnapshot(ctx, "thread-1")
	if err != nil || stored == nil {
		t.Fatalf("stored snapshot after lagging poll=%#v err=%v", stored, err)
	}
	compact = appserver.ThreadReadSnapshot{}
	if err := json.Unmarshal(stored.CompactJSON, &compact); err != nil || compact.LatestToolLabel != "sleep 20" {
		t.Fatalf("lagging poll erased live tool from compact snapshot: %#v err=%v", compact, err)
	}
}

func TestSyncOffMarksOffBeforeCleanupAndDoesNotRestoreLegacy(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	ctx := context.Background()
	if err := service.store.BeginSyncActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	if err := service.store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "s", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.SyncTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := service.store.FinishSyncActivation(ctx, "s", `{}`, true); err != nil {
		t.Fatal(err)
	}
	forum := &fakeSyncForum{onDelete: func() {
		state, _ := service.store.GetSyncState(ctx)
		if state.State != model.SyncStateOff {
			t.Fatalf("delete observed state %q", state.State)
		}
	}}
	service.SetSyncForum(forum)
	response, err := service.HandleMessage(ctx, -1001, 1, 123456789, "/sync off", 0)
	if err != nil || response == nil {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(forum.deletes) != 1 {
		t.Fatalf("deletes=%v", forum.deletes)
	}
}

func TestSyncOffCleansReadyDraftTopic(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	if err := service.store.CreateSyncTopicDraft(ctx, model.SyncTopicDraft{SessionID: "s", ChatID: -1001, TopicID: 21,
		Rank: 3, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 908, 123456789, "/sync off", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "deleted 3 topic") {
		t.Fatalf("response=%#v err=%v deletes=%#v", response, err, forum.deletes)
	}
	if len(forum.deletes) != 3 {
		t.Fatalf("deletes=%#v", forum.deletes)
	}
	drafts, err := service.store.ListSyncTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 0 {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
}

func TestSyncSafeOffRefusesStartingDraft(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	if err := service.store.CreateSyncTopicDraft(ctx, model.SyncTopicDraft{SessionID: "s", ChatID: -1001, TopicID: 21,
		Rank: 3, Title: "New task", CWD: "/tmp/project"}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := service.store.ClaimSyncTopicDraftMessage(ctx, -1001, 21, 909); err != nil {
		t.Fatal(err)
	}
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 910, 123456789, "/sync off", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "off refused") || !strings.Contains(response.Text, "New task") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(forum.deletes) != 0 {
		t.Fatalf("deletes=%#v", forum.deletes)
	}
}

func TestSyncDraftRejectsSecondMessageFromCurrentDurableState(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	draft := model.SyncTopicDraft{SessionID: "s", ChatID: -1001, TopicID: 21, Rank: 3, Title: "New task", CWD: "/tmp/project"}
	if err := service.store.CreateSyncTopicDraft(ctx, draft); err != nil {
		t.Fatal(err)
	}
	stale, err := service.store.GetActiveSyncTopicDraft(ctx, -1001, 21)
	if err != nil || stale == nil || stale.State != model.SyncDraftReady {
		t.Fatalf("stale=%#v err=%v", stale, err)
	}
	if _, _, _, err := service.store.ClaimSyncTopicDraftMessage(ctx, -1001, 21, 911); err != nil {
		t.Fatal(err)
	}
	response, err := service.handleSyncDraftMessage(ctx, *stale, 912, "second message")
	if err != nil || response == nil || !strings.Contains(response.Text, "already starting") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestSyncConcurrentTopicsShareWriterAndDuplicateDoesNotReplay(t *testing.T) {
	service := activeSyncService(t)
	writer := &syncWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 8)}
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
	if err != nil || sameTopic == nil || !strings.Contains(sameTopic.Text, "steered") {
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
	if len(writer.turnSteerCalls) != 1 || writer.turnSteerCalls[0].threadID != "thread-1" || writer.turnSteerCalls[0].message != "second same topic" {
		t.Fatalf("steers=%#v", writer.turnSteerCalls)
	}
	if writer.turnStartCalls[0].message != "first prompt" || writer.turnStartCalls[1].message != "parallel prompt" {
		t.Fatalf("prompts=%#v", writer.turnStartCalls)
	}
	snapshot := service.syncWriter.Snapshot()
	if snapshot.Active != 2 || snapshot.Generation == 0 {
		t.Fatalf("writer snapshot=%#v", snapshot)
	}
}

func TestSyncActiveTopicMessageSteersCurrentTelegramTurn(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "first", 0); err != nil {
		t.Fatal(err)
	}

	response, err := service.HandleMessageWithID(ctx, -1001, 11, 502, 123456789, "steer this", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "steered") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnSteerCalls) != 1 || writer.turnSteerCalls[0].turnID != "started-turn" || writer.turnSteerCalls[0].message != "steer this" {
		t.Fatalf("steers=%#v", writer.turnSteerCalls)
	}
	if len(writer.turnStartCalls) != 1 {
		t.Fatalf("turn starts=%#v, want only initial turn", writer.turnStartCalls)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.PendingTelegramTurnID != "started-turn" || topic.PendingTelegramUserFP != syncUserTextFingerprint("started-turn", "steer this") {
		t.Fatalf("pending Telegram user state=%#v err=%v", topic, err)
	}
}

func TestSyncManagedSteerFallsBackToNewTurnAfterAuthoritativeIdle(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "first", 0); err != nil {
		t.Fatal(err)
	}
	writer.turnSteerErrs = []error{errors.New("map[code:-32600 message:no active turn to steer]")}
	writer.threadReads = map[string]map[string]any{
		"thread-1": syncCompletedPayload("thread-1", "started-turn", "done"),
	}

	response, err := service.HandleMessageWithID(ctx, -1001, 11, 502, 123456789, "next", 0)
	if err != nil || response == nil || response.TurnID != "started-turn" || !strings.Contains(response.Text, "started") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnSteerCalls) != 1 || len(writer.turnStartCalls) != 2 || writer.turnStartCalls[1].message != "next" {
		t.Fatalf("steers=%#v starts=%#v", writer.turnSteerCalls, writer.turnStartCalls)
	}
	receipt, err := service.store.GetSyncReceipt(ctx, 11, 502)
	if err != nil || receipt == nil || receipt.State != model.SyncReceiptDispatched {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestSyncAuthoritativeSupersedingTurnReleasesStaleLocalLease(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "first", 0); err != nil {
		t.Fatal(err)
	}
	service.cfg.AppServerMode = string(appserver.TransportDaemon)
	if service.syncWriter.Snapshot().Active != 1 {
		t.Fatalf("writer before supersession=%#v", service.syncWriter.Snapshot())
	}
	state, err := service.store.GetSyncState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	forum := &fakeSyncForum{}
	service.processSyncSnapshotLocked(ctx, state, forum, *topic, appserver.SnapshotFromThreadRead(
		syncRunningPayloadWithCommentary("thread-1", "desktop-turn", "desktop work")), "sync_poll")

	if snapshot := service.syncWriter.Snapshot(); snapshot.Active != 0 || snapshot.Unknown != 0 {
		t.Fatalf("writer after supersession=%#v", snapshot)
	}
	updated, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || updated == nil || updated.ActiveTurnState != model.SyncTurnTerminal {
		t.Fatalf("topic after supersession=%#v err=%v", updated, err)
	}
}

func TestSyncSharedDaemonRestartUnknownReconcilesBeforeSteer(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = "daemon"
	ctx := context.Background()
	oldReceipt, _, err := service.store.AcceptSyncMessage(ctx, -1001, 11, 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncStarting(ctx, oldReceipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncDispatchState(ctx, oldReceipt, model.SyncReceiptDispatched, "shared-turn", model.SyncTurnActive, 7); err != nil {
		t.Fatal(err)
	}
	if err := service.store.RecoverSyncWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "shared-turn", "still running"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }

	response, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "continue after restart", 0)
	if err != nil || response == nil || response.TurnID != "shared-turn" || !strings.Contains(response.Text, "steered") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnSteerCalls) != 1 || writer.turnSteerCalls[0].turnID != "shared-turn" {
		t.Fatalf("steers=%#v", writer.turnSteerCalls)
	}
	if len(writer.turnStartCalls) != 0 {
		t.Fatalf("parallel starts=%#v", writer.turnStartCalls)
	}
	loadedOld, err := service.store.GetSyncReceipt(ctx, 11, 500)
	if err != nil || loadedOld == nil || loadedOld.State != model.SyncReceiptDispatched {
		t.Fatalf("old receipt=%#v err=%v, want no replay mutation", loadedOld, err)
	}
}

func TestSyncDesktopOriginActiveTurnIsSteeredWithoutParallelStart(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = "daemon"
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayloadWithCommentary("thread-1", "desktop-turn", "desktop progress"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }

	response, err := service.HandleMessageWithID(ctx, -1001, 11, 503, 123456789, "continue from Telegram", 0)
	if err != nil || response == nil || response.TurnID != "desktop-turn" || !strings.Contains(response.Text, "steered") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnSteerCalls) != 1 || writer.turnSteerCalls[0].turnID != "desktop-turn" {
		t.Fatalf("steers=%#v", writer.turnSteerCalls)
	}
	if len(writer.turnStartCalls) != 0 {
		t.Fatalf("parallel turn starts=%#v", writer.turnStartCalls)
	}
}

func TestSyncStaleDesktopActiveTurnFallsBackAfterAuthoritativeRead(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = "daemon"
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayload("thread-1", "stale-desktop-turn"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	writer := &stubSession{
		turnSteerErr: errors.New("map[code:-32600 message:no active turn to steer]"),
		threadReads:  map[string]map[string]any{"thread-1": syncCompletedPayload("thread-1", "stale-desktop-turn", "done")},
	}
	service.liveFactory = func() Session { return writer }

	response, err := service.HandleMessageWithID(ctx, -1001, 11, 504, 123456789, "start after stale", 0)
	if err != nil || response == nil || response.TurnID != "started-turn" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnSteerCalls) != 1 || len(writer.turnStartCalls) != 1 {
		t.Fatalf("steers=%#v starts=%#v", writer.turnSteerCalls, writer.turnStartCalls)
	}
}

func TestSyncAmbiguousTurnStartIsUnknownAndNeverReplayed(t *testing.T) {
	service := activeSyncService(t)
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
	if service.syncWriter.Snapshot().Unknown != 1 {
		t.Fatalf("writer=%#v", service.syncWriter.Snapshot())
	}
}

func TestSyncTerminalEventsRoutePerTopicAndCloseAfterLastTurn(t *testing.T) {
	service := activeSyncService(t)
	writer := &syncWriterSession{stubSession: &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncCompletedPayload("thread-1", "started-turn", "done one"),
		"thread-2": syncCompletedPayload("thread-2", "started-turn", "done two"),
	}}, events: make(chan appserver.Event, 8)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleMessageWithID(ctx, -1001, 12, 601, 123456789, "two", 0); err != nil {
		t.Fatal(err)
	}
	generation := service.syncWriter.Snapshot().Generation
	eventOne := appserver.Event{Method: "turn/completed", Params: map[string]any{"threadId": "thread-1", "turnId": "started-turn"}}
	service.handleSyncWriterEvent(ctx, writer, eventOne, generation+1)
	if len(forum.sends) != 0 {
		t.Fatalf("stale generation routed sends=%#v", forum.sends)
	}
	service.handleSyncWriterEvent(ctx, writer, eventOne, generation)
	if service.syncWriter.Snapshot().Active != 1 || writer.closeCalls != 0 {
		t.Fatalf("after first: writer=%#v closes=%d", service.syncWriter.Snapshot(), writer.closeCalls)
	}
	for _, send := range forum.sends {
		if send.topicID != 11 {
			t.Fatalf("first event crossed topic: %#v", forum.sends)
		}
	}
	eventTwo := appserver.Event{Method: "turn/completed", Params: map[string]any{"threadId": "thread-2", "turnId": "started-turn"}}
	service.handleSyncWriterEvent(ctx, writer, eventTwo, generation)
	if service.syncWriter.Snapshot().State != appserver.WriterStopped || writer.closeCalls != 1 {
		t.Fatalf("after last: writer=%#v closes=%d", service.syncWriter.Snapshot(), writer.closeCalls)
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

func TestSyncTransientInterruptedEventKeepsWriterAndRecoversProgress(t *testing.T) {
	service := activeSyncService(t)
	reads := map[string]map[string]any{
		"thread-1": syncInterruptedPayload("thread-1", "started-turn"),
	}
	writer := &syncWriterSession{stubSession: &stubSession{threadReads: reads}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if !service.isTelegramOriginTurn(ctx, "thread-1", "started-turn") {
		t.Fatal("Sync turn was not marked as Telegram-origin")
	}

	generation := service.syncWriter.Snapshot().Generation
	event := appserver.Event{Method: "turn/completed", Params: map[string]any{"threadId": "thread-1", "turnId": "started-turn"}}
	service.handleSyncWriterEvent(ctx, writer, event, generation)
	if snapshot := service.syncWriter.Snapshot(); snapshot.Active != 1 || snapshot.State != appserver.WriterRunning {
		t.Fatalf("transient interrupted released writer: %#v", snapshot)
	}
	if writer.closeCalls != 0 || len(forum.sends) != 0 {
		t.Fatalf("transient interrupted became visible/terminal: closes=%d sends=%#v", writer.closeCalls, forum.sends)
	}

	reads["thread-1"] = syncRunningPayload("thread-1", "started-turn")
	service.handleSyncWriterEvent(ctx, writer, appserver.Event{Method: "item/updated", Params: map[string]any{"threadId": "thread-1", "turnId": "started-turn"}}, generation)
	if len(forum.sends) != 1 || !strings.Contains(forum.sends[0].text, "inProgress") {
		t.Fatalf("recovered progress not delivered: %#v", forum.sends)
	}
	reads["thread-1"] = syncCompletedPayload("thread-1", "started-turn", "done")
	service.handleSyncWriterEvent(ctx, writer, event, generation)
	if snapshot := service.syncWriter.Snapshot(); snapshot.State != appserver.WriterStopped || snapshot.Active != 0 {
		t.Fatalf("confirmed terminal did not release writer: %#v", snapshot)
	}
	if writer.closeCalls != 1 {
		t.Fatalf("writer close calls=%d, want 1", writer.closeCalls)
	}
}

func TestSyncPollDefersTransientInterrupted(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	service.poll = &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncInterruptedPayload("thread-1", "started-turn"),
	}}
	service.pollConnected = true
	service.mu.Unlock()

	service.reconcileSync(ctx)
	if snapshot := service.syncWriter.Snapshot(); snapshot.Active != 1 || snapshot.State != appserver.WriterRunning {
		t.Fatalf("poll released transient interrupted writer: %#v", snapshot)
	}
	if writer.closeCalls != 0 {
		t.Fatalf("writer close calls=%d, want 0", writer.closeCalls)
	}
}

func TestSyncExplicitStopInterruptedBypassesTerminalGrace(t *testing.T) {
	service := activeSyncService(t)
	writer := &syncWriterSession{stubSession: &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncInterruptedPayload("thread-1", "started-turn"),
	}}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 502, 123456789, "/stop", 0); err != nil {
		t.Fatal(err)
	}
	service.handleSyncWriterEvent(ctx, writer, appserver.Event{Method: "turn/completed", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn",
	}}, service.syncWriter.Snapshot().Generation)
	if snapshot := service.syncWriter.Snapshot(); snapshot.State != appserver.WriterStopped || snapshot.Active != 0 {
		t.Fatalf("explicit stop did not bypass grace: %#v", snapshot)
	}
	if writer.closeCalls != 1 {
		t.Fatalf("writer close calls=%d, want 1", writer.closeCalls)
	}
}

func TestSyncPollTerminalEvidenceClosesWriterWhenEventWasMissed(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncCompletedPayload("thread-1", "started-turn", "done")}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	service.reconcileSync(ctx)
	if snapshot := service.syncWriter.Snapshot(); snapshot.State != appserver.WriterStopped || snapshot.Active != 0 {
		t.Fatalf("writer=%#v", snapshot)
	}
	if writer.closeCalls != 1 {
		t.Fatalf("writer close calls=%d", writer.closeCalls)
	}
}

func TestSyncApprovalCallbackIsGuardedByTopicTurnAndGeneration(t *testing.T) {
	service := activeSyncService(t)
	writer := &syncWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "needs approval", 0); err != nil {
		t.Fatal(err)
	}
	generation := service.syncWriter.Snapshot().Generation
	event := appserver.Event{Channel: "server_request", Method: "item/commandExecution/requestApproval", ID: "request-1", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn", "itemId": "item-1", "question": "Run it?",
	}}
	service.handleSyncWriterEvent(ctx, writer, event, generation)
	if len(forum.actions) != 1 || forum.actions[0].topicID != 11 || len(forum.actions[0].buttons) != 2 {
		t.Fatalf("actions=%#v", forum.actions)
	}
	if got := forum.actions[0].buttons[0][1].Text; got != "Allow command prefix" {
		t.Fatalf("persistent approval label=%q, want Allow command prefix", got)
	}
	if !strings.HasPrefix(forum.actions[0].text, syncApprovalHeader+"\n") {
		t.Fatalf("approval text=%q, want icon header", forum.actions[0].text)
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
	if len(forum.edits) != 1 || forum.edits[0].topicID != 11 || forum.edits[0].messageID != forum.actions[0].messageID || !strings.Contains(forum.edits[0].text, "Approved") {
		t.Fatalf("approval card edits=%#v, want same card marked Approved", forum.edits)
	}
	response, err = service.HandleCallback(ctx, -1001, 11, forum.actions[0].messageID, 123456789, token)
	if err != nil || response == nil || !strings.Contains(response.CallbackText, "stale") {
		t.Fatalf("duplicate callback=%#v err=%v", response, err)
	}
}

func TestSyncApprovalDecisionsResolveSameCard(t *testing.T) {
	tests := []struct {
		name       string
		row        int
		column     int
		decision   string
		resolution string
	}{
		{name: "approve_once", row: 0, column: 0, decision: "accept", resolution: "Approved"},
		{name: "allow_command_prefix", row: 0, column: 1, decision: "acceptForSession", resolution: "Approved"},
		{name: "deny", row: 1, column: 0, decision: "decline", resolution: "Denied"},
		{name: "cancel", row: 1, column: 1, decision: "cancel", resolution: "Cancelled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := activeSyncService(t)
			writer := &syncWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 4)}
			service.liveFactory = func() Session { return writer }
			forum := &fakeSyncForum{}
			service.SetSyncForum(forum)
			ctx := context.Background()
			if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "needs approval", 0); err != nil {
				t.Fatal(err)
			}
			event := appserver.Event{Channel: "server_request", Method: "item/commandExecution/requestApproval", ID: "request-1", Params: map[string]any{
				"threadId": "thread-1", "turnId": "started-turn", "itemId": "item-1", "question": "Run it?",
			}}
			service.handleSyncWriterEvent(ctx, writer, event, service.syncWriter.Snapshot().Generation)
			if len(forum.actions) != 1 {
				t.Fatalf("actions=%#v, want one", forum.actions)
			}
			action := forum.actions[0]
			token := action.buttons[test.row][test.column].CallbackData
			response, err := service.HandleCallback(ctx, -1001, action.topicID, action.messageID, 123456789, token)
			if err != nil || response == nil || !strings.Contains(response.CallbackText, "sent") {
				t.Fatalf("response=%#v err=%v", response, err)
			}
			if len(writer.respondRequestCalls) != 1 || writer.respondRequestCalls[0].result["decision"] != test.decision {
				t.Fatalf("responses=%#v, want decision %q", writer.respondRequestCalls, test.decision)
			}
			if len(forum.edits) != 1 || forum.edits[0].topicID != action.topicID || forum.edits[0].messageID != action.messageID ||
				!strings.Contains(forum.edits[0].text, test.resolution) {
				t.Fatalf("edits=%#v, want same card resolved as %q", forum.edits, test.resolution)
			}
		})
	}
}

func TestSyncDesktopOriginApprovalIsNotActionable(t *testing.T) {
	service := activeSyncService(t)
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	process := &syncWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 1)}
	event := appserver.Event{Channel: "server_request", Method: "item/requestApproval", ID: "desktop-request", Params: map[string]any{"threadId": "thread-1", "turnId": "desktop-turn"}}
	service.handleSyncWriterEvent(context.Background(), process, event, 99)
	if len(forum.actions) != 0 {
		t.Fatalf("desktop approval became actionable: %#v", forum.actions)
	}
}

func TestSyncStructuredUserInputCallbackReturnsGuardedAnswers(t *testing.T) {
	service := activeSyncService(t)
	writer := &syncWriterSession{stubSession: &stubSession{}, events: make(chan appserver.Event, 4)}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "ask", 0); err != nil {
		t.Fatal(err)
	}
	event := appserver.Event{Channel: "server_request", Method: "item/tool/requestUserInput", ID: "input-1", Params: map[string]any{
		"threadId": "thread-1", "turnId": "started-turn", "questions": []any{map[string]any{"id": "target", "question": "Where?", "options": []any{map[string]any{"label": "staging"}, map[string]any{"label": "production"}}}},
	}}
	service.handleSyncWriterEvent(ctx, writer, event, service.syncWriter.Snapshot().Generation)
	if len(forum.actions) != 1 || len(forum.actions[0].buttons) != 2 {
		t.Fatalf("actions=%#v", forum.actions)
	}
	if !strings.HasPrefix(forum.actions[0].text, syncInputHeader+"\n") {
		t.Fatalf("input text=%q, want icon header", forum.actions[0].text)
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

func TestSyncStopInterruptsOnlyCurrentTopicTurn(t *testing.T) {
	service := activeSyncService(t)
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

func TestSyncStopInterruptsDesktopOriginTurnFromAuthoritativeDaemonSnapshot(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = "daemon"
	ctx := context.Background()
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncRunningPayload("thread-1", "desktop-turn"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()

	response, err := service.HandleMessageWithID(ctx, -1001, 11, 701, 123456789, "/stop", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "Stop requested") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(poll.turnInterruptCalls) != 1 || poll.turnInterruptCalls[0].threadID != "thread-1" || poll.turnInterruptCalls[0].turnID != "desktop-turn" {
		t.Fatalf("interrupts=%#v", poll.turnInterruptCalls)
	}
}

func TestSyncSafeOffRefusesActiveTurnsWithoutCleanup(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/sync off", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "refused") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	state, _ := service.store.GetSyncState(ctx)
	if state.State != model.SyncStateActive || len(forum.deletes) != 0 {
		t.Fatalf("state=%#v deletes=%v", state, forum.deletes)
	}
}

func TestSyncForceOffInterruptsAllAndWaitsForTerminalBeforeCleanup(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.RequestTimeout = 200 * time.Millisecond
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.HandleMessageWithID(ctx, -1001, 12, 601, 123456789, "two", 0); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{
		"thread-1": syncCompletedPayload("thread-1", "started-turn", "one done"), "thread-2": syncCompletedPayload("thread-2", "started-turn", "two done"),
	}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/sync off --force", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "Sync off") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.turnInterruptCalls) != 2 {
		t.Fatalf("interrupts=%#v", writer.turnInterruptCalls)
	}
	state, _ := service.store.GetSyncState(ctx)
	if state.State != model.SyncStateOff || len(forum.deletes) != 2 {
		t.Fatalf("state=%#v deletes=%v", state, forum.deletes)
	}
}

func TestSyncForceOffTimeoutStaysDrainingAndDoesNotCleanup(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.RequestTimeout = 30 * time.Millisecond
	writer := &stubSession{}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	if _, err := service.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "one", 0); err != nil {
		t.Fatal(err)
	}
	poll := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncRunningPayload("thread-1", "started-turn")}}
	service.mu.Lock()
	service.poll, service.pollConnected = poll, true
	service.mu.Unlock()
	response, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/sync off --force", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "remains draining") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	state, _ := service.store.GetSyncState(ctx)
	if state.State != model.SyncStateDraining || len(forum.deletes) != 0 || service.syncWriter.Snapshot().Accepting {
		t.Fatalf("state=%#v deletes=%v writer=%#v", state, forum.deletes, service.syncWriter.Snapshot())
	}
}

func TestSyncRestartUnknownOwnershipBlocksSafeAndForceCleanup(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.RequestTimeout = 25 * time.Millisecond
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx := context.Background()
	receipt, _, err := service.store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.store.MarkSyncStarting(ctx, receipt, 7); err != nil {
		t.Fatal(err)
	}
	if err := service.store.RecoverSyncWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	safe, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/sync off", 0)
	if err != nil || safe == nil || !strings.Contains(safe.Text, "refused") {
		t.Fatalf("safe=%#v err=%v", safe, err)
	}
	forced, err := service.HandleMessageWithID(ctx, -1001, 1, 701, 123456789, "/sync off --force", 0)
	if err != nil || forced == nil || !strings.Contains(forced.Text, "remains draining") {
		t.Fatalf("forced=%#v err=%v", forced, err)
	}
	if len(forum.deletes) != 0 {
		t.Fatalf("unknown ownership was cleaned up: %v", forum.deletes)
	}
}

func TestSyncStartupResetsSessionCleansTopicsAndWarnsOnceWhenWebSocketUnavailable(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = string(appserver.TransportWebSocket)
	service.cfg.AppServerListen = "ws://127.0.0.1:4500"
	service.cfg.RequestTimeout = 25 * time.Millisecond
	service.cfg.IndexRefreshInterval = time.Hour
	service.cfg.SyncPollInterval = time.Hour
	failedPoll := &stubSession{startErr: errors.New("shared daemon unavailable")}
	service.poll = failedPoll
	service.pollFactory = func() Session { return failedPoll }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.store.SetState(ctx, "appserver.poll_connected", "true"); err != nil {
		t.Fatal(err)
	}
	if err := service.store.SetState(ctx, "appserver.live_connected", "true"); err != nil {
		t.Fatal(err)
	}

	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	service.FinishStartup(ctx)
	service.FinishStartup(ctx)
	select {
	case <-service.startupDone:
	case <-time.After(time.Second):
		t.Fatal("startup finalization did not finish")
	}

	state, err := service.store.GetSyncState(ctx)
	if err != nil || state.State != model.SyncStateOff {
		t.Fatalf("state=%#v err=%v", state, err)
	}
	if len(forum.deletes) != 2 || forum.deletes[0] != 11 || forum.deletes[1] != 12 {
		t.Fatalf("deletes=%v", forum.deletes)
	}
	if len(forum.sends) != 1 || forum.sends[0].topicID != syncGeneralSendTopicID ||
		!strings.Contains(forum.sends[0].text, "Shared Codex App Server is unavailable") ||
		!strings.Contains(forum.sends[0].text, "ws://127.0.0.1:4500") ||
		!strings.Contains(forum.sends[0].text, "/sync on") {
		t.Fatalf("startup warnings=%#v", forum.sends)
	}
	if value, _ := service.store.GetState(ctx, "appserver.poll_connected"); value != "false" {
		t.Fatalf("poll_connected=%q", value)
	}
	if value, _ := service.store.GetState(ctx, "appserver.live_connected"); value != "" {
		t.Fatalf("retired live_connected=%q", value)
	}
}

func TestSyncStartupDoesNotWarnWhenSharedDaemonConnects(t *testing.T) {
	service := activeSyncService(t)
	service.cfg.AppServerMode = string(appserver.TransportDaemon)
	service.cfg.IndexRefreshInterval = time.Hour
	service.cfg.SyncPollInterval = time.Hour
	service.poll = &stubSession{}
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := service.Start(ctx); err != nil {
		t.Fatal(err)
	}
	service.FinishStartup(ctx)
	select {
	case <-service.startupDone:
	case <-time.After(time.Second):
		t.Fatal("startup finalization did not finish")
	}

	if len(forum.sends) != 0 {
		t.Fatalf("unexpected startup warning=%#v", forum.sends)
	}
}

func TestSyncProjectPickerCreatesThreadThenTopicThenDurableBinding(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "new-thread", "title": "New task", "cwd": "/tmp/project", "updatedAt": float64(100)}}}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	forum := &fakeSyncForum{nextTopicID: 20}
	service.SetSyncForum(forum)
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
	drafts, err := service.store.ListSyncTopicDrafts(ctx, "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 || drafts[0].TopicID != 21 || drafts[0].State != model.SyncDraftReady {
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
	if len(writer.threadStartOptions) != 1 || writer.threadStartOptions[0].ApprovalPolicy != "on-request" || writer.threadStartOptions[0].ApprovalsReviewer != "auto_review" || writer.threadStartOptions[0].SandboxMode != "workspace-write" {
		t.Fatalf("thread permissions=%#v, want explicit Telegram permissions", writer.threadStartOptions)
	}
	if got := writer.turnStartCalls[0]; got.approvalPolicy != "on-request" || got.approvalsReviewer != "auto_review" || got.sandboxMode != "workspace-write" {
		t.Fatalf("turn permissions=%#v, want explicit Telegram permissions", got)
	}
	topics, err := service.store.ListSyncTopics(ctx, "s")
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

func TestSyncExistingEmptyTopicRecoversNoRolloutOnNextPrompt(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{
		threadResumeErr:   errors.New("map[code:-32600 message:no rollout found for thread id thread-1]"),
		threadStartResult: map[string]any{"thread": map[string]any{"id": "replacement-thread", "cwd": "/tmp/project"}},
	}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)

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
	topic, err := service.store.GetActiveSyncTopic(context.Background(), -1001, 11)
	if err != nil || topic == nil || topic.ThreadID != "replacement-thread" {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	if len(forum.renames) != 1 || forum.renames[0].title != "replacement prompt" {
		t.Fatalf("renames=%#v", forum.renames)
	}
}

func TestSyncExistingEmptyTopicDoesNotRecoverUnrelatedResumeError(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{
		threadResumeErr:   errors.New("permission denied"),
		threadStartResult: map[string]any{"thread": map[string]any{"id": "must-not-start"}},
	}
	service.liveFactory = func() Session { return writer }
	service.SetSyncForum(&fakeSyncForum{})

	response, err := service.HandleMessageWithID(context.Background(), -1001, 11, 904, 123456789, "do not recover", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "could not resume") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if len(writer.threadStartCalls) != 0 {
		t.Fatalf("unrelated resume error created replacement: %#v", writer.threadStartCalls)
	}
}

func TestSyncDraftDefinitiveThreadStartFailureCanRetryWithNewMessage(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{threadStartErr: errors.New("invalid cwd")}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{nextTopicID: 20}
	service.SetSyncForum(forum)
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
	drafts, err := service.store.ListSyncTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 1 || drafts[0].State != model.SyncDraftReady || drafts[0].SourceMessageID != 0 {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	receipt, err := service.store.GetSyncReceipt(ctx, 21, 905)
	if err != nil || receipt == nil || receipt.State != model.SyncReceiptRejected {
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

func TestSyncDraftDefinitiveFirstTurnFailureReturnsTopicToDraft(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{
		threadStartResult: map[string]any{"thread": map[string]any{"id": "empty-thread", "cwd": "/tmp/project"}},
		turnStartErr:      errors.New("invalid input"),
	}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{nextTopicID: 20}
	service.SetSyncForum(forum)
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
	drafts, err := service.store.ListSyncTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 1 || drafts[0].TopicID != 21 || drafts[0].State != model.SyncDraftReady {
		t.Fatalf("drafts=%#v err=%v", drafts, err)
	}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 21)
	if err != nil || topic != nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	receipt, err := service.store.GetSyncReceipt(ctx, 21, 907)
	if err != nil || receipt == nil || receipt.State != model.SyncReceiptRejected {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
}

func TestSyncControlHelpListsNewTaskCommands(t *testing.T) {
	service := activeSyncService(t)
	response, err := service.HandleMessageWithID(context.Background(), -1001, 1, 903, 123456789, "/threads", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "/projects") || !strings.Contains(response.Text, "/newchat") || !strings.Contains(response.Text, "/repair") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	status, err := service.HandleMessageWithID(context.Background(), -1001, 1, 904, 123456789, "/status", 0)
	if err != nil || status == nil || !strings.Contains(status.Text, "New task commands: /projects, /newchat") ||
		!strings.Contains(status.Text, "Poller status: /pollers") ||
		!strings.Contains(status.Text, "External requests: /requests") ||
		!strings.Contains(status.Text, "Repair command: /repair") ||
		!strings.Contains(status.Text, "Dead deliveries:") ||
		!strings.Contains(status.Text, "App-server heartbeat:") ||
		!strings.Contains(status.Text, "Open health incidents:") {
		t.Fatalf("status=%#v err=%v", status, err)
	}
}

func TestSyncControlRepairRequestsSoftSessionRepair(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()

	response, err := service.HandleMessageWithID(ctx, -1001, 1, 905, 123456789, "/repair@assistant_bot", 0)
	if err != nil || response == nil || !strings.Contains(response.Text, "sessions will be recreated") {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	request, err := service.store.GetState(ctx, "control.repair_request")
	if err != nil || !strings.HasSuffix(request, "|telegram") {
		t.Fatalf("repair request=%q err=%v", request, err)
	}
	state, err := service.store.GetSyncState(ctx)
	if err != nil || state.State != model.SyncStateActive {
		t.Fatalf("Sync state=%#v err=%v, want unchanged active state", state, err)
	}
}

func TestSyncPromptTopicTitleKeepsUnicodeAndBoundsLength(t *testing.T) {
	if got := syncPromptTopicTitle("  Собери   информацию про Codex server  "); got != "Собери информацию про Codex server" {
		t.Fatalf("title=%q", got)
	}
	got := syncPromptTopicTitle(strings.Repeat("длинное название ", 20))
	if !strings.HasSuffix(got, "…") || len([]rune(got)) > syncPromptTitleMaxRunes+1 {
		t.Fatalf("bounded title=%q runes=%d", got, len([]rune(got)))
	}
}

func TestSyncNewTaskDoesNotCreateThreadWhenTopicCreationFails(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "orphan-thread", "title": "Safe partial", "cwd": "/tmp/project"}}}
	service.liveFactory = func() Session { return writer }
	forum := &fakeSyncForum{createErrAt: 1}
	service.SetSyncForum(forum)
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
	topics, _ := service.store.ListSyncTopics(ctx, "s")
	for _, topic := range topics {
		if topic.ThreadID == "orphan-thread" {
			t.Fatalf("failed topic got binding: %#v", topic)
		}
	}
	if service.syncWriter.Snapshot().State != appserver.WriterStopped {
		t.Fatalf("writer=%#v", service.syncWriter.Snapshot())
	}
	duplicate, err := service.HandleCallback(ctx, -1001, 1, 900, 123456789, menu.Buttons[0][0].CallbackData)
	if err != nil || duplicate == nil || !strings.Contains(duplicate.CallbackText, "stale") || len(writer.threadStartCalls) != 0 {
		t.Fatalf("duplicate=%#v starts=%#v err=%v", duplicate, writer.threadStartCalls, err)
	}
}

func TestSyncProjectsFailClosedWhileOff(t *testing.T) {
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	forum := &fakeSyncForum{}
	service.SetSyncForum(forum)
	response, err := service.HandleMessageWithID(context.Background(), -1001, 1, 700, 123456789, "/projects", 0)
	if err != nil || response == nil || len(response.Buttons) != 0 || len(forum.creates) != 0 {
		t.Fatalf("response=%#v creates=%v err=%v", response, forum.creates, err)
	}
	if service.syncWriter.Snapshot().State != appserver.WriterStopped {
		t.Fatalf("writer=%#v", service.syncWriter.Snapshot())
	}
}

func TestSyncProjectCallbackFromOldSessionFailsBeforeThreadStart(t *testing.T) {
	service := activeSyncService(t)
	writer := &stubSession{threadStartResult: map[string]any{"thread": map[string]any{"id": "must-not-start"}}}
	service.liveFactory = func() Session { return writer }
	ctx := context.Background()
	menu, err := service.HandleMessageWithID(ctx, -1001, 1, 700, 123456789, "/projects", 0)
	if err != nil || menu == nil || len(menu.Buttons) == 0 {
		t.Fatalf("menu=%#v err=%v", menu, err)
	}
	if _, err := service.store.MarkSyncOff(ctx, "s"); err != nil {
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

func syncRunningPayload(threadID, turnID string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "inProgress", "turns": []any{map[string]any{"id": turnID, "status": "inProgress", "items": []any{}}}}}
}

func syncRunningPayloadWithCommentary(threadID, turnID, commentary string) map[string]any {
	return syncRunningPayloadWithCommentaries(threadID, turnID, commentary)
}

func syncRunningPayloadWithCommentaries(threadID, turnID string, commentaries ...string) map[string]any {
	items := make([]any, 0, len(commentaries))
	for index, commentary := range commentaries {
		items = append(items, map[string]any{
			"id": fmt.Sprintf("%s-commentary-%d", turnID, index+1), "type": "agentMessage", "phase": "commentary", "text": commentary,
		})
	}
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "inProgress", "turns": []any{map[string]any{
		"id": turnID, "status": "inProgress", "items": items,
	}}}}
}

type syncTestUser struct {
	id   string
	text string
}

func syncRunningPayloadWithUser(threadID, turnID, userID, userText, commentary string) map[string]any {
	return syncRunningPayloadWithUsers(threadID, turnID, []syncTestUser{{id: userID, text: userText}}, commentary)
}

func syncRunningPayloadWithUsers(threadID, turnID string, users []syncTestUser, commentary string) map[string]any {
	items := make([]any, 0, len(users)+1)
	for _, user := range users {
		items = append(items, map[string]any{"id": user.id, "type": "userMessage", "content": []any{map[string]any{"type": "text", "text": user.text}}})
	}
	if commentary != "" {
		items = append(items, map[string]any{"id": turnID + "-commentary", "type": "agentMessage", "phase": "commentary", "text": commentary})
	}
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "inProgress", "turns": []any{map[string]any{
		"id": turnID, "status": "inProgress", "items": items,
	}}}}
}

func syncInterruptedPayload(threadID, turnID string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "interrupted", "turns": []any{map[string]any{"id": turnID, "status": "interrupted", "items": []any{}}}}}
}

func syncCompletedPayload(threadID, turnID, finalText string) map[string]any {
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "completed", "turns": []any{map[string]any{
		"id": turnID, "status": "completed", "items": []any{map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": finalText}},
	}}}}
}

func syncCompletedPayloadWithCommentaries(threadID, turnID, finalText string, commentaries ...string) map[string]any {
	items := make([]any, 0, len(commentaries)+1)
	for index, commentary := range commentaries {
		items = append(items, map[string]any{
			"id": fmt.Sprintf("%s-commentary-%d", turnID, index+1), "type": "agentMessage", "phase": "commentary", "text": commentary,
		})
	}
	items = append(items, map[string]any{"id": "final", "type": "agentMessage", "phase": "final_answer", "text": finalText})
	return map[string]any{"thread": map[string]any{"id": threadID, "status": "completed", "turns": []any{map[string]any{
		"id": turnID, "status": "completed", "items": items,
	}}}}
}

func activeSyncService(t *testing.T) *Service {
	t.Helper()
	service := newTestService(t)
	service.cfg.SyncGroupID = -1001
	ctx := context.Background()
	if err := service.store.BeginSyncActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	for index, topicID := range []int64{11, 12} {
		threadID := fmt.Sprintf("thread-%d", index+1)
		if err := service.store.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "s", ChatID: -1001, TopicID: topicID, ThreadID: threadID, Title: "Topic", TelegramState: model.SyncTopicConnected}); err != nil {
			t.Fatal(err)
		}
		if err := service.store.UpsertThread(ctx, model.Thread{ID: threadID, Title: "Topic", CWD: "/tmp/project", Status: "idle"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.store.FinishSyncActivation(ctx, "s", `{}`, true); err != nil {
		t.Fatal(err)
	}
	service.SetSyncForum(&fakeSyncForum{})
	return service
}
