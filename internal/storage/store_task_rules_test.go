package storage

import (
	"context"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
	"time"
)

func ruleWait(action model.TaskPollingAction) model.TaskPolling {
	cfg, _ := json.Marshal(model.PollingRulesConfig{Rules: []model.PollingRule{{ID: "wait", Type: "pr_wait", Config: json.RawMessage(`{"pr_id":"42","condition_groups":[["request_changes"]]}`), Action: action}}})
	return model.TaskPolling{Type: model.PollingRules, Mode: model.PollingOnce, Config: cfg, Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
}
func TestRuleContinuationMovesTODOAndSurvivesClaim(t *testing.T) {
	for _, auto := range []bool{false, true} {
		t.Run(map[bool]string{false: "manual", true: "auto"}[auto], func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			p := ruleWait(model.TaskPollingAction{Type: model.PollingResumeTask, Column: model.TaskTODO, Prompt: "rule prompt {{reasons}}", AutoStart: auto})
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task", ThreadID: "chat", Column: model.TaskPollingColumn, Polling: &p})
			if err != nil {
				t.Fatal(err)
			}
			out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, RuleID: "wait", Cursor: json.RawMessage(`{"rules":{}}`), Events: []model.PollingEvent{{Key: "one", Payload: json.RawMessage(`{}`), Variables: map[string]string{"reasons": "failed"}}}}
			ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123)
			if err != nil || !ok {
				t.Fatal(ok, err)
			}
			task, _ = s.GetTask(ctx, task.ID)
			if task.Column != model.TaskTODO || len(task.Requests) != 1 || task.Requests[0].Prompt != "rule prompt failed" {
				t.Fatalf("%+v", task)
			}
			req, err := s.GetExternalLaunchRequest(ctx, task.Requests[0].ID)
			if err != nil || req.AutoStart != auto || req.TargetThreadID != "chat" {
				t.Fatal(req, err)
			}
			claimed, err := s.ClaimExternalLaunchRequest(ctx, req.ID)
			if err != nil || !claimed {
				t.Fatal("TODO request could not start", claimed, err)
			}
			ok, err = s.AcceptTaskPollingOutcome(ctx, out, 123)
			if err != nil || ok {
				t.Fatal("stale outcome accepted", ok, err)
			}
		})
	}
}

func TestRuleDoneAndCursorUpgradePreserveOldWork(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: json.RawMessage(`{"pr_id":"42"}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}, Cursor: json.RawMessage(`{"head_commit":"accepted"}`)}
	p := ruleWait(model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskDone})
	var cfg model.PollingRulesConfig
	_ = json.Unmarshal(p.Config, &cfg)
	cfg.Rules[0].ID = "legacy-wait"
	p.Config, _ = json.Marshal(cfg)
	raw := reconcileRuleCursor(old, p)
	var state model.PollingRulesCursor
	_ = json.Unmarshal(raw, &state)
	if string(state.Rules["legacy-wait"].Cursor) != string(old.Cursor) {
		t.Fatal(string(raw))
	}
	cfg.Rules[0].Config = json.RawMessage(`{"pr_id":"43"}`)
	p.Config, _ = json.Marshal(cfg)
	_ = json.Unmarshal(reconcileRuleCursor(old, p), &state)
	// Decode into a fresh map: JSON merge semantics retain old keys otherwise.
	var changed model.PollingRulesCursor
	_ = json.Unmarshal(reconcileRuleCursor(old, p), &changed)
	if len(changed.Rules) != 0 {
		t.Fatal("new PR inherited old evidence")
	}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "done", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, RuleID: "legacy-wait", Events: []model.PollingEvent{{Key: "done", Payload: json.RawMessage(`{}`)}}}
	ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	task, _ = s.GetTask(ctx, task.ID)
	if task.Column != model.TaskDone || len(task.Requests) != 0 {
		t.Fatal(task)
	}
	due, err := s.ListDueTaskPolling(ctx, time.Now().Add(time.Hour), 20)
	if err != nil || len(due) != 0 {
		t.Fatal(due, err)
	}
}

func TestSourceCanCreateDoneTaskWithoutLaunch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, out := makeTaskBoardSource(t, s, "")
	p := *source.Polling
	p.Action.InitialLaunch = &model.TaskLaunchPolicy{Mode: "none"}
	p.Action.Column = model.TaskDone
	p.Action.ChildDefinition.Config.InitialPrompt = ""
	source, err := s.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: p})
	if err != nil {
		t.Fatal(err)
	}
	out.Generation = source.Polling.Generation
	_, err = s.AcceptTaskPollingOutcome(ctx, out, 123)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.SourceTaskID == source.ID && (task.Column != model.TaskDone || len(task.Requests) > 0) {
			t.Fatal(task)
		}
	}
	requests, err := s.ListExternalLaunchRequestsByStatus(ctx, nil, 20)
	if err != nil || len(requests) > 0 {
		t.Fatal(requests, err)
	}
	source, _ = s.GetTask(ctx, source.ID)
	if source.Column != model.TaskPollingColumn {
		t.Fatal("source discovery stopped")
	}
}
