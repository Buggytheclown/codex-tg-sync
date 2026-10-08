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
	login  string
	status string
	calls  int
	fail   bool
	author string
}

func (f *fakePR) ListAssigned(_ context.Context, login string) ([]arcanumreview.PullRequest, error) {
	f.login = login
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
	return arcanumreview.PRSnapshot{Status: f.status, Author: arcanumreview.PRIdentity{Name: f.author}, HeadCommit: "abc", CommonStatus: "waiting_for_review", Comments: []arcanumreview.PRComment{}}, nil
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

func TestMergedWaitEmitsNoContinuationAndSourceSkipsMerged(t *testing.T) {
	client := &fakePR{status: "merged"}
	checker := NewChecker(client)
	wait := model.TaskPolling{TaskID: "wait", Type: TypePRWait, Mode: model.PollingOnce, Config: jsonBytes(Config{PRID: "42", Conditions: []string{ConditionCommit}}), Action: model.TaskPollingAction{Type: model.PollingResumeTask}, Cursor: jsonBytes(PRCursor{HeadCommit: "old"})}
	out, err := checker.Check(context.Background(), wait, time.Now())
	if err != nil || out.PRStatus != "merged" || len(out.Events) != 0 || string(out.Cursor) != string(wait.Cursor) {
		t.Fatalf("wait: %#v %v", out, err)
	}
	source := model.TaskPolling{TaskID: "source", Type: TypeReview, Mode: model.PollingRepeat, Config: jsonBytes(Config{Login: "reviewer"}), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "work"}}}}
	out, err = checker.Check(context.Background(), source, time.Now())
	if err != nil || len(out.Events) != 0 {
		t.Fatalf("source: %#v %v", out, err)
	}
}

type staleMergedSourcePR struct {
	fakePR
	reads []int64
}

func (f *staleMergedSourcePR) ListAssigned(context.Context, string) ([]arcanumreview.PullRequest, error) {
	out := []arcanumreview.PullRequest{}
	for id := int64(1); id <= 6; id++ {
		out = append(out, arcanumreview.PullRequest{ID: id, Author: "alice", Summary: "Change"})
	}
	return out, nil
}
func (f *staleMergedSourcePR) ReadPRSnapshot(_ context.Context, id int64) (arcanumreview.PRSnapshot, error) {
	f.reads = append(f.reads, id)
	if id <= 5 {
		return arcanumreview.PRSnapshot{ID: id, Status: "merged"}, nil
	}
	return arcanumreview.PRSnapshot{ID: id, Status: "open", Author: arcanumreview.PRIdentity{Name: "alice"}, HeadCommit: "abc", CommonStatus: "waiting_for_review"}, nil
}
func TestMergedAssignedCandidatesDoNotStarveLaterOpenPR(t *testing.T) {
	client := &staleMergedSourcePR{}
	checker := NewChecker(client)
	source := model.TaskPolling{TaskID: "source", Type: TypeReview, Mode: model.PollingRepeat, Config: jsonBytes(Config{Login: "reviewer"}), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "work"}}}}
	out, err := checker.Check(context.Background(), source, time.Now())
	if err != nil || len(out.Events) != 0 || len(client.reads) != 5 {
		t.Fatalf("first: %#v reads=%v err=%v", out, client.reads, err)
	}
	source.Cursor = out.Cursor
	out, err = checker.Check(context.Background(), source, time.Now())
	if err != nil || len(out.Events) != 1 || out.Events[0].SourceKey != "arcanum_review:6" || len(client.reads) != 6 {
		t.Fatalf("second: %#v reads=%v err=%v", out, client.reads, err)
	}
	source.Cursor = out.Cursor
	out, err = checker.Check(context.Background(), source, time.Now())
	if err != nil || len(out.Events) != 0 || len(client.reads) != 6 {
		t.Fatalf("repeat: %#v reads=%v err=%v", out, client.reads, err)
	}
}

func TestConfiguredLoginOverridesStoredSourceAndChild(t *testing.T) {
	client := &fakePR{}
	checker := NewChecker(client)
	login := "configured-reviewer"
	checker.Login = &login
	wait := model.TaskPolling{Type: TypePRWait, Mode: model.PollingOnce, Config: jsonBytes(Config{PRID: "{{pr_id}}", Login: "stale-reviewer", Conditions: []string{ConditionAnswered}}), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
	source := model.TaskPolling{Type: TypeReview, Mode: model.PollingRepeat, Config: jsonBytes(Config{Login: "stale-reviewer"}), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "work"}, Polling: &wait}}}
	effective, err := WithLogin(source, login)
	if err != nil {
		t.Fatal(err)
	}
	childCfg, _ := DecodeConfig(*effective.Action.ChildDefinition.Polling)
	if childCfg.Login != login {
		t.Fatalf("child login %q", childCfg.Login)
	}
	original, _ := DecodeConfig(*source.Action.ChildDefinition.Polling)
	if original.Login != "stale-reviewer" {
		t.Fatal("normalization mutated original")
	}
	if _, err := checker.Check(context.Background(), source, time.Now()); err != nil {
		t.Fatal(err)
	}
	if client.login != login {
		t.Fatalf("queried account %q", client.login)
	}
	source.Config = []byte(`{"login":123}`)
	if _, err := checker.Check(context.Background(), source, time.Now()); err == nil {
		t.Fatal("malformed typed config accepted")
	}
	source.Config = jsonBytes(Config{Login: "stale-reviewer"})
	source.Action.ChildDefinition.Polling.Config = []byte(`{"pr_id":"42","complete_on_merge":"false"}`)
	if _, err := WithLogin(source, login); err == nil {
		t.Fatal("malformed child config accepted")
	}
}

func TestConfiguredLoginRejectsNonObjectConfig(t *testing.T) {
	for _, raw := range []string{"null", "[]", "123", "{} {}"} {
		p := model.TaskPolling{Type: TypeReview, Config: []byte(raw)}
		if _, err := WithLogin(p, "reviewer"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}

func TestConfiguredLoginRequiredOnlyForAccountConditions(t *testing.T) {
	wait := model.TaskPolling{Type: TypePRWait, Config: jsonBytes(Config{PRID: "42", Conditions: []string{ConditionCommit}})}
	if _, err := WithLogin(wait, ""); err != nil {
		t.Fatal(err)
	}
	wait.Config = jsonBytes(Config{PRID: "42", Conditions: []string{ConditionAnswered}})
	if _, err := WithLogin(wait, ""); err == nil {
		t.Fatal("missing configured account accepted")
	}
}
