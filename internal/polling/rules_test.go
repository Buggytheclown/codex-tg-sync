package polling

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
	"time"
)

func TestRuleGroupsRequireSimultaneousFacts(t *testing.T) {
	cfg := Config{ConditionGroups: [][]string{{ConditionChanges, ConditionMergeChecks}}}
	failed := arcanumreview.PRSnapshot{HeadCommit: "a", PublishedDiffID: 1, CommonStatus: "waiting_for_review", Checks: []arcanumreview.PRCheck{{Key: arcanumreview.CheckKey{System: "ci", Type: "tests"}, Required: true, Status: "failure"}}}
	old, reasons := ComparePR(cfg, PRCursor{}, failed, 1)
	if len(reasons) != 0 {
		t.Fatal(reasons)
	}
	failed.CommonStatus = "waiting_for_changes"
	failed.Checks = []arcanumreview.PRCheck{}
	old, reasons = ComparePR(cfg, old, failed, 1)
	if len(reasons) != 0 {
		t.Fatal("latched facts", reasons)
	}
	failed.Checks = []arcanumreview.PRCheck{{Key: arcanumreview.CheckKey{System: "ci", Type: "tests"}, Required: true, Status: "failure"}}
	old, reasons = ComparePR(cfg, old, failed, 1)
	if len(reasons) != 2 {
		t.Fatal(reasons)
	}
	_, reasons = ComparePR(cfg, old, failed, 2)
	if len(reasons) != 0 {
		t.Fatal("unchanged facts replayed", reasons)
	}
}

func TestRulesChooseDoneAndKeepGeneration(t *testing.T) {
	rules := []model.PollingRule{
		{ID: "prompt", Type: TypePRWait, Config: jsonBytes(Config{PRID: "42", ConditionGroups: [][]string{{ConditionMerged}}}), Action: model.TaskPollingAction{Type: model.PollingResumeTask, Column: model.TaskTODO, Prompt: "continue {{reasons}}"}},
		{ID: "done", Type: TypePRWait, Config: jsonBytes(Config{PRID: "42", ConditionGroups: [][]string{{ConditionMerged}}}), Action: model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskDone}},
	}
	p := model.TaskPolling{TaskID: "t", Generation: 8, Type: model.PollingRules, Mode: model.PollingOnce, Config: jsonBytes(model.PollingRulesConfig{Rules: rules}), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
	client := &fakePR{status: "merged"}
	out, err := NewChecker(client).Check(context.Background(), p, time.Now())
	if err != nil || out.RuleID != "done" || len(out.Events) != 1 || out.Generation != 8 {
		t.Fatalf("%+v %v", out, err)
	}
	if client.calls != 1 {
		t.Fatal(client.calls)
	}
}

func TestRuleCursorSurvivesUnmatchedANDAndObservesRecovery(t *testing.T) {
	cfg := Config{ConditionGroups: [][]string{{ConditionCommit, ConditionChanges}}}
	old := PRCursor{HeadCommit: "old", CommonStatus: "waiting_for_review"}
	snapshot := arcanumreview.PRSnapshot{HeadCommit: "new", CommonStatus: "waiting_for_review"}
	next, reasons := ComparePR(cfg, old, snapshot, 1)
	if len(reasons) > 0 || next.HeadCommit != "old" {
		t.Fatal("partial AND consumed head", next, reasons)
	}
	snapshot.CommonStatus = "waiting_for_changes"
	next, reasons = ComparePR(cfg, next, snapshot, 1)
	if len(reasons) != 2 || next.HeadCommit != "new" {
		t.Fatal(next, reasons)
	}
}
func TestRulesErrorDoesNotPublishPartialCursorOrEvents(t *testing.T) {
	rule := model.PollingRule{ID: "check", Type: TypePRWait, Config: jsonBytes(Config{PRID: "42", ConditionGroups: [][]string{{ConditionMergeChecks}}}), Action: model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskInbox}}
	p := model.TaskPolling{TaskID: "t", Generation: 1, Type: model.PollingRules, Mode: model.PollingOnce, Config: jsonBytes(model.PollingRulesConfig{Rules: []model.PollingRule{rule}}), Action: model.TaskPollingAction{Type: model.PollingReturnTask}, Cursor: jsonBytes(model.PollingRulesCursor{})}
	client := &mergePR{checkErr: fmt.Errorf("unavailable")}
	out, err := NewChecker(client).Check(context.Background(), p, time.Now())
	if err == nil || len(out.Events) > 0 || string(out.Cursor) != string(p.Cursor) {
		t.Fatal(out, err)
	}
}
func TestFeedbackGroupsAndChildRuleBaseline(t *testing.T) {
	client := &mergePR{count: 1, checks: []arcanumreview.PRCheck{failedCheck()}}
	p := feedbackSource()
	p.Config = jsonBytes(Config{Login: "alice", ConditionGroups: [][]string{{ConditionChanges, ConditionMergeChecks}}})
	child := model.TaskPolling{Type: model.PollingRules, Mode: model.PollingOnce, Action: model.TaskPollingAction{Type: model.PollingReturnTask}, Config: jsonBytes(model.PollingRulesConfig{Rules: []model.PollingRule{{ID: "feedback", Type: TypePRWait, Config: jsonBytes(Config{PRID: "{{pr_id}}", ConditionGroups: [][]string{{ConditionChanges}, {ConditionMergeChecks}}}), Action: model.TaskPollingAction{Type: model.PollingResumeTask, Column: model.TaskTODO, Prompt: "continue {{reasons}}"}}}})}
	p.Action.ChildDefinition.Polling = &child
	before := string(child.Config)
	if err := Validate(p); err != nil {
		t.Fatal(err)
	}
	if string(child.Config) != before {
		t.Fatal("validation mutated template")
	}
	first, err := NewChecker(client).Check(context.Background(), p, time.Now())
	if err != nil || len(first.Events) > 0 {
		t.Fatal(first, err)
	}
	client.common = "waiting_for_changes"
	out, err := NewChecker(client).Check(context.Background(), p, time.Now())
	if err != nil || len(out.Events) != 1 {
		t.Fatal(out, err)
	}
	child.TaskID = "child"
	child.Generation = 1
	child.Cursor = out.Events[0].Cursor
	var cfg model.PollingRulesConfig
	_ = json.Unmarshal(child.Config, &cfg)
	cfg.Rules[0].Config = jsonBytes(Config{PRID: "1", ConditionGroups: [][]string{{ConditionChanges}, {ConditionMergeChecks}}})
	child.Config = jsonBytes(cfg)
	repeated, err := NewChecker(client).Check(context.Background(), child, time.Now())
	if err != nil || len(repeated.Events) > 0 {
		t.Fatal("source evidence replayed", repeated, err)
	}
	client.checks = []arcanumreview.PRCheck{}
	client.common = "waiting_for_review"
	child.Cursor = repeated.Cursor
	child.Generation++
	recovered, err := NewChecker(client).Check(context.Background(), child, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	client.checks = []arcanumreview.PRCheck{failedCheck()}
	child.Cursor = recovered.Cursor
	child.Generation++
	retry, err := NewChecker(client).Check(context.Background(), child, time.Now())
	if err != nil || len(retry.Events) != 1 {
		t.Fatal("retry lost", retry, err)
	}
}
func TestScheduledRuleAndValidation(t *testing.T) {
	rule := model.PollingRule{ID: "clock", Type: TypeCron, Config: jsonBytes(Config{Cron: "0 9 * * *", Timezone: "UTC", MaxLateness: "2h"}), Action: model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskInbox}}
	p := model.TaskPolling{TaskID: "t", Type: model.PollingRules, Mode: model.PollingOnce, Action: model.TaskPollingAction{Type: model.PollingReturnTask}, Config: jsonBytes(model.PollingRulesConfig{Rules: []model.PollingRule{rule}})}
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	out, err := NewChecker(nil).Check(context.Background(), p, now)
	if err != nil || out.RuleID != "clock" || len(out.Events) != 1 {
		t.Fatal(out, err)
	}
	rule.Action.Column = model.TaskPollingColumn
	p.Config = jsonBytes(model.PollingRulesConfig{Rules: []model.PollingRule{rule}})
	if Validate(p) == nil {
		t.Fatal("action can stay in Polling")
	}
	rule.Action.Column = model.TaskInbox
	p.Config = jsonBytes(model.PollingRulesConfig{Rules: []model.PollingRule{rule, rule}})
	if Validate(p) == nil {
		t.Fatal("duplicate rule identity")
	}
}

func TestORRecoveryDoesNotReplayStillTrueCondition(t *testing.T) {
	cfg := Config{ConditionGroups: [][]string{{ConditionChanges}, {ConditionMergeChecks}}}
	current := arcanumreview.PRSnapshot{HeadCommit: "a", PublishedDiffID: 1, CommonStatus: "waiting_for_changes", Checks: []arcanumreview.PRCheck{failedCheck()}}
	old, reasons := ComparePR(cfg, PRCursor{}, current, 1)
	if len(reasons) != 2 {
		t.Fatal(reasons)
	}
	current.Checks = []arcanumreview.PRCheck{}
	old, reasons = ComparePR(cfg, old, current, 2)
	if len(reasons) > 0 {
		t.Fatal("recovery replayed existing Request changes", reasons)
	}
	current.Checks = []arcanumreview.PRCheck{failedCheck()}
	_, reasons = ComparePR(cfg, old, current, 2)
	if len(reasons) == 0 {
		t.Fatal("new failure lost while Request changes remained true")
	}
}

func TestPartialCheckRecoveryDoesNotWakeUntilNewFailure(t *testing.T) {
	cfg := Config{ConditionGroups: [][]string{{ConditionMergeChecks}}}
	a, b := failedCheck(), failedCheck()
	b.Key.Type = "lint"
	current := arcanumreview.PRSnapshot{HeadCommit: "a", PublishedDiffID: 1, Checks: []arcanumreview.PRCheck{a, b}}
	old, reasons := ComparePR(cfg, PRCursor{}, current, 1)
	if len(reasons) != 1 {
		t.Fatal(reasons)
	}
	current.Checks = []arcanumreview.PRCheck{a}
	old, reasons = ComparePR(cfg, old, current, 2)
	if len(reasons) > 0 {
		t.Fatal("partial recovery fired", reasons)
	}
	current.Checks = []arcanumreview.PRCheck{a, b}
	_, reasons = ComparePR(cfg, old, current, 2)
	if len(reasons) != 1 {
		t.Fatal("check retry lost", reasons)
	}
}
