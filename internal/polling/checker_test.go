package polling

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
	"time"
)

type fakePR struct {
	calls  int
	fail   bool
	author string
}

func (f *fakePR) ListAssigned(context.Context, string) ([]arcanumreview.PullRequest, error) {
	return []arcanumreview.PullRequest{{ID: 42, Author: "alice", Summary: "Change"}}, nil
}
func (f *fakePR) ListAuthoredWaitingForChanges(ctx context.Context, s string) ([]arcanumreview.PullRequest, error) {
	return f.ListAssigned(ctx, s)
}
func (f *fakePR) ReadPRSnapshot(context.Context, int64) (arcanumreview.PRSnapshot, error) {
	f.calls++
	if f.fail {
		return arcanumreview.PRSnapshot{}, errors.New("unavailable")
	}
	return arcanumreview.PRSnapshot{Author: arcanumreview.PRIdentity{Name: f.author}, HeadCommit: "abc", CommonStatus: "waiting_for_review", Comments: []arcanumreview.PRComment{}}, nil
}
func TestSourceEmitsBaselineAndStableCycleKey(t *testing.T) {
	client := &fakePR{author: "actual-author"}
	checker := NewChecker(client)
	p := model.TaskPolling{TaskID: "source", Type: TypeReview, Mode: model.PollingRepeat, Config: jsonBytes(Config{Login: "reviewer"}), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "prompt"}}}}
	out, err := checker.Check(context.Background(), p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Events) != 1 || out.Events[0].SourceKey != "arcanum_review:42" || len(out.Events[0].Cursor) == 0 {
		t.Fatalf("%#v", out)
	}
	var facts map[string]string
	if err := json.Unmarshal(out.Events[0].Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts["author"] != "actual-author" || out.Events[0].Variables["author"] != "actual-author" {
		t.Fatalf("author lost: %#v", out.Events[0])
	}
	p.Cursor = out.Cursor
	out, err = checker.Check(context.Background(), p, time.Now())
	if err != nil || len(out.Events) != 0 || client.calls != 1 {
		t.Fatalf("repeat %#v calls %d err %v", out, client.calls, err)
	}
}
func TestReadFailureKeepsCursorAndProducesNoEvent(t *testing.T) {
	p := model.TaskPolling{TaskID: "wait", Type: TypePRWait, Mode: model.PollingOnce, Config: jsonBytes(Config{PRID: "42", Conditions: []string{ConditionCommit}}), Action: model.TaskPollingAction{Type: model.PollingReturnTask}, Cursor: jsonBytes(PRCursor{HeadCommit: "old"})}
	out, err := NewChecker(&fakePR{fail: true}).Check(context.Background(), p, time.Now())
	if err == nil || len(out.Events) != 0 || string(out.Cursor) != string(p.Cursor) {
		t.Fatalf("%#v %v", out, err)
	}
}
func TestCronOccurrenceIgnoresGenerationAndPreservesLateness(t *testing.T) {
	p := model.TaskPolling{TaskID: "source", Type: TypeCron, Mode: model.PollingRepeat, Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "prompt"}}}, Config: jsonBytes(Config{Cron: "0 9 * * *", Timezone: "UTC", LegacyID: "daily", MaxLateness: "2h"})}
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	a, err := NewChecker(nil).Check(context.Background(), p, now)
	if err != nil {
		t.Fatal(err)
	}
	p.Generation = 2
	b, _ := NewChecker(nil).Check(context.Background(), p, now)
	if len(a.Events) != 1 || a.Events[0].Key != "cron:daily:2026-10-06" || a.Events[0].Key != b.Events[0].Key {
		t.Fatalf("%#v %#v", a, b)
	}
	late, _ := NewChecker(nil).Check(context.Background(), p, now.Add(2*time.Hour))
	if len(late.Events) > 0 {
		t.Fatal("late slot replayed")
	}
}

func TestWaitEventCarriesCurrentSnapshotAuthor(t *testing.T) {
	p := model.TaskPolling{TaskID: "wait", Type: TypePRWait, Mode: model.PollingOnce, Config: jsonBytes(Config{PRID: "42", Author: "stale-author", Conditions: []string{ConditionCommit}}), Action: model.TaskPollingAction{Type: model.PollingResumeTask}, Cursor: jsonBytes(PRCursor{HeadCommit: "old"})}
	out, err := NewChecker(&fakePR{author: "actual-author"}).Check(context.Background(), p, time.Now())
	if err != nil || len(out.Events) != 1 {
		t.Fatalf("out=%#v err=%v", out, err)
	}
	var facts map[string]any
	if err := json.Unmarshal(out.Events[0].Payload, &facts); err != nil {
		t.Fatal(err)
	}
	if facts["author"] != "actual-author" || out.Events[0].Variables["author"] != "actual-author" {
		t.Fatalf("author=%#v", out.Events[0])
	}
}
