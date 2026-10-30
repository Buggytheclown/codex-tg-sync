package polling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

type mergePR struct {
	fakePR
	checks     []arcanumreview.PRCheck
	checkErr   error
	checkReads int
	reads      []int64
	count      int
	common     string
	failAt     int64
	errAt      int64
}

func failedCheck() arcanumreview.PRCheck {
	return arcanumreview.PRCheck{Key: arcanumreview.CheckKey{System: "ci", Type: "tests"}, Required: true, Status: "failure", Description: "Tests failed", URI: "https://example.com/run"}
}
func (f *mergePR) ListAuthoredOpen(context.Context, string) ([]arcanumreview.PullRequest, error) {
	prs := []arcanumreview.PullRequest{}
	for id := int64(1); id <= int64(f.count); id++ {
		prs = append(prs, arcanumreview.PullRequest{ID: id, Author: "alice", Summary: fmt.Sprint("PR ", id)})
	}
	return prs, nil
}
func (f *mergePR) ReadPRSnapshot(_ context.Context, id int64) (arcanumreview.PRSnapshot, error) {
	f.reads = append(f.reads, id)
	common := f.common
	if common == "" {
		common = "waiting_for_review"
	}
	return arcanumreview.PRSnapshot{ID: id, Status: "open", Author: arcanumreview.PRIdentity{Name: "alice"}, CommonStatus: common, PublishedDiffID: id, HeadCommit: "head", Comments: []arcanumreview.PRComment{}}, nil
}
func (f *mergePR) ReadPRChecks(_ context.Context, id int64) ([]arcanumreview.PRCheck, error) {
	f.checkReads++
	if id == f.errAt {
		return nil, errors.New("check page unavailable")
	}
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	if f.failAt > 0 {
		if id == f.failAt {
			return []arcanumreview.PRCheck{failedCheck()}, nil
		}
		return []arcanumreview.PRCheck{}, nil
	}
	return f.checks, nil
}
func feedbackSource() model.TaskPolling {
	return model.TaskPolling{TaskID: "source", Type: TypeChanges, Mode: model.PollingRepeat, Config: jsonBytes(Config{Login: "alice", CheckMergeFailures: true}), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "Fix {{pr_title}}", Config: model.TaskConfig{InitialPrompt: "Inspect PR checks"}}}}
}
func TestFeedbackScansBeyondHealthyCandidatesAndSeedsChildBaseline(t *testing.T) {
	client := &mergePR{count: 7, failAt: 6}
	p := feedbackSource()
	checker := NewChecker(client)
	first, err := checker.Check(context.Background(), p, time.Now())
	if err != nil || len(first.Events) != 0 || len(client.reads) != 5 {
		t.Fatalf("%#v %v reads=%v", first, err, client.reads)
	}
	p.Cursor = first.Cursor
	second, err := checker.Check(context.Background(), p, time.Now())
	if err != nil || len(second.Events) != 1 || second.Events[0].SourceKey != "arcanum_changes:6" {
		t.Fatalf("%#v %v", second, err)
	}
	var baseline PRCursor
	if err = json.Unmarshal(second.Events[0].Cursor, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.ChecksDiffID != 6 || len(baseline.FailedChecks) != 1 {
		t.Fatal(baseline)
	}
	var facts struct {
		Failed []arcanumreview.PRCheck `json:"failed_checks"`
	}
	json.Unmarshal(second.Events[0].Payload, &facts)
	if len(facts.Failed) != 1 || facts.Failed[0].URI == "" {
		t.Fatal(string(second.Events[0].Payload))
	}
	p.Cursor = second.Cursor
	third, err := checker.Check(context.Background(), p, time.Now())
	if err != nil || len(third.Events) != 0 {
		t.Fatalf("duplicate %#v %v", third, err)
	}
}
func TestFeedbackRequestChangesStillTriggersWithGreenChecks(t *testing.T) {
	client := &mergePR{count: 1, common: "waiting_for_changes", checks: []arcanumreview.PRCheck{}}
	out, err := NewChecker(client).Check(context.Background(), feedbackSource(), time.Now())
	if err != nil || len(out.Events) != 1 {
		t.Fatalf("%#v %v", out, err)
	}
}
func TestMergeChecksOptInAndErrorPreserveCursor(t *testing.T) {
	p := model.TaskPolling{TaskID: "wait", Type: TypePRWait, Mode: model.PollingOnce, Generation: 1, Config: jsonBytes(Config{PRID: "1", Conditions: []string{ConditionChanges}}), Action: model.TaskPollingAction{Type: model.PollingReturnTask}, Cursor: jsonBytes(PRCursor{CommonStatus: "waiting_for_review"})}
	client := &mergePR{checks: []arcanumreview.PRCheck{failedCheck()}}
	out, err := NewChecker(client).Check(context.Background(), p, time.Now())
	if err != nil || len(out.Events) != 0 || client.checkReads != 0 {
		t.Fatalf("disabled %#v %v", out, err)
	}
	p.Config = jsonBytes(Config{PRID: "1", Conditions: []string{ConditionMergeChecks}})
	client.checkErr = errors.New("checks unavailable")
	out, err = NewChecker(client).Check(context.Background(), p, time.Now())
	if err == nil || len(out.Events) != 0 || string(out.Cursor) != string(p.Cursor) {
		t.Fatalf("%#v %v", out, err)
	}
	source := feedbackSource()
	source.Cursor = []byte(`{"seen":[2]}`)
	client.count = 1
	out, err = NewChecker(client).Check(context.Background(), source, time.Now())
	if err == nil || len(out.Events) != 0 {
		t.Fatalf("source %#v %v", out, err)
	}
}
func TestMergeFailurePersistsAcrossReopenAndRearmWithoutDuplicateRequest(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	store.SetTaskPollingValidator(Validate)
	p := model.TaskPolling{Type: TypePRWait, Mode: model.PollingOnce, Config: jsonBytes(Config{PRID: "1", Conditions: []string{ConditionMergeChecks}}), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}
	task, err := store.CreateTask(ctx, model.CreateTaskInput{Title: "Fix PR", Column: model.TaskPollingColumn, ThreadID: "chat", Config: model.TaskConfig{ContinuationPrompt: "Inspect PR checks"}, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	client := &mergePR{checks: []arcanumreview.PRCheck{failedCheck()}}
	checker := NewChecker(client)
	out, err := checker.Check(ctx, *task.Polling, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
		t.Fatalf("%v %v", ok, err)
	}
	task, err = store.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Column != model.TaskInbox || len(task.Requests) != 1 || task.Requests[0].TargetThreadID != "chat" {
		t.Fatalf("%#v", task)
	}
	events, err := store.ListTaskEvents(ctx, task.ID)
	if err != nil || len(events) == 0 {
		t.Fatalf("%v %v", events, err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.SetTaskPollingValidator(Validate)
	task, err = store.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskPollingColumn})
	if err != nil {
		t.Fatal(err)
	}
	out, err = checker.Check(ctx, *task.Polling, time.Now())
	if err != nil || len(out.Events) != 0 {
		t.Fatalf("duplicate %#v %v", out, err)
	}
	if _, err = store.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	task, err = store.GetTask(ctx, task.ID)
	if err != nil || len(task.Requests) != 1 {
		t.Fatalf("%#v %v", task, err)
	}
	client.checks = []arcanumreview.PRCheck{}
	out, err = checker.Check(ctx, *task.Polling, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	task, _ = store.GetTask(ctx, task.ID)
	client.checks = []arcanumreview.PRCheck{failedCheck()}
	out, err = checker.Check(ctx, *task.Polling, time.Now())
	if err != nil || len(out.Events) != 1 {
		t.Fatalf("retry %#v %v", out, err)
	}
}

func TestMergeCheckConfigRejectsWrongSourceAndMalformedFlag(t *testing.T) {
	p := feedbackSource()
	p.Type = TypeReview
	if err := Validate(p); err == nil {
		t.Fatal("merge check discovery accepted on review source")
	}
	p = feedbackSource()
	p.Config = []byte(`{"login":"alice","check_merge_failures":"true"}`)
	if err := Validate(p); err == nil {
		t.Fatal("malformed flag accepted")
	}
}

func TestFeedbackCheckErrorRejectsPartialPass(t *testing.T) {
	p := feedbackSource()
	p.Cursor = []byte(`{"seen":[7]}`)
	client := &mergePR{count: 2, checks: []arcanumreview.PRCheck{failedCheck()}, errAt: 2}
	store := &runnerStore{due: []model.TaskPolling{p}}
	if err := NewRunner(store, NewChecker(client), 123).PollOnce(context.Background()); err == nil {
		t.Fatal("partial read error not reported")
	}
	if len(store.outcomes) != 1 || store.outcomes[0].Error == "" || len(store.outcomes[0].Events) != 0 || string(store.outcomes[0].Cursor) != string(p.Cursor) {
		t.Fatalf("%#v", store.outcomes)
	}
}
