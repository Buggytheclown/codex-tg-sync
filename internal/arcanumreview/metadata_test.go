package arcanumreview

import (
	"context"
	"strings"
	"testing"
)

func TestReadPRMetadataIsPassiveAndRejectsIncompleteStatus(t *testing.T) {
	r := &snapshotRunner{outputs: [][]byte{[]byte(`{"id":42,"status":"open","author":{"name":"alice"}}`), []byte(`{"review_requests":[{"id":42,"common_status":"waiting_for_changes"}]}`)}}
	got, err := NewCLIClient("ya", r).ReadPRMetadata(context.Background(), 42)
	if err != nil || got.CommonStatus != "waiting_for_changes" || len(r.calls) != 2 {
		t.Fatalf("metadata=%#v calls=%v err=%v", got, r.calls, err)
	}
	for _, args := range r.calls {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "comments") || strings.Contains(joined, "checks") || strings.Contains(joined, "diff_sets") {
			t.Fatalf("expensive metadata read: %s", joined)
		}
	}
	bad := &snapshotRunner{outputs: [][]byte{[]byte(`{"id":42,"status":"open","author":{"name":"alice"}}`), []byte(`{"review_requests":[]}`)}}
	if _, err = NewCLIClient("ya", bad).ReadPRMetadata(context.Background(), 42); err == nil {
		t.Fatal("missing status accepted")
	}
	merged := &snapshotRunner{outputs: [][]byte{[]byte(`{"id":42,"status":"merged"}`)}}
	if got, err = NewCLIClient("ya", merged).ReadPRMetadata(context.Background(), 42); err != nil || got.Status != "merged" || len(merged.calls) != 1 {
		t.Fatalf("merged: %#v %v", got, err)
	}
}
