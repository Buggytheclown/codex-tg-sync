package arcanumreview

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type pageRunner struct {
	calls [][]string
	pages []string
}

func (r *pageRunner) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	if len(r.pages) == 0 {
		return nil, fmt.Errorf("unexpected command")
	}
	p := r.pages[0]
	r.pages = r.pages[1:]
	return []byte(p), nil
}
func TestSearchExplicitPagesAndLifecycle(t *testing.T) {
	r := &pageRunner{pages: []string{
		`{"review_requests":[{"id":1,"summary":"one","author":{"name":"alice"},"status":"published","full_status":"open","common_status":"waiting_for_review"}],"total_count":2,"has_next":false}`,
		`{"review_requests":[{"id":2,"summary":"two","author":{"name":"alice"},"status":"published","full_status":"merged","common_status":"merged"}],"total_count":2,"has_next":false}`,
	}}
	c := NewCLIClient("ya", r)
	result, err := c.Search(context.Background(), "author(alice)", 1, nil)
	if err != nil || !result.Complete || len(result.Items) != 2 {
		t.Fatalf("%+v %v", result, err)
	}
	if result.Items[0].Snapshot.Status != "open" || result.Items[1].Snapshot.Status != "merged" {
		t.Fatalf("lifecycle: %+v", result)
	}
	for _, args := range r.calls {
		if strings.Contains(strings.Join(args, " "), "--all") {
			t.Fatal(args)
		}
	}
}
func TestSearchKeepsPositiveRowsOnIncompletePage(t *testing.T) {
	r := &pageRunner{pages: []string{`{"review_requests":[{"id":1,"author":{"name":"alice"},"summary":"one","full_status":"open","common_status":"draft"}],"total_count":2}`, `{"review_requests":[],"total_count":2}`}}
	result, err := NewCLIClient("ya", r).Search(context.Background(), "author(alice)", 1, nil)
	if err == nil || result.Complete || len(result.Items) != 1 {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestFullPageFalseHasNextDoesNotTruncateDiscoveryWithoutTotal(t *testing.T) {
	r := &pageRunner{pages: []string{`{"review_requests":[{"id":1}],"has_next":false}`, `{"review_requests":[{"id":2}],"has_next":false}`, `{"review_requests":[],"has_next":false}`}}
	result, err := NewCLIClient("ya", r).Search(context.Background(), "author(alice)", 1, nil)
	if err != nil || !result.Complete || len(result.Items) != 2 || len(r.calls) != 3 {
		t.Fatalf("truncated discovery: %+v %v", result, err)
	}
}
