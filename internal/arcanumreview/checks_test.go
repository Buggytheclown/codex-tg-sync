package arcanumreview

import (
	"context"
	"reflect"
	"testing"
)

func TestReadMergeChecksPaginatesAndFiltersRequiredFailures(t *testing.T) {
	r := &snapshotRunner{outputs: [][]byte{
		[]byte(`{"checks":[{"key":{"system":"arcanum","type":"approved"},"required":true,"status":"failure"},{"key":{"system":"arcanum","type":"comment_issues_closed"},"required":true,"status":"failure"},{"key":{"system":"ci","type":"tests"},"required":true,"status":"failure","description":"Tests failed","uri":"https://example.com/run"}],"next_page_cursor":"opaque"}`),
		[]byte(`{"checks":[{"key":{"system":"CI","type":"optional"},"required":false,"status":"failure"},{"key":{"system":"ci","type":"build"},"required":true,"status":"pending"},{"key":{"system":"ci","type":"configure"},"required":true,"status":"unknown"}]}`),
	}}
	checks, err := NewCLIClient("ya", r).ReadPRChecks(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}
	failed := FailedMergeChecks(checks)
	if len(failed) != 1 || failed[0].Key.Type != "tests" || failed[0].URI != "https://example.com/run" {
		t.Fatalf("failed: %#v", failed)
	}
	want := []string{"tool", "gena-arcanum-cli", "--json", "diff", "checks", "9", "--limit", "50", "--cursor", "opaque"}
	if len(r.calls) != 2 || !reflect.DeepEqual(r.calls[1], want) {
		t.Fatalf("calls: %v", r.calls)
	}
}
func TestReadMergeChecksRejectsIncompleteAndFailedPages(t *testing.T) {
	for _, last := range []string{`null`, `{}`, `{"checks":null}`, `{"checks":[{"required":true,"status":"failure"}]}`, `{"checks":[],"next_page_cursor":"again"}`} {
		r := &snapshotRunner{outputs: [][]byte{[]byte(`{"checks":[],"next_page_cursor":"again"}`), []byte(last)}}
		if _, err := NewCLIClient("ya", r).ReadPRChecks(context.Background(), 9); err == nil {
			t.Fatalf("accepted %s", last)
		}
	}
	r := &snapshotRunner{outputs: [][]byte{[]byte(`{"checks":[]}`)}}
	if _, err := NewCLIClient("ya", r).ReadPRChecks(context.Background(), 0); err == nil || len(r.calls) != 0 {
		t.Fatal("invalid diff reached API")
	}
}
func TestListAuthoredOpenDoesNotRequireRequestChanges(t *testing.T) {
	r := &snapshotRunner{outputs: [][]byte{[]byte(`{"review_requests":[{"id":42,"author":{"name":"alice"},"summary":"Change"}]}`)}}
	prs, err := NewCLIClient("ya", r).ListAuthoredOpen(context.Background(), "alice")
	if err != nil || len(prs) != 1 {
		t.Fatalf("%v %v", prs, err)
	}
	if r.calls[0][6] != "open(true);published(true);author(alice)" {
		t.Fatal(r.calls)
	}
}

func TestMergeFailurePredicateAndOrder(t *testing.T) {
	checks := []PRCheck{}
	for _, status := range []string{"pending", "unknown", "success", "skipped", "failure"} {
		checks = append(checks, PRCheck{Key: CheckKey{System: "ci", Type: status}, Required: true, Status: status})
	}
	checks = append(checks, PRCheck{Key: CheckKey{System: "CI", Type: "approved"}, Required: true, Status: "failure"})
	failed := FailedMergeChecks(checks)
	if len(failed) != 2 || failed[0].Key.System != "CI" || failed[1].Key.Type != "failure" {
		t.Fatal(failed)
	}
}
