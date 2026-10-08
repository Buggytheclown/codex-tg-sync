package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestTaskVersionsGenerationAndChatDedup(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "chat", ThreadID: "thread", Column: model.TaskTODO})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "duplicate", ThreadID: "thread"})
	if err != nil || b.ID != a.ID {
		t.Fatalf("dedup: %#v %v", b, err)
	}
	_, err = s.MoveTask(ctx, a.ID, model.MoveTaskInput{ExpectedVersion: a.Version, Column: model.TaskPollingColumn})
	if !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("missing config: %v", err)
	}
	a, err = s.SetTaskPolling(ctx, a.ID, model.SetTaskPollingInput{ExpectedVersion: a.Version, Polling: model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.MoveTask(ctx, a.ID, model.MoveTaskInput{ExpectedVersion: 1, Column: model.TaskDone})
	if !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale: %v", err)
	}
	a, err = s.MoveTask(ctx, a.ID, model.MoveTaskInput{ExpectedVersion: a.Version, Column: model.TaskPollingColumn})
	if err != nil {
		t.Fatal(err)
	}
	generation := a.Polling.Generation
	a, err = s.MoveTask(ctx, a.ID, model.MoveTaskInput{ExpectedVersion: a.Version, Column: model.TaskTODO})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := s.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: a.ID, Generation: generation, Cursor: []byte(`{"late":true}`)}, 123)
	if err != nil || accepted {
		t.Fatalf("late outcome: %v %v", accepted, err)
	}
}

func TestTaskAcceptanceFreezesOwnedDefinitionAndRequest(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	def := model.TaskDefinition{Title: "Review {{pr}}", Config: model.TaskConfig{InitialPrompt: "Review {{pr}}", ContinuationPrompt: "Continue {{pr}}", Execution: model.TaskExecutionSettings{CWD: "/workspace", Model: "test", ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"}}, Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}}

	source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", TicketURL: "https://tracker.example/TASK-123", Priority: "high", Column: model.TaskPollingColumn, Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &def}}})
	if err != nil {
		t.Fatal(err)
	}
	outcome := model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Cursor: []byte(`{"pr":1}`), Events: []model.PollingEvent{{Key: "pr:1", SourceKey: "pr:1", Variables: map[string]string{"pr": "1"}, Payload: []byte(`{"reasons":["assigned"]}`)}}}
	accepted, err := s.AcceptTaskPollingOutcome(ctx, outcome, 123)
	if err != nil || !accepted {
		t.Fatalf("accept: %v %v", accepted, err)
	}
	def.Title = "changed"
	def.Config.InitialPrompt = "changed"
	def.Config.ContinuationPrompt = "future continuation"
	def.Config.Execution.CWD = "/future-workspace"
	def.Polling.Config = []byte(`{"future_wait":true}`)
	source.Polling.Action.ChildDefinition = &def
	source, err = s.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: *source.Polling})
	if err != nil {
		t.Fatal(err)
	}
	outcome.Generation = source.Polling.Generation

	accepted, err = s.AcceptTaskPollingOutcome(ctx, outcome, 123)
	if err != nil || !accepted {
		t.Fatalf("duplicate check: %v %v", accepted, err)
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("tasks: %d %v", len(tasks), err)
	}
	var child model.Task
	for _, item := range tasks {
		if item.ID != source.ID {
			child = item
		}
	}
	if child.TicketURL != "" || child.Priority != "normal" || child.Title != "source: Review 1" || child.Config.InitialPrompt != "Review 1" || child.Polling == nil || child.Column != model.TaskInbox || len(child.Requests) != 1 {
		t.Fatalf("snapshot: %#v", child)
	}
	request, err := s.GetExternalLaunchRequest(ctx, child.Requests[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if request.AutoStart || request.Prompt != "Review 1" || request.TaskID != child.ID || request.ApprovalPolicy == nil || *request.ApprovalPolicy != "on-request" {
		t.Fatalf("request: %#v", request)
	}
	if child.SourceTaskID != source.ID || child.Config.Execution.CWD != "/workspace" || child.Config.ContinuationPrompt != "Continue 1" || string(child.Polling.Config) != `{}` {
		t.Fatalf("child lost owned settings or source link: %#v", child)
	}
	outcome.Events[0].Key = "pr:2"
	outcome.Events[0].SourceKey = "pr:2"
	outcome.Events[0].Variables["pr"] = "2"
	if accepted, err := s.AcceptTaskPollingOutcome(ctx, outcome, 123); err != nil || !accepted {
		t.Fatalf("future event: %v %v", accepted, err)
	}
	all, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var future *model.Task
	for i := range all {
		if all[i].SourceKey == "pr:2" {
			future = &all[i]
		}
	}
	if future == nil || future.Title != "source: changed" || future.Config.InitialPrompt != "changed" || future.Config.ContinuationPrompt != "future continuation" || future.Config.Execution.CWD != "/future-workspace" || future.Column != model.TaskInbox || future.Polling == nil || string(future.Polling.Config) != `{"future_wait":true}` {
		t.Fatalf("future task did not copy source definition with inactive waiting: %#v", future)
	}
	frozen, err := s.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil || frozen.Prompt != request.Prompt || frozen.CWD != request.CWD {
		t.Fatalf("source edit changed pending request: %#v %v", frozen, err)
	}
	_, err = s.MoveTask(ctx, child.ID, model.MoveTaskInput{ExpectedVersion: child.Version, Column: model.TaskDone})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.ClaimExternalLaunchRequest(ctx, request.ID)
	if err != nil || claimed {
		t.Fatalf("cancelled claimed: %v %v", claimed, err)
	}
	request, _ = s.GetExternalLaunchRequest(ctx, request.ID)
	if request.Status != model.ExternalLaunchDismissed {
		t.Fatalf("not dismissed: %#v", request)
	}
}

func makeTaskBoardSource(t *testing.T, s *Store, mode string) (*model.Task, model.TaskPollingOutcome) {
	t.Helper()
	ctx := context.Background()
	def := model.TaskDefinition{Title: "Review {{pr_id}}", Config: model.TaskConfig{InitialPrompt: "Review {{pr_id}}", ContinuationPrompt: "Continue {{pr_id}}", Execution: model.TaskExecutionSettings{CWD: "/project", Model: "fixed-model", ReasoningEffort: "high"}}, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"{{pr_id}}"}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}}

	source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", Column: model.TaskPollingColumn, Polling: &model.TaskPolling{Type: "pr_source", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &def}}})
	if err != nil {
		t.Fatal(err)
	}
	return source, model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Cursor: []byte(`{"seen":1}`), Events: []model.PollingEvent{{Key: "pr:1", SourceKey: "pr:1", Payload: []byte(`{"facts":["new"]}`), Cursor: []byte(`{"revision":"baseline"}`), Variables: map[string]string{"pr_id": "1"}}}}
}

func TestTaskEventFailureRollsBackEveryWrite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	if accepted, err := s.AcceptTaskPollingOutcome(ctx, out, 0); err == nil || accepted {
		t.Fatalf("missing approval topic accepted %v %v", accepted, err)
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("partial child: %v %v", tasks, err)
	}
	events, err := s.ListTaskEvents(ctx, source.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("partial event: %v %v", events, err)
	}
	got, _ := s.GetTask(ctx, source.ID)
	if string(got.Polling.Cursor) != "{}" || got.Version != source.Version {
		t.Fatalf("partial cursor: %#v", got)
	}
	s.SetTaskPollingValidator(func(p model.TaskPolling) error {
		if p.Type == "pr_wait" {
			return errors.New("invalid child")
		}
		return nil
	})
	if accepted, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err == nil || accepted {
		t.Fatalf("invalid child accepted %v %v", accepted, err)
	}
	tasks, _ = s.ListTasks(ctx)
	if len(tasks) != 1 {
		t.Fatal("invalid child committed")
	}
	s.SetTaskPollingValidator(nil)
	if accepted, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !accepted {
		t.Fatalf("retry after rollback: %v %v", accepted, err)
	}
	tasks, _ = s.ListTasks(ctx)
	for _, task := range tasks {
		if task.ID != source.ID {
			if string(task.Polling.Cursor) != `{"revision":"baseline"}` || string(task.Polling.Config) != `{"complete_on_merge":false,"pr_id":"1"}` {
				t.Fatalf("child baseline/substitution: %#v", task.Polling)
			}
		}
	}
}

func TestTaskCancellationAfterClaimAndImmutablePendingSnapshot(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s.ListTasks(ctx)
	var child *model.Task
	for i := range tasks {
		if tasks[i].ID != source.ID {
			child = &tasks[i]
		}
	}
	requestID := child.Requests[0].ID
	config := child.Config
	config.InitialPrompt = "edited"
	config.Execution.Model = "edited-model"
	var err error
	child, err = s.UpdateTask(ctx, child.ID, model.UpdateTaskInput{ExpectedVersion: child.Version, Title: "edited", Config: config})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := s.GetExternalLaunchRequest(ctx, requestID)
	if request.Prompt != "Review 1" || request.Model != "fixed-model" {
		t.Fatalf("mutable request: %#v", request)
	}
	claimed, err := s.ClaimExternalLaunchRequest(ctx, requestID)
	if err != nil || !claimed {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	if _, err = s.MoveTask(ctx, child.ID, model.MoveTaskInput{ExpectedVersion: child.Version, Column: model.TaskTODO}); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.CompleteExternalLaunchRequest(ctx, requestID, model.ExternalLaunchSessionStarted, "thread", "turn", "", ""); err != nil || changed {
		t.Fatalf("cancelled dispatch completion: %v %v", changed, err)
	}
	if changed, err := s.ResetExternalLaunchRequestPending(ctx, requestID, "busy", "busy"); err != nil || changed {
		t.Fatalf("cancelled reset: %v %v", changed, err)
	}
	if changed, err := s.RetryExternalLaunchRequest(ctx, requestID); err != nil || changed {
		t.Fatalf("cancelled retry: %v %v", changed, err)
	}
	got, _ := s.GetTask(ctx, child.ID)
	if got.ThreadID != "" {
		t.Fatal("cancelled task linked")
	}
}

func TestTaskBindingAndProjectionDoNotChangeColumn(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	items, _ := s.ListTasks(ctx)
	var child model.Task
	for _, item := range items {
		if item.ID != source.ID {
			child = item
		}
	}
	id := child.Requests[0].ID
	if ok, err := s.ClaimExternalLaunchRequest(ctx, id); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := s.CompleteExternalLaunchRequest(ctx, id, model.ExternalLaunchSessionStarted, "task-thread", "first-turn", "", ""); err != nil || !ok {
		t.Fatalf("bind %v %v", ok, err)
	}
	if err := s.UpsertSnapshot(ctx, "task-thread", model.ThreadSnapshotState{LastSeenTurnID: "manual-followup", LastSeenTurnStatus: "completed"}); err != nil {
		t.Fatal(err)
	}
	childPtr, _ := s.GetTask(ctx, child.ID)
	if childPtr.ThreadID != "task-thread" || childPtr.Column != model.TaskInbox || childPtr.Execution == nil || childPtr.Execution.LastSeenTurnID != "manual-followup" {
		t.Fatalf("projection: %#v", childPtr)
	}
	ids, err := s.ListTrackedTaskThreadIDs(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "task-thread" {
		t.Fatalf("tracked ids %v %v", ids, err)
	}
	// Arm the copied once waiter; keep its accepted baseline across rearming.
	childPtr, err = s.MoveTask(ctx, child.ID, model.MoveTaskInput{ExpectedVersion: childPtr.Version, Column: model.TaskPollingColumn})
	if err != nil {
		t.Fatal(err)
	}
	outcome := model.TaskPollingOutcome{TaskID: child.ID, Generation: childPtr.Polling.Generation, Cursor: []byte(`{"revision":"next"}`), Events: []model.PollingEvent{{Key: "commit:next", Payload: []byte(`{"commit":"next"}`)}}}
	if ok, err := s.AcceptTaskPollingOutcome(ctx, outcome, 123); err != nil || !ok {
		t.Fatalf("resume event %v %v", ok, err)
	}
	childPtr, _ = s.GetTask(ctx, child.ID)
	var resume model.TaskRequestInfo
	for _, r := range childPtr.Requests {
		if r.ID != id {
			resume = r
		}
	}
	if resume.TargetThreadID != "task-thread" || resume.Prompt != "Continue 1" {
		t.Fatalf("resume snapshot %#v", resume)
	}
	if ok, err := s.ClaimExternalLaunchRequest(ctx, resume.ID); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := s.CompleteExternalLaunchRequest(ctx, resume.ID, model.ExternalLaunchSessionStarted, "wrong-thread", "turn", "", ""); err == nil || ok {
		t.Fatalf("wrong target accepted: %v %v", ok, err)
	}
	request, _ := s.GetExternalLaunchRequest(ctx, resume.ID)
	if request.Status != model.ExternalLaunchStarting || request.ThreadID != "" {
		t.Fatalf("mismatch did not roll back: %#v", request)
	}
	if ok, err := s.CompleteExternalLaunchRequest(ctx, resume.ID, model.ExternalLaunchSessionStarted, "task-thread", "second-turn", "", ""); err != nil || !ok {
		t.Fatalf("same target: %v %v", ok, err)
	}
}

func TestTaskPollingCursorIdentityAndDueActivation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "wait", Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"1","conditions":["new_commit"]}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if err != nil {
		t.Fatal(err)
	}
	due, err := s.ListDueTaskPolling(ctx, time.Now(), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("inactive due %v %v", due, err)
	}
	task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskPollingColumn})
	if err != nil {
		t.Fatal(err)
	}
	due, err = s.ListDueTaskPolling(ctx, time.Now(), 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("active due %v %v", due, err)
	}
	if accepted, err := s.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, Cursor: []byte(`{"revision":"one"}`), NextCheckAt: model.TimeString(time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))}, 0); err != nil || !accepted {
		t.Fatal(err)
	}
	task, _ = s.GetTask(ctx, task.ID)
	config := *task.Polling
	config.Config = []byte(`{"pr_id":"1","conditions":["all_resolved"]}`)
	task, err = s.SetTaskPolling(ctx, task.ID, model.SetTaskPollingInput{ExpectedVersion: task.Version, Polling: config})
	if err != nil {
		t.Fatal(err)
	}
	if string(task.Polling.Cursor) != `{"revision":"one"}` {
		t.Fatal("same object lost baseline")
	}
	oldGeneration := task.Polling.Generation
	config = *task.Polling
	config.Config = []byte(`{"pr_id":"2"}`)
	task, err = s.SetTaskPolling(ctx, task.ID, model.SetTaskPollingInput{ExpectedVersion: task.Version, Polling: config})
	if err != nil {
		t.Fatal(err)
	}
	if string(task.Polling.Cursor) != "{}" || task.Polling.Generation <= oldGeneration {
		t.Fatalf("new object kept baseline %#v", task.Polling)
	}
}

func TestTaskConcurrentEditAndAcceptance(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	items, _ := s.ListTasks(ctx)
	if len(items) != 2 {
		t.Fatalf("duplicate child count %d", len(items))
	}
	for _, item := range items {
		if item.ID != source.ID && len(item.Requests) != 1 {
			t.Fatalf("duplicate requests %d", len(item.Requests))
		}
	}
	task, _ := s.CreateTask(ctx, model.CreateTaskInput{Title: "edit"})
	errs = make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: "changed"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	successes := 0
	for err := range errs {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrTaskConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("edit winners %d", successes)
	}
}

func TestTaskMovePreservesUnknownAndResolveBindsAtomically(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	items, _ := s.ListTasks(ctx)
	var child model.Task
	for _, item := range items {
		if item.ID != source.ID {
			child = item
		}
	}
	id := child.Requests[0].ID
	if claimed, err := s.ClaimExternalLaunchRequest(ctx, id); err != nil || !claimed {
		t.Fatal(err)
	}
	if changed, err := s.CompleteExternalLaunchRequest(ctx, id, model.ExternalLaunchOutcomeUnknown, "", "", "unknown", "ambiguous"); err != nil || !changed {
		t.Fatal(err)
	}
	if _, err := s.MoveTask(ctx, child.ID, model.MoveTaskInput{ExpectedVersion: child.Version, Column: model.TaskDone}); err != nil {
		t.Fatal(err)
	}
	request, _ := s.GetExternalLaunchRequest(ctx, id)
	if request.Status != model.ExternalLaunchOutcomeUnknown {
		t.Fatalf("unknown was cancelled: %#v", request)
	}
	if changed, err := s.ResolveExternalLaunchOutcome(ctx, id, "confirmed-thread", "confirmed-turn"); err != nil || !changed {
		t.Fatalf("resolve %v %v", changed, err)
	}
	linked, _ := s.GetTask(ctx, child.ID)
	if linked.ThreadID != "confirmed-thread" || linked.Column != model.TaskDone {
		t.Fatalf("resolution changed column %#v", linked)
	}
}

func TestTaskRequestKeepsConfiguredPromptAndPersistsFacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	out.Events[0].SourceURL = "https://example.test/pr/1"
	out.Events[0].Payload = []byte(`{"reasons":["request_changes"]}`)
	if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	items, _ := s.ListTasks(ctx)
	for _, item := range items {
		if item.ID == source.ID {
			continue
		}
		request, _ := s.GetExternalLaunchRequest(ctx, item.Requests[0].ID)
		if request.Prompt != item.Config.InitialPrompt {
			t.Fatalf("prompt modified %q", request.Prompt)
		}
		if request.Source != source.Title {
			t.Fatalf("source=%q", request.Source)
		}
	}
	events, _ := s.ListTaskEvents(ctx, source.ID)
	found := false
	for _, event := range events {
		if event.Kind == "polling_event" {
			found = strings.Contains(string(event.Payload), `"request_changes"`)
		}
	}
	if !found {
		t.Fatal("event facts missing")
	}
}

func TestTaskDefinitionCopiesDataWithoutRecursiveVariableExpansion(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	def := model.TaskDefinition{Title: "Review {{title}}", Config: model.TaskConfig{InitialPrompt: "Review {{title}} and {{other}}", ContinuationPrompt: "continue"}, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"{{pr_id}}","label":"{{title}}"}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}, Generation: 999, Cursor: []byte(`{"illegal":true}`)}}

	raw, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "illegal") || strings.Contains(string(raw), "generation") {
		t.Fatalf("definition contains runtime state %s", raw)
	}
	source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", Column: model.TaskPollingColumn, Polling: &model.TaskPolling{Type: "pr_source", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &def}}})
	if err != nil {
		t.Fatal(err)
	}
	event := model.PollingEvent{Key: "data", SourceKey: "data", Title: `literal "{{other}}"`, Variables: map[string]string{"other": "expanded", "pr_id": "1"}, Payload: []byte(`{}`)}
	if _, err := s.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Events: []model.PollingEvent{event}}, 123); err != nil {
		t.Fatal(err)
	}
	items, _ := s.ListTasks(ctx)
	for _, item := range items {
		if item.ID == source.ID {
			continue
		}
		if item.Title != `source: Review literal "{{other}}"` || item.Config.InitialPrompt != `Review literal "{{other}}" and expanded` {
			t.Fatalf("recursive substitution: %#v", item)
		}
		var cfg map[string]any
		if err := json.Unmarshal(item.Polling.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		if cfg["label"] != event.Title {
			t.Fatalf("JSON interpolation escaped incorrectly %s", item.Polling.Config)
		}
	}
}

func TestTaskPollingActivationRequiresReadyContinuation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "not ready", Config: model.TaskConfig{ContinuationPrompt: "continue"}, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"1"}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskPollingColumn}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("unlinked activated: %v", err)
	}
	linked, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "linked", ThreadID: "thread", Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"1"}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTask(ctx, linked.ID, model.MoveTaskInput{ExpectedVersion: linked.Version, Column: model.TaskPollingColumn}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("blank continuation activated: %v", err)
	}
}

func TestTaskDoneCancelsDefiniteFailureAndDoesNotRearmRetry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	items, _ := s.ListTasks(ctx)
	var child model.Task
	for _, item := range items {
		if item.ID != source.ID {
			child = item
		}
	}
	id := child.Requests[0].ID
	if ok, err := s.ClaimExternalLaunchRequest(ctx, id); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := s.CompleteExternalLaunchRequest(ctx, id, model.ExternalLaunchFailed, "", "", "pre_dispatch", "not started"); err != nil || !ok {
		t.Fatal(err)
	}
	done, err := s.MoveTask(ctx, child.ID, model.MoveTaskInput{ExpectedVersion: child.Version, Column: model.TaskDone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.MoveTask(ctx, done.ID, model.MoveTaskInput{ExpectedVersion: done.Version, Column: model.TaskInbox}); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RetryExternalLaunchRequest(ctx, id); err != nil || ok {
		t.Fatalf("cancelled failure retried %v %v", ok, err)
	}
	request, _ := s.GetExternalLaunchRequest(ctx, id)
	if request.Status != model.ExternalLaunchDismissed {
		t.Fatalf("failure not dismissed %#v", request)
	}
}

func TestTaskCannotClearActiveContinuationOrCombineDistinctIdentities(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "active", ThreadID: "thread-one", SourceKey: "source-one", Column: model.TaskPollingColumn, Config: model.TaskConfig{ContinuationPrompt: "continue"}, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"1"}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateTask(ctx, task.ID, model.UpdateTaskInput{ExpectedVersion: task.Version, Title: task.Title, Config: model.TaskConfig{}}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("cleared active continuation: %v", err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Version != task.Version || got.Config.ContinuationPrompt != "continue" {
		t.Fatal("invalid edit changed task")
	}
	if _, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "second", ThreadID: "thread-two", SourceKey: "source-two"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "conflicting identities", ThreadID: "thread-one", SourceKey: "source-two"}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("contradictory identity accepted: %v", err)
	}
}

func TestSourceDefinitionValidationAndAtomicEdit(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	def := model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "first", ContinuationPrompt: "again", Execution: model.TaskExecutionSettings{CWD: "/work"}}}
	p := model.TaskPolling{Type: "pr", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &def}}
	source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"missing definition", "blank prompt", "recursive source", "repeat child", "resume missing prompt"} {
		t.Run(kind, func(t *testing.T) {
			d := def
			bad := p
			bad.Config = []byte(`{"changed":true}`)
			bad.Action.ChildDefinition = &d
			switch kind {
			case "missing definition":
				bad.Action.ChildDefinition = nil
			case "blank prompt":
				d.Config.InitialPrompt = ""
			case "recursive source":
				d.Polling = &p
			case "repeat child":
				d.Polling = &model.TaskPolling{Type: "pr", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
			case "resume missing prompt":
				d.Config.ContinuationPrompt = ""
				d.Polling = &model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}
			}
			if _, err := s.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: bad}); !errors.Is(err, ErrTaskInvalid) {
				t.Fatalf("invalid source accepted: %v", err)
			}
			got, err := s.GetTask(ctx, source.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != source.Version || string(got.Polling.Config) != `{}` || got.Polling.Action.ChildDefinition.Config.InitialPrompt != "first" {
				t.Fatalf("invalid edit partially persisted: %#v", got)
			}
		})
	}
	d := def
	d.Config.InitialPrompt = "future"
	p.Action.ChildDefinition = &d
	p.Config = []byte(`{"changed":true}`)
	got, err := s.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: p})
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Polling.Config) != `{"changed":true}` || got.Polling.Action.ChildDefinition.Config.InitialPrompt != "future" || got.Version != source.Version+1 || got.Polling.Generation != source.Polling.Generation+1 {
		t.Fatalf("source definition and trigger not saved together: %#v", got)
	}
}
