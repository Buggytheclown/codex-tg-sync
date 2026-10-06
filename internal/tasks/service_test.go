package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/polling"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func TestServiceDefaultsAndTypedValidation(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	ctx := context.Background()
	service.SetExecutionDefaults(model.TaskExecutionSettings{CWD: "/workspace", Model: "default-model", ReasoningEffort: "high", ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"})
	service.SetPollingValidator(func(p model.TaskPolling) error {
		if p.Type != "known" {
			return errors.New("unknown source")
		}
		return nil
	})
	_, err = service.CreateTask(ctx, model.CreateTaskInput{Title: "bad", Polling: &model.TaskPolling{Type: "unknown", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed validation error: %v", err)
	}
	task, err := service.AddChat(ctx, "thread", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if task.Column != model.TaskTODO || task.Config.Execution.CWD != "/workspace" || task.Config.Execution.Model != "default-model" || task.Config.Execution.ApprovalPolicy != "on-request" {
		t.Fatalf("defaults %#v", task)
	}
	service.SetExecutionDefaults(model.TaskExecutionSettings{CWD: "/different", Model: "different"})
	again, err := service.AddChat(ctx, "thread", "renamed")
	if err != nil || again.ID != task.ID || again.Config.Execution.Model != "default-model" {
		t.Fatalf("dedup changed snapshot %#v %v", again, err)
	}
}

func TestSourceSwitchRejectsUnresolvedCronWaitWithoutChangingChildren(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	service := New(store)
	service.SetPollingValidator(polling.Validate)
	ctx := context.Background()
	wait := model.TaskPolling{Type: polling.TypePRWait, Mode: model.PollingOnce, Config: []byte(`{"pr_id":"{{pr_id}}","conditions":["new_commit"]}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
	def := model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "old prompt", Execution: model.TaskExecutionSettings{CWD: "/old"}}, Polling: &wait}
	p := model.TaskPolling{Type: polling.TypeReview, Mode: model.PollingRepeat, Config: []byte(`{"login":"reviewer"}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &def}}
	source, err := service.CreateTask(ctx, model.CreateTaskInput{Title: "source", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	out := model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Events: []model.PollingEvent{{Key: "pr:1", SourceKey: "pr:1", Variables: map[string]string{"pr_id": "1"}, Payload: []byte(`{}`)}}}
	if ok, err := store.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
		t.Fatalf("initial event %v %v", ok, err)
	}
	all, err := service.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var child model.Task
	for _, item := range all {
		if item.ID != source.ID {
			child = item
		}
	}
	request, err := store.GetExternalLaunchRequest(ctx, child.Requests[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	p.Type = polling.TypeCron
	p.Config = []byte(`{"cron":"0 9 * * *","timezone":"UTC"}`)
	if _, err := service.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: p}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unresolved cron wait saved: %v", err)
	}
	if _, err := service.CreateTask(ctx, model.CreateTaskInput{Title: "bad cron", Polling: &p}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unresolved cron source created: %v", err)
	}
	same, err := service.GetTask(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	if same.Version != source.Version || same.Polling.Type != polling.TypeReview {
		t.Fatalf("invalid switch changed source: %#v", same)
	}
	wait.Config = []byte(`{"pr_id":"321","conditions":["new_commit"]}`)
	def.Polling = &wait
	def.Config.InitialPrompt = "future prompt"
	def.Config.Execution.CWD = "/future"
	p.Action.ChildDefinition = &def
	source, err = service.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: p})
	if err != nil {
		t.Fatal(err)
	}
	out.Generation = source.Polling.Generation
	out.Events = []model.PollingEvent{{Key: "cron:day", SourceKey: "cron:day", Variables: map[string]string{"date": "2026-10-06"}, Payload: []byte(`{}`)}}
	if ok, err := store.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
		t.Fatalf("explicit cron PR wait event %v %v", ok, err)
	}
	old, err := service.GetTask(ctx, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := store.GetExternalLaunchRequest(ctx, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if old.Config.InitialPrompt != "old prompt" || old.Config.Execution.CWD != "/old" || string(old.Polling.Config) != string(child.Polling.Config) || frozen.Prompt != request.Prompt || frozen.CWD != request.CWD {
		t.Fatal("source change rewrote old task or frozen request")
	}
	all, err = service.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var future *model.Task
	for i := range all {
		if all[i].SourceKey == "cron:day" {
			future = &all[i]
		}
	}
	if future == nil || future.Config.InitialPrompt != "future prompt" || future.Config.Execution.CWD != "/future" || future.Polling == nil || future.Column != model.TaskInbox {
		t.Fatalf("future cron child %#v", future)
	}
	var resolved polling.Config
	if err = json.Unmarshal(future.Polling.Config, &resolved); err != nil || resolved.PRID != "321" {
		t.Fatalf("explicit child PR waiting not preserved: %#v %v", resolved, err)
	}
}
