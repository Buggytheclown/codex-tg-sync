package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/polling"
)

type boardSearchRunner struct{ calls int }

func (r *boardSearchRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls++
	if !slices.Contains(args, "search") {
		return nil, fmt.Errorf("unexpected optional read")
	}
	return []byte(`{"review_requests":[{"id":42,"author":{"name":"alice"},"summary":"one","full_status":"open","common_status":"waiting_for_changes","active_diff_set":{"id":9,"arc_branch_heads":{"from_id":"head"}},"diff_sets":[{"id":9,"status":"published"}]},{"id":43,"author":{"name":"alice"},"summary":"two","full_status":"open","common_status":"waiting_for_changes","active_diff_set":{"id":10,"arc_branch_heads":{"from_id":"head"}},"diff_sets":[{"id":10,"status":"published"}]}],"total_count":2}`), nil
}
func TestSharedSearchUpdatesFooterAndWakesFutureRulesWithoutOptionalReads(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	s.SetTaskPollingValidator(polling.Validate)
	group := []string{polling.ConditionMergeChecks}
	key, _ := json.Marshal(group)
	ids := []string{}
	for _, prID := range []string{"42", "43"} {
		cfg, _ := json.Marshal(polling.Config{PRID: prID, Author: "alice", ConditionGroups: [][]string{{polling.ConditionChanges}, group}})
		rule := model.PollingRule{ID: "feedback", Type: polling.TypePRWait, Config: cfg, Action: model.TaskPollingAction{Type: model.PollingReturnTask, Column: model.TaskInbox}}
		rules, _ := json.Marshal(model.PollingRulesConfig{Rules: []model.PollingRule{rule}})
		task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: prID, CardType: model.CardPR, SourceURL: prID, Column: model.TaskPollingColumn, Polling: &model.TaskPolling{Type: model.PollingRules, Mode: model.PollingOnce, Config: rules, Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, task.ID)
		leaf, _ := json.Marshal(polling.PRCursor{Generation: task.Polling.Generation, HeadCommit: "head", CommonStatus: "waiting_for_review", Matches: map[string]string{string(key): "accepted-checks"}})
		cursor, _ := json.Marshal(model.PollingRulesCursor{Generation: task.Polling.Generation, Rules: map[string]model.PollingRuleState{"feedback": {Cursor: leaf, NextCheckAt: model.TimeString(time.Now().Add(24 * time.Hour).Format(time.RFC3339Nano))}}})
		if _, err := s.db.ExecContext(ctx, `UPDATE task_polling SET cursor_json=?,next_check_at=? WHERE task_id=?`, string(cursor), time.Now().Add(24*time.Hour).Format(time.RFC3339Nano), task.ID); err != nil {
			t.Fatal(err)
		}
	}
	command := &boardSearchRunner{}
	reader := arcanumreview.NewSharedReader(arcanumreview.NewCLIClient("ya", command), s)
	checker := polling.NewChecker(reader)
	tasks, err := s.ListPollingTasks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	footers, err := s.ListPRFooterDemand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := checker.Prepare(ctx, tasks, footers); err != nil {
		t.Fatal(err)
	}
	if err := reader.SearchOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := reader.PublishOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := polling.NewRunner(s, checker, 77).PollOnce(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		task, err := s.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if task.Column != model.TaskInbox || task.PRStatus == nil || task.PRStatus.CommonStatus != "waiting_for_changes" {
			t.Fatalf("shared data did not reach both consumers: %+v", task)
		}
		var state model.PollingRulesCursor
		_ = json.Unmarshal(task.Polling.Cursor, &state)
		var cursor polling.PRCursor
		_ = json.Unmarshal(state.Rules["feedback"].Cursor, &cursor)
		if cursor.Matches[string(key)] != "accepted-checks" {
			t.Fatalf("waiting OR state lost: %+v", cursor)
		}
		events, err := s.ListTaskEvents(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, event := range events {
			if event.EventKey != "" {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("accepted %d events", count)
		}
	}
	if command.calls != 1 {
		t.Fatalf("commands=%d, expected one search", command.calls)
	}
}
