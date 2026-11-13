package arcanumreview

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type snapshotRunner struct {
	outputs [][]byte
	calls   [][]string
	err     error
}

func (r *snapshotRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	if r.err != nil {
		return nil, r.err
	}
	if len(r.outputs) == 0 {
		return nil, errors.New("unexpected command")
	}
	b := r.outputs[0]
	r.outputs = r.outputs[1:]
	return b, nil
}
func TestPRCommentsPreservePublicationAndReplies(t *testing.T) {
	r := &snapshotRunner{outputs: [][]byte{[]byte(`[{"id":11,"is_draft":false,"issue_status":"open","author":{"name":"reviewer"}},{"id":12,"reply_to_id":11,"is_draft":false,"author":{"name":"alice"}}]`)}}
	comments, err := NewCLIClient("ya", r).ReadPRComments(context.Background(), 42)
	if err != nil || len(comments) != 2 || !comments[0].Published() || comments[1].ReplyToID != 11 {
		t.Fatalf("%+v %v", comments, err)
	}
	if !strings.Contains(strings.Join(r.calls[0], " "), "--with-dropped") {
		t.Fatal("missing dropped comments")
	}
}
func TestPRCommentsRejectUnknownAndReadFailures(t *testing.T) {
	for _, body := range []string{`null`, `{}`} {
		r := &snapshotRunner{outputs: [][]byte{[]byte(body)}}
		if _, err := NewCLIClient("ya", r).ReadPRComments(context.Background(), 42); err == nil {
			t.Fatal("unknown comments accepted")
		}
	}
	if _, err := NewCLIClient("ya", &snapshotRunner{err: errors.New("404")}).ReadPRComments(context.Background(), 42); err == nil {
		t.Fatal("read error accepted")
	}
}
func TestFallbackInfoValidatesExactIdentity(t *testing.T) {
	for _, id := range []int64{42, 43} {
		r := &snapshotRunner{outputs: [][]byte{[]byte(`{"id":42,"status":"merged"}`)}}
		s, err := NewCLIClient("ya", r).ReadPRInfo(context.Background(), id)
		if (err == nil) != (id == 42) {
			t.Fatalf("%+v %v", s, err)
		}
	}
}
func TestSearchNormalizesPublishedRevisionAndRejectsLifecycleConflict(t *testing.T) {
	r := &snapshotRunner{outputs: [][]byte{[]byte(`{"review_requests":[{"id":42,"full_status":"open","common_status":"waiting_for_review","active_diff_set":{"id":12,"arc_branch_heads":{"from_id":"draft"}},"diff_sets":[{"id":11,"status":"published","published_at":"2026-10-02T00:00:00Z"},{"id":12,"status":"draft"},{"id":9,"status":"published","published_at":"2026-10-01T00:00:00Z"}]}]}`)}}
	result, err := NewCLIClient("ya", r).Search(context.Background(), "author(alice)", 100, nil)
	if err != nil || len(result.Items) != 1 || result.Items[0].Snapshot.PublishedDiffID != 11 || result.Items[0].Snapshot.HeadCommit != "" {
		t.Fatalf("%+v %v", result, err)
	}
	for _, p := range []SearchItem{{Lifecycle: "open", CommonStatus: "merged"}, {Lifecycle: "merged", CommonStatus: "waiting_for_review"}} {
		if p.snapshot().Status != "" {
			t.Fatal("conflicting lifecycle accepted")
		}
	}
}
