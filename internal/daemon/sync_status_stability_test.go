package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestSyncDetailMergePreservesAuthoritativeOrderAndMissingItems(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name              string
		previous, current []string
		want              []string
	}{
		{"restored prefix", []string{"c", "d"}, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"}},
		{"corrupt order", []string{"c", "a", "d", "b"}, []string{"a", "b", "c", "d"}, []string{"a", "b", "c", "d"}},
		{"missing prefix", []string{"a", "b", "c"}, []string{"b", "c", "d"}, []string{"a", "b", "c", "d"}},
		{"missing interior", []string{"a", "b", "c"}, []string{"a", "c", "d"}, []string{"a", "b", "c", "d"}},
		{"missing suffix", []string{"a", "b", "c"}, []string{"a", "b", "d"}, []string{"a", "b", "c", "d"}},
		{"empty read", []string{"a", "b"}, nil, []string{"a", "b"}},
		{"disjoint read", []string{"a", "b"}, []string{"c", "d"}, []string{"a", "b", "c", "d"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			items := func(ids []string) []model.DetailItem {
				var out []model.DetailItem
				for _, id := range ids {
					out = append(out, model.DetailItem{ID: id, Kind: model.DetailItemCommentary, Text: "Same commentary"})
				}
				return out
			}
			previous, current := items(test.previous), items(test.current)
			merged := mergeSyncDetailItems(previous, current)
			var got []string
			for _, item := range merged {
				got = append(got, item.ID)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("merged IDs = %v, want %v", got, test.want)
			}
			if twice := mergeSyncDetailItems(merged, current); !reflect.DeepEqual(twice, merged) {
				t.Fatalf("second merge changed the projection: %v", twice)
			}
		})
	}
}

func TestSyncDetailMergeUpdatesIDLessBlockWithoutResettingTiming(t *testing.T) {
	t.Parallel()
	start := model.TimeString("2026-10-02T12:00:00Z")
	previous := []model.DetailItem{
		{ID: "tool", Kind: model.DetailItemTool},
		{Kind: model.DetailItemCommentary, CommentaryIndex: 2, Text: "Draft", StartedAt: start},
	}
	current := []model.DetailItem{{Kind: model.DetailItemCommentary, CommentaryIndex: 2, Text: "Updated"}}
	merged := mergeSyncDetailItems(previous, current)
	if len(merged) != 2 || merged[1].Text != "Updated" || merged[1].StartedAt != start {
		t.Fatalf("ID-less block was duplicated or retimed: %#v", merged)
	}
}

func TestSyncLongStatusSurvivesRepeatedSnapshots(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	now := start.Add(80 * time.Second)
	service.now = func() time.Time { return now }
	topic := model.SyncTopic{SessionID: "s", ChatID: -1001, TopicID: 11, ThreadID: "thread-1"}
	forum := &fakeSyncForum{}
	read := func(blocks int) appserver.ThreadReadSnapshot {
		var items []any
		for block := 1; block <= blocks; block++ {
			items = append(items, map[string]any{"id": fmt.Sprintf("block-%d", block), "type": "agentMessage", "phase": "commentary", "text": fmt.Sprintf("Commentary %d", block)})
			for tool := range 12 {
				items = append(items, map[string]any{"id": fmt.Sprintf("tool-%d-%d", block, tool), "type": "commandExecution", "command": "hidden command", "status": "completed"})
			}
		}
		snapshot := appserver.SnapshotFromThreadRead(map[string]any{"id": topic.ThreadID, "status": "inProgress", "turns": []any{map[string]any{"id": "turn-1", "status": "inProgress", "items": items}}})
		snapshot.LatestTurnStartedAt = start.Format(time.RFC3339Nano)
		return snapshot
	}
	first, _ := service.persistSyncSnapshot(ctx, topic, read(8))
	firstBlocks := syncStatusBlocks(first.DetailItems)
	if len(firstBlocks) != 8 {
		t.Fatalf("retained %d commentary blocks, want 8", len(firstBlocks))
	}
	var delivered bool
	topic, delivered = service.deliverSyncStatus(ctx, forum, topic, first, now, syncStatusFreshness{})
	if !delivered {
		t.Fatal("initial Status delivery failed")
	}
	anchor := topic.StatusMessageID
	topic, _ = service.deliverSyncStatus(ctx, forum, topic, first, now.Add(time.Second), syncStatusFreshness{})
	if len(forum.edits) != 0 {
		t.Fatal("unchanged content within a timer bucket caused another edit")
	}
	for poll := range 5 {
		now = now.Add(10 * time.Second)
		observed, _ := service.persistSyncSnapshot(ctx, topic, read(8))
		if blocks := syncStatusBlocks(observed.DetailItems); !reflect.DeepEqual(blocks, firstBlocks) {
			t.Fatalf("poll %d changed block order, identity, or timing: %#v", poll, blocks)
		}
		message := renderSyncStatusAt(observed, now)
		for index := 1; index <= 8; index++ {
			if !strings.Contains(message.Text, fmt.Sprintf("tools 12\nCommentary %d", index)) {
				t.Fatalf("poll %d lost commentary or tool count %d: %q", poll, index, message.Text)
			}
		}
		if strings.Contains(message.Text, "hidden command") {
			t.Fatal("tool labels leaked into Status")
		}
		topic, delivered = service.deliverSyncStatus(ctx, forum, topic, observed, now, syncStatusFreshness{})
		if !delivered || topic.StatusMessageID != anchor || len(forum.sends) != 1 {
			t.Fatal("poll replaced the existing Status anchor")
		}
	}
	now = now.Add(10 * time.Second)
	appended, _ := service.persistSyncSnapshot(ctx, topic, read(9))
	if blocks := syncStatusBlocks(appended.DetailItems); len(blocks) != 9 || !reflect.DeepEqual(blocks[:8], firstBlocks) {
		t.Fatalf("append changed previous status blocks: %#v", blocks)
	}
	updated := read(9)
	updated.DetailItems[0].Text = "Updated commentary"
	now = now.Add(10 * time.Second)
	observed, _ := service.persistSyncSnapshot(ctx, topic, updated)
	if block := syncStatusBlocks(observed.DetailItems)[0]; block.Text != "Updated commentary" || block.StartedAt != firstBlocks[0].StartedAt {
		t.Fatalf("same-ID update reset timing: %#v", block)
	}
	terminal := read(9)
	terminal.LatestTurnStatus = "completed"
	terminal.Thread.Status = "completed"
	terminal.LatestTurnUpdatedAt = now.Format(time.RFC3339Nano)
	finished, _ := service.persistSyncSnapshot(ctx, topic, terminal)
	finishedText := renderSyncStatusAt(finished, now).Text
	now = now.Add(time.Hour)
	restarted, _ := service.persistSyncSnapshot(ctx, topic, terminal)
	if renderSyncStatusAt(restarted, now).Text != finishedText {
		t.Fatal("terminal status changed after persisted snapshot reload")
	}
	newTurn := read(1)
	newTurn.LatestTurnID = "turn-2"
	newTurn.LatestTurnStartedAt = now.Format(time.RFC3339Nano)
	fresh, _ := service.persistSyncSnapshot(ctx, topic, newTurn)
	if blocks := syncStatusBlocks(fresh.DetailItems); len(blocks) != 1 || blocks[0].StartedAt != model.TimeString(newTurn.LatestTurnStartedAt) {
		t.Fatalf("new turn inherited old blocks or timing: %#v", blocks)
	}
}

func TestSyncStatusRestoredPrefixKeepsHealthyTimingAnchors(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	current := appserver.ThreadReadSnapshot{LatestTurnID: "turn-1", LatestTurnStatus: "inProgress", LatestTurnStartedAt: start.Format(time.RFC3339Nano)}
	for index := 1; index <= 3; index++ {
		current.DetailItems = append(current.DetailItems, model.DetailItem{ID: fmt.Sprintf("block-%d", index), Kind: model.DetailItemCommentary, CommentaryIndex: index, Text: "Working"})
	}
	prior := current
	prior.DetailItems = append([]model.DetailItem(nil), current.DetailItems[1:]...)
	for index := range prior.DetailItems {
		prior.DetailItems[index].StartedAt = model.TimeString(start.Add(time.Duration(index+1) * 10 * time.Second).Format(time.RFC3339Nano))
	}
	raw, _ := json.Marshal(prior)
	previous := model.ThreadSnapshotState{CompactJSON: raw, LastPollAt: model.TimeString(start.Add(30 * time.Second).Format(time.RFC3339Nano))}
	restored := appserver.CompactSnapshot(&previous, monotonicSyncSnapshot(&previous, current), start.Add(40*time.Second))
	var observed appserver.ThreadReadSnapshot
	if err := json.Unmarshal(restored.CompactJSON, &observed); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(observed.DetailItems[1:], prior.DetailItems) {
		t.Fatal("restoring the prefix changed healthy known starts")
	}
	if first := parseTime(observed.DetailItems[0].StartedAt); !first.Before(parseTime(prior.DetailItems[0].StartedAt)) {
		t.Fatal("restored prefix was timed after its anchor")
	}
}

func TestSyncStatusRepairsCorruptedPersistedBlockTiming(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct{ invertedIndex, terminal bool }{{true, false}, {false, false}, {true, true}, {false, true}} {
		t.Run(fmt.Sprintf("inverted-index-%t-terminal-%t", test.invertedIndex, test.terminal), func(t *testing.T) {
			current := appserver.ThreadReadSnapshot{Thread: model.Thread{ID: "thread-1"}, LatestTurnID: "turn-1", LatestTurnStatus: "inProgress", LatestTurnStartedAt: start.Format(time.RFC3339Nano)}
			if test.terminal {
				current.LatestTurnStatus = "completed"
				current.LatestTurnUpdatedAt = start.Add(9 * time.Minute).Format(time.RFC3339Nano)
			}
			for index := 1; index <= 3; index++ {
				current.DetailItems = append(current.DetailItems, model.DetailItem{ID: fmt.Sprintf("block-%d", index), Kind: model.DetailItemCommentary, CommentaryIndex: index, Text: "Working"})
			}
			corrupt := current
			corrupt.DetailItems = append([]model.DetailItem(nil), current.DetailItems...)
			if test.invertedIndex {
				corrupt.DetailItems[0], corrupt.DetailItems[1] = corrupt.DetailItems[1], corrupt.DetailItems[0]
			}
			for index := range corrupt.DetailItems {
				minutes := index + 5
				if !test.invertedIndex {
					minutes = 7 - index
				}
				corrupt.DetailItems[index].StartedAt = model.TimeString(start.Add(time.Duration(minutes) * time.Minute).Format(time.RFC3339Nano))
			}
			raw, _ := json.Marshal(corrupt)
			previous := model.ThreadSnapshotState{CompactJSON: raw, LastPollAt: model.TimeString(start.Add(8 * time.Minute).Format(time.RFC3339Nano))}
			merged := monotonicSyncSnapshot(&previous, current)
			repaired := appserver.CompactSnapshot(&previous, merged, start.Add(9*time.Minute))
			var observed appserver.ThreadReadSnapshot
			if err := json.Unmarshal(repaired.CompactJSON, &observed); err != nil {
				t.Fatal(err)
			}
			for index, block := range observed.DetailItems {
				if block.ID != current.DetailItems[index].ID || block.StartedAt != model.TimeString(start.Add(time.Duration(index)*3*time.Minute).Format(time.RFC3339Nano)) {
					t.Fatalf("corrupted block did not recover: %#v", observed.DetailItems)
				}
			}
			repeated := appserver.CompactSnapshot(&repaired, monotonicSyncSnapshot(&repaired, current), start.Add(10*time.Minute))
			var again appserver.ThreadReadSnapshot
			_ = json.Unmarshal(repeated.CompactJSON, &again)
			if !reflect.DeepEqual(again.DetailItems, observed.DetailItems) {
				t.Fatal("recovered block timing changed on the next poll")
			}
		})
	}
}

func TestSyncCorruptStatusDefersTimingRecoveryUntilOrderIsRestored(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	current := appserver.ThreadReadSnapshot{LatestTurnID: "turn-1", LatestTurnStatus: "inProgress", LatestTurnStartedAt: start.Format(time.RFC3339Nano)}
	for index := 1; index <= 3; index++ {
		current.DetailItems = append(current.DetailItems, model.DetailItem{ID: fmt.Sprintf("block-%d", index), Kind: model.DetailItemCommentary, CommentaryIndex: index, Text: "Working"})
	}
	corrupt := current
	corrupt.DetailItems = []model.DetailItem{current.DetailItems[2], current.DetailItems[0], current.DetailItems[1]}
	for index := range corrupt.DetailItems {
		corrupt.DetailItems[index].StartedAt = model.TimeString(start.Add(time.Duration(index) * 10 * time.Second).Format(time.RFC3339Nano))
	}
	raw, _ := json.Marshal(corrupt)
	previous := model.ThreadSnapshotState{CompactJSON: raw, LastPollAt: model.TimeString(start.Add(40 * time.Second).Format(time.RFC3339Nano))}
	partial := current
	partial.DetailItems = current.DetailItems[:2]
	for poll := range 3 {
		state := appserver.CompactSnapshot(&previous, monotonicSyncSnapshot(&previous, partial), start.Add(time.Duration(50+poll*10)*time.Second))
		var observed appserver.ThreadReadSnapshot
		if err := json.Unmarshal(state.CompactJSON, &observed); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(observed.DetailItems, corrupt.DetailItems) {
			t.Fatal("incomplete read repeatedly rebuilt unresolved corrupt timing")
		}
		previous = state
	}
	repaired := appserver.CompactSnapshot(&previous, monotonicSyncSnapshot(&previous, current), start.Add(90*time.Second))
	var observed appserver.ThreadReadSnapshot
	if err := json.Unmarshal(repaired.CompactJSON, &observed); err != nil {
		t.Fatal(err)
	}
	for index, block := range observed.DetailItems {
		if block.ID != current.DetailItems[index].ID || block.StartedAt != model.TimeString(start.Add(time.Duration(index)*30*time.Second).Format(time.RFC3339Nano)) {
			t.Fatal("full read did not recover deferred corruption")
		}
	}
}

func TestSyncStatusKeepsGlobalBlockNumberAndUTF16Limit(t *testing.T) {
	t.Parallel()
	start := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	snapshot := appserver.ThreadReadSnapshot{LatestTurnID: "turn-1", LatestTurnStatus: "completed", LatestTurnStartedAt: start.Format(time.RFC3339Nano), LatestTurnUpdatedAt: start.Add(time.Minute).Format(time.RFC3339Nano), DetailItems: []model.DetailItem{
		{ID: "block-7", Kind: model.DetailItemCommentary, CommentaryIndex: 7, Text: "Older block"},
		{ID: "block-8", Kind: model.DetailItemCommentary, CommentaryIndex: 8, Text: strings.Repeat("😀 newest line\n", 500) + "TAIL"},
	}}
	short := snapshot
	short.DetailItems = snapshot.DetailItems[:1]
	if message := renderSyncStatusAt(short, start); !strings.Contains(message.Text, "Блок 7 ·") {
		t.Fatalf("block was renumbered: %q", message.Text)
	}
	message := renderSyncStatusAt(snapshot, start)
	if syncUTF16Len(message.Text) > 4096 || !strings.HasSuffix(message.Text, "TAIL") || len(message.Entities) != 1 {
		t.Fatalf("overflow status invalid: length=%d entities=%v", syncUTF16Len(message.Text), message.Entities)
	}
	entity := message.Entities[0]
	if entity.Offset+entity.Length != syncUTF16Len(message.Text) {
		t.Fatal("terminal entity exceeds the retained UTF-16 body")
	}
}
