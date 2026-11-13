package polling

import (
	"context"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
	"time"
)

func TestPartialCoveragePreservesWaitingGroupAndAllowsReadyOR(t *testing.T) {
	group := []string{ConditionMergeChecks}
	key, _ := json.Marshal(group)
	old := PRCursor{Generation: 1, HeadCommit: "head", CommonStatus: "waiting_for_review", Matches: map[string]string{string(key): "accepted"}, ChecksDiffID: 3, FailedChecks: []arcanumreview.CheckKey{{System: "ci", Type: "tests"}}}
	cfg := Config{ConditionGroups: [][]string{{ConditionChanges}, group}}
	s := arcanumreview.PRSnapshot{Status: "open", CommonStatus: "waiting_for_changes", HeadCommit: "head", PublishedDiffID: 3, Coverage: &arcanumreview.Coverage{SearchAt: time.Now()}}
	next, reasons := ComparePR(cfg, old, s, 1)
	if len(reasons) != 1 || reasons[0] != ConditionChanges {
		t.Fatalf("reasons %v", reasons)
	}
	if next.Matches[string(key)] != "accepted" || len(next.FailedChecks) != 1 {
		t.Fatalf("waiting state lost: %+v", next)
	}
	cfg.ConditionGroups = [][]string{{ConditionChanges, ConditionMergeChecks}}
	next, reasons = ComparePR(cfg, old, s, 1)
	if len(reasons) != 0 || next.ChecksDiffID != old.ChecksDiffID {
		t.Fatalf("incomplete AND: %+v %v", next, reasons)
	}
}

func TestFreshSlowObservationSurvivesNewerSearchInOtherORGroup(t *testing.T) {
	now := time.Now().UTC()
	c := NewChecker(nil)
	cfg := Config{ConditionGroups: [][]string{{ConditionChanges}, {ConditionMergeChecks}}}
	p := model.TaskPolling{TaskID: "task", Generation: 1, LastAttemptAt: timestamp(now.Add(5 * time.Second))}
	old := PRCursor{Generation: 1, Observations: map[string]string{groupKey([]string{ConditionChanges}): now.Add(5 * time.Second).Format(time.RFC3339Nano)}}
	s := arcanumreview.PRSnapshot{ID: 1, Status: "open", CommonStatus: "waiting_for_review", PublishedDiffID: 3, Checks: []arcanumreview.PRCheck{{Key: arcanumreview.CheckKey{System: "ci", Type: "tests"}, Required: true, Status: "failure"}}, Coverage: &arcanumreview.Coverage{SearchAt: now.Add(5 * time.Second), ChecksAt: now, Checks: true}}
	out := model.TaskPollingOutcome{}
	if !c.applyCoverage(context.Background(), p, cfg, &old, &s, now.Add(10*time.Second), &out) {
		t.Fatal("fresh checks were suppressed by other group")
	}
	next, reasons := ComparePR(cfg, old, s, 1)
	if len(reasons) != 1 || reasons[0] != ConditionMergeChecks || next.Observations[groupKey([]string{ConditionMergeChecks})] == "" {
		t.Fatalf("%+v %v", next, reasons)
	}
}
