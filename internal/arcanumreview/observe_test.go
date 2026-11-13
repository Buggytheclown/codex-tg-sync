package arcanumreview

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReadObserverRecordsPhysicalCallsAndOpaqueResourceKeys(t *testing.T) {
	runner := &snapshotRunner{outputs: [][]byte{[]byte(`{"review_requests":[]}`), []byte(`{"review_requests":[]}`)}}
	client := NewCLIClient("ya", runner)
	events := []ReadEvent{}
	client.SetReadObserver(func(e ReadEvent) { events = append(events, e) })
	for _, offset := range []string{"0", "100"} {
		var value any
		if err := client.readJSON(context.Background(), &value, "pr", "search", "--query", "author(private-login)", "--offset", offset); err != nil {
			t.Fatal(err)
		}
	}
	if len(events) != 4 || events[0].Phase != "start" || events[1].Phase != "finish" || events[0].Method != "pr.search" || !events[1].Success || events[0].Key != events[2].Key {
		t.Fatalf("events %+v", events)
	}
	if strings.Contains(events[0].Key, "private-login") || events[0].StartedAt.IsZero() || events[1].Duration < 0 {
		t.Fatalf("unsafe/incomplete event %+v", events[0])
	}
	runner.err = errors.New("failure")
	var result any
	if err := client.readJSON(context.Background(), &result, "pr", "get", "42"); err == nil {
		t.Fatal("read error lost")
	}
	if events[len(events)-1].Success || events[len(events)-1].Method != "pr.get" {
		t.Fatal(events)
	}
}
