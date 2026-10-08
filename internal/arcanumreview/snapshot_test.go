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
func TestReadPRSnapshotUsesPublishedSearchHeadAndV2Comments(t *testing.T) {
	r := &snapshotRunner{outputs: [][]byte{
		[]byte(`{"id":42,"status":"open","author":{"uid":"u1","name":"alice"}}`),
		[]byte(`{"review_requests":[{"id":42,"common_status":"waiting_for_changes","active_diff_set":{"id":9,"arc_branch_heads":{"from_id":"abc"}},"diff_sets":[{"id":9,"status":"published"}]}],"total_count":1}`),
		[]byte(`[{"id":11,"is_draft":false,"issue_status":"open","author":{"uid":"u2","name":"reviewer"}},{"id":12,"reply_to_id":11,"is_draft":false,"author":{"uid":"u1","name":"alice"}}]`),
	}}
	s, err := NewCLIClient("ya", r).ReadPRSnapshot(context.Background(), 42)
	if err != nil {
		t.Fatal(err)
	}
	if s.HeadCommit != "abc" || s.CommonStatus != "waiting_for_changes" || len(s.Comments) != 2 || s.Comments[1].ReplyToID != 11 {
		t.Fatalf("snapshot %#v", s)
	}
	if !strings.Contains(strings.Join(r.calls[1], " "), "author(alice)") || strings.Contains(strings.Join(r.calls[1], " "), "open(true)") {
		t.Fatalf("search must retain closed PRs: %v", r.calls[1])
	}
	if !strings.Contains(strings.Join(r.calls[2], " "), "--with-dropped") {
		t.Fatal("dropped comments omitted")
	}
}
func TestReadPRSnapshotRejectsUnknownPublicationAndMissingComments(t *testing.T) {
	for _, comments := range []string{`null`, `{}`} {
		r := &snapshotRunner{outputs: [][]byte{[]byte(`{"id":42,"status":"open","author":{"name":"alice"}}`), []byte(`{"review_requests":[{"id":42,"common_status":"waiting_for_review","active_diff_set":{"id":9,"arc_branch_heads":{"from_id":"draft"}},"diff_sets":[{"id":9,"status":"draft"}]}]}`), []byte(comments)}}
		if _, err := NewCLIClient("ya", r).ReadPRSnapshot(context.Background(), 42); err == nil {
			t.Fatal("unknown data accepted")
		}
	}
}
func TestReadPRSnapshotDoesNotTurnReadErrorIntoEmptyIssues(t *testing.T) {
	if _, err := NewCLIClient("ya", &snapshotRunner{err: errors.New("404")}).ReadPRSnapshot(context.Background(), 42); err == nil {
		t.Fatal("read error ignored")
	}
}

func TestMergedSnapshotNeedsOnlyAuthoritativeMetadata(t *testing.T) {
	r := &snapshotRunner{outputs: [][]byte{[]byte(`{"id":42,"status":"merged"}`)}}
	snapshot, err := NewCLIClient("ya", r).ReadPRSnapshot(context.Background(), 42)
	if err != nil || snapshot.Status != "merged" || len(r.calls) != 1 {
		t.Fatalf("snapshot: %#v calls=%v err=%v", snapshot, r.calls, err)
	}
	wrong := &snapshotRunner{outputs: [][]byte{[]byte(`{"id":43,"status":"merged"}`)}}
	if _, err := NewCLIClient("ya", wrong).ReadPRSnapshot(context.Background(), 42); err == nil {
		t.Fatal("wrong PR identity accepted")
	}
}
