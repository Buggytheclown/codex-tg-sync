package polling

import (
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"testing"
)

func published(id int64, author string, reply int64, status string) arcanumreview.PRComment {
	draft := false
	return arcanumreview.PRComment{ID: id, Author: arcanumreview.PRIdentity{Name: author}, ReplyToID: reply, IssueStatus: status, IsDraft: &draft}
}
func TestPRWaitAnyKeepsAcceptedHeadWhileTrackingNewReviewComments(t *testing.T) {
	cfg := Config{Login: "reviewer", Conditions: []string{ConditionCommit, ConditionAnswered}}
	old := PRCursor{HeadCommit: "old", CommonStatus: "waiting_for_review"}
	now := arcanumreview.PRSnapshot{HeadCommit: "new", CommonStatus: "waiting_for_review", Comments: []arcanumreview.PRComment{published(1, "reviewer", 0, "open"), published(2, "author", 1, "")}}
	next, reasons := ComparePR(cfg, old, now, 2)
	if len(reasons) != 2 || next.HeadCommit != "new" {
		t.Fatalf("reasons %v cursor %#v", reasons, next)
	}
	_, again := ComparePR(cfg, next, now, 3)
	if len(again) != 0 {
		t.Fatalf("consumed event retriggered: %v", again)
	}
}
func TestPRWaitNoVacuousOrSelfDraftReply(t *testing.T) {
	cfg := Config{Login: "reviewer", Conditions: []string{ConditionAnswered}}
	old := PRCursor{HeadCommit: "a", Generation: 1}
	if _, r := ComparePR(cfg, old, arcanumreview.PRSnapshot{}, 1); len(r) > 0 {
		t.Fatal(r)
	}
	comments := []arcanumreview.PRComment{published(1, "reviewer", 0, "open"), published(2, "reviewer", 1, ""), published(3, "author", 2, "")}
	comments[2].IsDraft = nil
	if _, r := ComparePR(cfg, old, arcanumreview.PRSnapshot{Comments: comments}, 2); len(r) > 0 {
		t.Fatal(r)
	}
	comments[2] = published(3, "author", 2, "")
	if _, r := ComparePR(cfg, old, arcanumreview.PRSnapshot{Comments: comments}, 2); len(r) != 1 {
		t.Fatalf("nested reply not recognized %v", r)
	}
}
func TestPRWaitAllResolvedAndStatusEdges(t *testing.T) {
	cfg := Config{Conditions: []string{ConditionResolved, ConditionChanges}}
	old := PRCursor{Generation: 1, CommonStatus: "waiting_for_review", Tracked: []int64{1, 2}}
	snap := arcanumreview.PRSnapshot{CommonStatus: "waiting_for_review", Comments: []arcanumreview.PRComment{published(1, "r", 0, "resolved"), published(2, "r", 0, "open")}}
	next, r := ComparePR(cfg, old, snap, 1)
	if len(r) > 0 {
		t.Fatal(r)
	}
	snap.Comments[1].IssueStatus = "resolved"
	snap.CommonStatus = "waiting_for_changes"
	next, r = ComparePR(cfg, next, snap, 1)
	if len(r) != 2 {
		t.Fatalf("expected two ANY reasons %v", r)
	}
	if _, r = ComparePR(cfg, next, snap, 2); len(r) > 0 {
		t.Fatalf("unchanged status repeated %v", r)
	}
}
func TestPRWaitFirstObservationSeedsCommitAndRequestChanges(t *testing.T) {
	cfg := Config{Conditions: []string{ConditionCommit, ConditionChanges, ConditionResolved}}
	_, r := ComparePR(cfg, PRCursor{}, arcanumreview.PRSnapshot{HeadCommit: "a", CommonStatus: "waiting_for_changes"}, 1)
	if len(r) > 0 {
		t.Fatalf("first observation isn't an edge %v", r)
	}
}

func TestPRWaitRetainsHeadAcrossDraftAndFindsIssueResolvedBeforeRearm(t *testing.T) {
	issue := published(1, "reviewer", 0, "resolved")
	issue.CreatedAt = "2026-10-06T10:01:00Z"
	old := PRCursor{HeadCommit: "accepted", Since: "2026-10-06T10:00:00Z"}
	next, reasons := ComparePR(Config{Conditions: []string{ConditionResolved}}, old, arcanumreview.PRSnapshot{Comments: []arcanumreview.PRComment{issue}}, 2)
	if len(reasons) != 1 || next.HeadCommit != "accepted" {
		t.Fatalf("cursor %#v reasons %v", next, reasons)
	}
	old.Since = "2026-10-06T11:00:00Z"
	if _, reasons = ComparePR(Config{Conditions: []string{ConditionResolved}}, old, arcanumreview.PRSnapshot{Comments: []arcanumreview.PRComment{issue}}, 2); len(reasons) > 0 {
		t.Fatal("historical resolved issue retriggered")
	}
}
