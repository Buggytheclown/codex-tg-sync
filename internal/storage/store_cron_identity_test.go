package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/polling"
)

func TestCronIdentityCompatibilityAndRescheduling(t *testing.T) {
	for _, source := range []bool{false, true} {
		for _, history := range []string{"new", "known", "unknown", "invalid", "imported"} {
			t.Run(fmt.Sprintf("source=%t/%s", source, history), func(t *testing.T) {
				ctx := context.Background()
				path := filepath.Join(t.TempDir(), "state.db")
				s, err := Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { s.Close() }()
				config := json.RawMessage(`{"cron":"0 13 * * *","timezone":"Europe/Minsk","max_lateness":"5m"}`)
				p := model.TaskPolling{Type: "cron", Mode: model.PollingOnce, Config: config, Action: model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskInbox}}
				if source {
					p.Mode = model.PollingRepeat
					p.Action = model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "scheduled", Config: model.TaskConfig{InitialPrompt: "run"}}}
				} else {
					rules, _ := json.Marshal(model.PollingRulesConfig{Rules: []model.PollingRule{{ID: "ежедневно", Type: "cron", Config: config, Action: p.Action}}})
					p = model.TaskPolling{Type: model.PollingRules, Mode: model.PollingOnce, Config: rules, Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
				}
				task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "cron", Column: model.TaskPollingColumn, Polling: &p})
				if err != nil {
					t.Fatal(err)
				}
				morning := time.Date(2026, 11, 3, 10, 1, 0, 0, time.UTC)
				check := func(now time.Time, legacy bool) {
					t.Helper()
					task, err = s.GetTask(ctx, task.ID)
					if err != nil {
						t.Fatal(err)
					}
					out, err := polling.NewChecker(nil).Check(ctx, *task.Polling, now)
					if err != nil || len(out.Events) != 1 {
						t.Fatalf("check %+v %v", out, err)
					}
					if legacy {
						event := &out.Events[0]
						prefix := "cron:" + task.ID + ":"
						if !source {
							prefix = "rule:ежедневно:" + prefix
						}
						event.Key = prefix + "2026-11-03"
						event.SourceKey = "cron:" + task.ID + ":2026-11-03"
					}
					if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
						t.Fatal(ok, err)
					}
				}
				rearm := func(spec string) {
					t.Helper()
					task, err = s.GetTask(ctx, task.ID)
					if err != nil {
						t.Fatal(err)
					}
					p := *task.Polling
					cfg := json.RawMessage(fmt.Sprintf(`{"cron":%q,"timezone":"Europe/Minsk","max_lateness":"5m"}`, spec))
					if source {
						p.Config = cfg
					} else {
						var rules model.PollingRulesConfig
						if err := json.Unmarshal(p.Config, &rules); err != nil {
							t.Fatal(err)
						}
						rules.Rules[0].Config = cfg
						p.Config, _ = json.Marshal(rules)
					}
					task, err = s.SetTaskPolling(ctx, task.ID, model.SetTaskPollingInput{ExpectedVersion: task.Version, Polling: p})
					if err != nil {
						t.Fatal(err)
					}
					if task.Column != model.TaskPollingColumn {
						task, err = s.MoveTask(ctx, task.ID, model.MoveTaskInput{ExpectedVersion: task.Version, Column: model.TaskPollingColumn})
						if err != nil {
							t.Fatal(err)
						}
					}
				}
				eventCount := func(want int) {
					t.Helper()
					var count int
					if err := s.db.QueryRow(`SELECT count(*) FROM task_events WHERE task_id=? AND coalesce(event_key,'')<>''`, task.ID).Scan(&count); err != nil {
						t.Fatal(err)
					}
					if count != want {
						t.Fatalf("accepted events=%d want=%d", count, want)
					}
					if source {
						items, err := s.ListTasks(ctx)
						if err != nil {
							t.Fatal(err)
						}
						if len(items) != want+1 {
							t.Fatalf("tasks=%d want=%d", len(items), want+1)
						}
						for _, item := range items {
							if item.ID != task.ID && len(item.Requests) != 1 {
								t.Fatalf("child requests: %+v", item.Requests)
							}
						}
					}
				}
				check(morning, history != "new")
				eventCount(1)
				if history == "unknown" || history == "imported" || history == "invalid" {
					payload := "{}"
					if history == "invalid" {
						payload = `{"scheduled_at":"bad"}`
					}
					if _, err := s.db.Exec(`UPDATE task_events SET payload_json=? WHERE task_id=? AND coalesce(event_key,'')<>''`, payload, task.ID); err != nil {
						t.Fatal(err)
					}
				}
				if history == "imported" {
					if _, err := s.db.Exec(`UPDATE task_events SET kind='legacy_import' WHERE task_id=? AND coalesce(event_key,'')<>''`, task.ID); err != nil {
						t.Fatal(err)
					}
				}
				s.Close()
				s, err = Open(path)
				if err != nil {
					t.Fatal(err)
				}
				rearm("0 13 * * *")
				check(morning.Add(time.Minute), false)
				eventCount(1)
				rearm("0 16 * * *")
				check(morning.Add(3*time.Hour), false)
				blocked := history == "unknown" || history == "invalid" || history == "imported"
				want := 2
				if blocked {
					want = 1
				}
				eventCount(want)
				rearm("0 13 * * *")
				check(morning, false)
				eventCount(want)
				rearm("0 13 * * *")
				check(morning.Add(24*time.Hour), false)
				eventCount(want + 1)
				// Compatibility is read-only: old historical keys remain intact.
				if history != "new" {
					events, err := s.ListTaskEvents(ctx, task.ID)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, event := range events {
						if strings.HasSuffix(event.EventKey, ":2026-11-03") {
							found = true
						}
					}
					if !found {
						t.Fatal("legacy history was rewritten")
					}
				}
			})
		}
	}
}

func TestCronCompatibilityPreservesRuleAndOwnerScope(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	config := json.RawMessage(`{"cron":"0 13 * * *","timezone":"Europe/Minsk","max_lateness":"5m"}`)
	action := model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskInbox}
	rules, _ := json.Marshal(model.PollingRulesConfig{Rules: []model.PollingRule{{ID: "one", Type: "cron", Config: config, Action: action}, {ID: "two", Type: "cron", Config: config, Action: action}}})
	p := model.TaskPolling{Type: model.PollingRules, Mode: model.PollingOnce, Config: rules, Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "rules", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "other", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "rule:two:cron:" + task.ID + ":"
	// Unknown times protect only their rule/owner. Reuse a key under another task
	// to verify the owner predicate independently of the key's owner component.
	for i, entry := range []struct{ owner, key string }{{task.ID, "rule:one:cron:" + task.ID + ":2026-11-03"}, {other.ID, prefix + "2026-11-03"}} {
		if _, err := s.db.Exec(`INSERT INTO task_events(id,task_id,kind,event_key,payload_json,created_at) VALUES(?,?,'legacy_import',?,'{}','old')`, fmt.Sprint(i), entry.owner, entry.key); err != nil {
			t.Fatal(err)
		}
	}
	out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, RuleID: "two", Events: []model.PollingEvent{{Key: prefix + "2026-11-03T10:00:00Z", Payload: []byte(`{"scheduled_at":"2026-11-03T13:00:00+03:00"}`)}}}
	if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
		t.Fatal(ok, err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Column != model.TaskInbox {
		t.Fatal("another owner or rule blocked the slot")
	}
}

func TestCronCompatibilityComparesInstantsAcrossLocalDates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := model.TaskPolling{Type: "cron", Mode: model.PollingOnce, Config: []byte(`{"cron":"0 10 * * *","timezone":"UTC"}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskInbox}}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "timezone", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "cron:" + task.ID + ":"
	if _, err := s.db.Exec(`INSERT INTO task_events(id,task_id,kind,event_key,payload_json,created_at) VALUES('old',?,'polling_event',?,?,'old')`, task.ID, prefix+"2026-11-02", `{"scheduled_at":"2026-11-02T23:00:00-11:00"}`); err != nil {
		t.Fatal(err)
	}
	out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, Events: []model.PollingEvent{{Key: prefix + "2026-11-03T10:00:00Z", Payload: []byte(`{"scheduled_at":"2026-11-03T10:00:00Z"}`)}}}
	if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
		t.Fatal(ok, err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Column != model.TaskPollingColumn {
		t.Fatal("same instant replayed after zone change")
	}
}

func TestCronCompatibilitySurvivesFlatToRulesConversion(t *testing.T) {
	for _, timestampKey := range []bool{false, true} {
		t.Run(fmt.Sprint(timestampKey), func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			cfg := json.RawMessage(`{"cron":"0 10 * * *","timezone":"UTC"}`)
			action := model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskInbox}
			rules, _ := json.Marshal(model.PollingRulesConfig{Rules: []model.PollingRule{{ID: "legacy-wait", Type: "cron", Config: cfg, Action: action}}})
			p := model.TaskPolling{Type: model.PollingRules, Mode: model.PollingOnce, Config: rules, Action: model.TaskPollingAction{Type: model.PollingReturnTask}}
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "converted", Column: model.TaskPollingColumn, Polling: &p})
			if err != nil {
				t.Fatal(err)
			}
			prefix := "cron:" + task.ID + ":"
			oldKey := prefix + "2026-11-03"
			if timestampKey {
				oldKey += "T10:00:00Z"
			}
			if _, err := s.db.Exec(`INSERT INTO task_events(id,task_id,kind,event_key,payload_json,created_at) VALUES('old',?,'polling_event',?,?,'old')`, task.ID, oldKey, `{"scheduled_at":"2026-11-03T10:00:00Z"}`); err != nil {
				t.Fatal(err)
			}
			out, err := polling.NewChecker(nil).Check(ctx, *task.Polling, time.Date(2026, 11, 3, 10, 1, 0, 0, time.UTC))
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
				t.Fatal(ok, err)
			}
			got, _ := s.GetTask(ctx, task.ID)
			if got.Column != model.TaskPollingColumn {
				t.Fatal("conversion replayed a slot")
			}
		})
	}
}

func TestCronIdentityAcceptanceFailureCanRetryWithoutPartialChild(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := model.TaskPolling{Type: "cron", Mode: model.PollingRepeat, Config: []byte(`{"cron":"0 10 * * *","timezone":"UTC"}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "cron child", Config: model.TaskConfig{InitialPrompt: "run"}}}}
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", Column: model.TaskPollingColumn, Polling: &p})
	if err != nil {
		t.Fatal(err)
	}
	out, err := polling.NewChecker(nil).Check(ctx, *task.Polling, time.Date(2026, 11, 3, 10, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 0); err == nil || ok {
		t.Fatal("missing approval route accepted", ok, err)
	}
	items, err := s.ListTasks(ctx)
	if err != nil || len(items) != 1 {
		t.Fatal("partial child", items, err)
	}
	events, err := s.ListTaskEvents(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == "polling_event" {
			t.Fatal("failed attempt consumed occurrence")
		}
	}
	for i := 0; i < 2; i++ {
		if ok, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	items, err = s.ListTasks(ctx)
	if err != nil || len(items) != 2 {
		t.Fatal("retry duplicated child", items, err)
	}
}
