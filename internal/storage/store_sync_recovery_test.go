package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"reflect"
	"testing"
)

func TestSyncRecoveryPreservesPresentationAndExpiresOnlySyncCallbacks(t *testing.T) {
	for _, stateName := range []string{model.SyncStateActive, model.SyncStateActivating, model.SyncStateDraining, model.SyncStateOff} {
		t.Run(stateName, func(t *testing.T) {
			store := openTestStore(t)
			ctx := context.Background()
			if err := store.BeginSyncActivation(ctx, "session", -1001); err != nil {
				t.Fatal(err)
			}
			topic := model.SyncTopic{SessionID: "session", ChatID: -1001, TopicID: 11, ThreadID: "thread", TelegramState: model.SyncTopicConnected,
				StatusMessageID: 90, StatusTurnID: "turn", LastRenderFP: "render", LastUserFP: "user", LastFinalFP: "final", PendingTelegramUserFP: "pending-user", PendingTelegramTurnID: "turn"}
			if err := store.UpsertSyncTopic(ctx, topic); err != nil {
				t.Fatal(err)
			}
			if err := store.FinishSyncActivation(ctx, "session", "summary", true); err != nil {
				t.Fatal(err)
			}
			for _, id := range []int64{12, 13} {
				if err := store.CreateSyncTopicDraft(ctx, model.SyncTopicDraft{SessionID: "session", ChatID: -1001, TopicID: id, CWD: "/tmp/project", Title: "draft"}); err != nil {
					t.Fatal(err)
				}
			}
			_, _, _, err := store.ClaimSyncTopicDraftMessage(ctx, -1001, 13, 502)
			if err != nil {
				t.Fatal(err)
			}
			receipt, _, err := store.AcceptSyncMessage(ctx, -1001, 11, 501)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.MarkSyncStarting(ctx, receipt, 7); err != nil {
				t.Fatal(err)
			}
			if err := store.MarkSyncDispatchState(ctx, receipt, model.SyncReceiptDispatched, "turn", model.SyncTurnActive, 7); err != nil {
				t.Fatal(err)
			}
			// Add an accepted receipt beside a dispatched receipt: only the former becomes unknown.
			if _, _, err := store.AcceptSyncMessage(ctx, -1001, 11, 503); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, `UPDATE sync_state SET state=? WHERE id=1`, stateName); err != nil {
				t.Fatal(err)
			}
			for _, action := range []string{"sync_approval", "sync_user_input", "sync_new_project", "external_start"} {
				if err := store.PutCallbackRoute(ctx, model.CallbackRoute{Token: action, Action: action, Status: model.CallbackStatusActive, PayloadJSON: "{}", CreatedAt: model.NowString()}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := store.GetSyncState(ctx)
			saved, _ := store.GetActiveSyncTopicByThread(ctx, "session", "thread")
			// Off topics are still inspected through the session list.
			if saved == nil {
				topics, _ := store.ListSyncTopics(ctx, "session")
				saved = &topics[0]
			}
			if err := store.RecoverSyncWriterState(ctx); err != nil {
				t.Fatal(err)
			}
			after, _ := store.GetSyncState(ctx)
			expected := before
			if stateName == model.SyncStateActivating {
				expected.State = model.SyncStateActive
			}
			if !reflect.DeepEqual(after, expected) {
				t.Fatalf("state changed: before=%#v after=%#v", expected, after)
			}
			topics, _ := store.ListSyncTopics(ctx, "session")
			got := topics[0]
			want := *saved
			want.ActiveTurnState = model.SyncTurnUnknown
			want.WriterGeneration = 0
			want.UpdatedAt = got.UpdatedAt
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("presentation changed: got=%#v want=%#v", got, want)
			}
			drafts, _ := store.ListSyncTopicDrafts(ctx, "session")
			if len(drafts) != 2 || drafts[0].State != model.SyncDraftReady || drafts[1].State != model.SyncDraftUnknown {
				t.Fatalf("drafts=%#v", drafts)
			}
			accepted, _ := store.GetSyncReceipt(ctx, 11, 503)
			dispatched, _ := store.GetSyncReceipt(ctx, 11, 501)
			if accepted.State != model.SyncReceiptUnknown || dispatched.State != model.SyncReceiptDispatched {
				t.Fatalf("receipts=%#v %#v", accepted, dispatched)
			}
			for _, action := range []string{"sync_approval", "sync_user_input", "sync_new_project", "external_start"} {
				route, _ := store.GetCallbackRoute(ctx, action)
				want := model.CallbackStatusExpired
				if action == "external_start" {
					want = model.CallbackStatusActive
				}
				if route.Status != want {
					t.Fatalf("route %s=%s", action, route.Status)
				}
			}
			if err := store.RecoverSyncWriterState(ctx); err != nil {
				t.Fatal(err)
			}
			again, _ := store.GetSyncState(ctx)
			if !reflect.DeepEqual(again, after) {
				t.Fatal("repeated recovery changed state")
			}
		})
	}
}

func TestSyncRecoveryMissingDatabaseStaysOff(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.RecoverSyncWriterState(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := store.GetSyncState(ctx)
	if state.State != model.SyncStateOff || state.SessionID != "" {
		t.Fatalf("state=%#v", state)
	}
}
