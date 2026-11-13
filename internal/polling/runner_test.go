package polling

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type runnerStore struct {
	due      []model.TaskPolling
	outcomes []model.TaskPollingOutcome
	limit    int
}

func (s *runnerStore) ListTasks(context.Context) ([]model.Task, error) { return nil, nil }
func (s *runnerStore) ListDueTaskPolling(_ context.Context, _ time.Time, limit int) ([]model.TaskPolling, error) {
	s.limit = limit
	return s.due, nil
}
func (s *runnerStore) AcceptTaskPollingOutcome(_ context.Context, out model.TaskPollingOutcome, _ int64) (bool, error) {
	s.outcomes = append(s.outcomes, out)
	return true, nil
}

type runnerChecker struct {
	calls int
	err   error
}

func (c *runnerChecker) Check(_ context.Context, p model.TaskPolling, _ time.Time) (model.TaskPollingOutcome, error) {
	c.calls++
	return model.TaskPollingOutcome{PRStatus: "merged", Cursor: json.RawMessage(`{"advanced":true}`), Events: []model.PollingEvent{{Key: "partial"}}}, c.err
}
func TestRunnerErrorPreservesCursorAndRejectsPartialEvents(t *testing.T) {
	store := &runnerStore{due: []model.TaskPolling{{TaskID: "source", Generation: 4, Type: TypeReview, Cursor: json.RawMessage(`{"seen":[1]}`)}}}
	checker := &runnerChecker{err: errors.New("second PR unavailable")}
	r := NewRunner(store, checker, 77)
	r.now = func() time.Time { return time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC) }
	if err := r.PollOnce(context.Background()); err == nil {
		t.Fatal("check failure not reported")
	}
	if store.limit != 20 || len(store.outcomes) != 1 {
		t.Fatalf("store=%#v", store)
	}
	out := store.outcomes[0]
	if out.PRStatus != "" || len(out.Events) != 0 || string(out.Cursor) != `{"seen":[1]}` || out.Generation != 4 || out.Error == "" || out.NextCheckAt == "" {
		t.Fatalf("out=%#v", out)
	}
}
func TestRunnerCronResumeGraceSkipsOnlyCron(t *testing.T) {
	store := &runnerStore{due: []model.TaskPolling{{TaskID: "cron", Type: TypeCron}, {TaskID: "pr", Type: TypeReview}}}
	checker := &runnerChecker{}
	r := NewRunner(store, checker, 77)
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 1 {
		t.Fatalf("startup calls=%d", checker.calls)
	}
	for i := 0; i < 8; i++ {
		now = now.Add(30 * time.Second)
		if err := r.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if checker.calls != 10 {
		t.Fatalf("grace calls=%d", checker.calls)
	}
	now = now.Add(2 * time.Minute)
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checker.calls != 11 {
		t.Fatalf("resume calls=%d", checker.calls)
	}
}

type clockAdvancingChecker struct {
	now       *time.Time
	cronCalls int
}

func (c *clockAdvancingChecker) Check(_ context.Context, p model.TaskPolling, _ time.Time) (model.TaskPollingOutcome, error) {
	if p.Type == TypeCron {
		c.cronCalls++
	} else {
		*c.now = c.now.Add(100 * time.Second)
	}
	return model.TaskPollingOutcome{}, nil
}
func TestRunnerSlowChecksDoNotRepeatedlyResetCronGrace(t *testing.T) {
	store := &runnerStore{due: []model.TaskPolling{{TaskID: "slow-pr", Type: TypeReview}, {TaskID: "cron", Type: TypeCron}}}
	now := time.Date(2026, 10, 6, 10, 0, 0, 0, time.UTC)
	checker := &clockAdvancingChecker{now: &now}
	r := NewRunner(store, checker, 77)
	r.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if err := r.PollOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		now = now.Add(10 * time.Second)
	}
	if checker.cronCalls == 0 {
		t.Fatal("slow checks kept postponing cron indefinitely")
	}
	calls := checker.cronCalls
	now = now.Add(2 * time.Minute)
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if checker.cronCalls != calls {
		t.Fatal("actual idle/suspend gap did not reapply cron grace")
	}
}

type compactStatusStore struct {
	runnerStore
	active  []model.Task
	enabled map[string]bool
	started int
}

func (s *compactStatusStore) ListTasks(context.Context) ([]model.Task, error) {
	panic("health must not load task details/archive")
}
func (s *compactStatusStore) ListPollingTasks(context.Context) ([]model.Task, error) {
	return s.active, nil
}
func (s *compactStatusStore) ListRegisteredTaskPollers(context.Context) ([]string, error) {
	return []string{"archived"}, nil
}
func (s *compactStatusStore) RegisterTaskPoller(_ context.Context, id, _ string, _ time.Duration, enabled bool) error {
	s.enabled[id] = enabled
	return nil
}
func (s *compactStatusStore) NoteExternalPollStarted(context.Context, string)       { s.started++ }
func (s *compactStatusStore) NoteExternalPollResult(context.Context, string, error) {}

type waitingChecker struct{}

func (waitingChecker) Check(_ context.Context, p model.TaskPolling, now time.Time) (model.TaskPollingOutcome, error) {
	return model.TaskPollingOutcome{Waiting: true, Cursor: p.Cursor, NextCheckAt: timestamp(now.Add(5 * time.Minute))}, nil
}
func TestRunnerUsesCompactRegistrationAndDisablesArchivedIDsAfterRestart(t *testing.T) {
	p := model.TaskPolling{TaskID: "active", Type: TypePRWait, Config: jsonBytes(Config{PRID: "42", Conditions: []string{ConditionCommit}}), NextCheckAt: timestamp(time.Now().Add(24 * time.Hour))}
	s := &compactStatusStore{active: []model.Task{{ID: "active", Title: "active", Column: model.TaskPollingColumn, Polling: &p}}, enabled: map[string]bool{}}
	r := NewRunner(s, waitingChecker{}, 77)
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.enabled["archived"] || !s.enabled["active"] || len(s.outcomes) != 1 || s.started != 0 {
		t.Fatalf("registration/wait: %+v", s)
	}
	s.active = nil
	if err := r.PollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.enabled["active"] {
		t.Fatal("Done/deleted poller remained registered")
	}
}
