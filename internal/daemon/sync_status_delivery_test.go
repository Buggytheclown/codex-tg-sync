package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tgformat"
)

func TestSyncStatusDeliveryIgnoresMetadataAndRetriesProgress(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	current := appserver.SnapshotFromThreadRead(syncRunningPayloadWithCommentary("thread-1", "turn-1", "Working"))
	current.LatestTurnStartedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	current.ToolCallCounts = []int{0, 1}
	topic, err := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil {
		t.Fatalf("topic=%#v err=%v", topic, err)
	}
	forum := &fakeSyncForum{}
	delivered, ok := service.deliverSyncStatus(ctx, forum, *topic, current, now)
	if !ok {
		t.Fatal("initial Status failed")
	}
	for range 3 {
		now = now.Add(time.Minute)
		current.ToolCallCounts[1]++
		current.LatestTurnUpdatedAt = now.Format(time.RFC3339Nano)
		delivered, ok = service.deliverSyncStatus(ctx, forum, delivered, current, now)
		if !ok || len(forum.edits) != 0 {
			t.Fatalf("metadata caused Status edit: %#v", forum.edits)
		}
	}
	confirmed := delivered.LastRenderFP
	for i := range current.DetailItems {
		if current.DetailItems[i].Kind == model.DetailItemCommentary {
			current.DetailItems[i].Text = "New progress"
		}
	}
	forum.editErr = errors.New("temporary Telegram error")
	failed, ok := service.deliverSyncStatus(ctx, forum, delivered, current, now)
	if ok || failed.LastRenderFP != confirmed {
		t.Fatal("failed edit acknowledged new progress")
	}
	if err := service.store.UpsertSnapshot(ctx, current.Thread.ID, appserver.CompactSnapshot(nil, current, now)); err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(service.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	service = reloaded
	service.SetSyncForum(forum)
	forum.editErr = nil
	// No new event: a delivery pass can retry the latest persisted progress.
	service.runSyncDelivery(ctx, current.Thread.ID)
	topic, err = service.store.GetActiveSyncTopic(ctx, -1001, 11)
	if err != nil || topic == nil || topic.LastRenderFP == confirmed {
		t.Fatalf("progress stayed pending: %#v %v", topic, err)
	}
	if len(forum.sends) != 1 || len(forum.edits) != 2 || !strings.Contains(forum.edits[1].text, "New progress") || !strings.Contains(forum.edits[1].text, "tools 4") {
		t.Fatalf("retry lost progress, accumulated tools or anchor: sends=%#v edits=%#v", forum.sends, forum.edits)
	}
	service.runSyncDelivery(ctx, current.Thread.ID)
	if len(forum.edits) != 2 {
		t.Fatal("restart/repeated delivery duplicated Status")
	}
}

func TestSyncStatusDeliveryObservesStateAndPlanChanges(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	now := time.Now()
	current := appserver.SnapshotFromThreadRead(syncRunningPayloadWithCommentary("thread-1", "turn-1", "Working"))
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	forum := &fakeSyncForum{}
	delivered, ok := service.deliverSyncStatus(ctx, forum, *topic, current, now)
	if !ok {
		t.Fatal("initial Status failed")
	}
	for _, mutate := range []func(){
		func() { current.WaitingOnApproval = true },
		func() { current.WaitingOnApproval = false },
		func() {
			current.DetailItems = append(current.DetailItems, model.DetailItem{Kind: model.DetailItemPlan, Text: "New plan", CommentaryIndex: 2})
		},
		func() { current.LatestTurnStatus = "failed"; current.Thread.Status = "failed" },
	} {
		mutate()
		count := len(forum.edits)
		delivered, ok = service.deliverSyncStatus(ctx, forum, delivered, current, now)
		if !ok || len(forum.edits) != count+1 {
			t.Fatalf("state/plan change was suppressed: %#v", forum.edits)
		}
	}
}

func TestSyncStatusDeliveryKeepsLegacyTerminalFingerprint(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	now := time.Now()
	current := appserver.SnapshotFromThreadRead(syncCompletedPayload("thread-1", "turn-1", "done"))
	current.LatestTurnStartedAt = now.Add(-time.Minute).Format(time.RFC3339Nano)
	current.LatestTurnUpdatedAt = now.Format(time.RFC3339Nano)
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	topic.StatusMessageID, topic.StatusTurnID = 90, current.LatestTurnID
	topic.LastRenderFP = syncFingerprint(tgformat.HashRendered(renderSyncStatusAt(current, now)))
	topic.LastFinalFP = current.LatestFinalFP
	if err := service.store.UpsertSyncTopic(ctx, *topic); err != nil {
		t.Fatal(err)
	}
	previous := appserver.CompactSnapshot(nil, current, now)
	if !syncStoredTerminalPresentationDelivered(&previous, *topic) {
		t.Fatal("legacy completed Status no longer considered delivered")
	}
	forum := &fakeSyncForum{}
	if _, ok := service.deliverSyncStatus(ctx, forum, *topic, current, now.Add(time.Hour)); !ok || len(forum.edits)+len(forum.sends) != 0 {
		t.Fatal("upgrade refreshed an unchanged completed Status")
	}
}

func TestSyncStatusDeliveryUpgradesActiveFingerprintOnce(t *testing.T) {
	service := activeSyncService(t)
	ctx := context.Background()
	current := appserver.SnapshotFromThreadRead(syncRunningPayloadWithCommentary("thread-1", "turn-1", "Working"))
	topic, _ := service.store.GetActiveSyncTopic(ctx, -1001, 11)
	topic.StatusMessageID, topic.StatusTurnID = 90, current.LatestTurnID
	topic.LastRenderFP = "legacy-full-render-hash"
	forum := &fakeSyncForum{}
	delivered, ok := service.deliverSyncStatus(ctx, forum, *topic, current, time.Now())
	if !ok || len(forum.edits) != 1 || !strings.HasPrefix(delivered.LastRenderFP, "content-v1:") {
		t.Fatalf("active card did not upgrade in place: %#v", delivered)
	}
	if _, ok := service.deliverSyncStatus(ctx, forum, delivered, current, time.Now().Add(time.Hour)); !ok || len(forum.edits) != 1 {
		t.Fatal("active card repeatedly upgraded on unchanged content")
	}
}
