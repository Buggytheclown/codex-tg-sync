package storage

import (
	"context"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestAFCActivationIsIsolatedAndDisablesObserverOnlyOnSuccess(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.SetGlobalObserverTarget(ctx, 10, 0, true); err != nil {
		t.Fatal(err)
	}
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", Rank: 1, Title: "One", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{"created":1}`, true); err != nil {
		t.Fatal(err)
	}
	state, err := store.GetAFCState(ctx)
	if err != nil || state.State != model.AFCStateActive {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
	_, configured, err := store.GetGlobalObserverTarget(ctx)
	if err != nil || !configured {
		t.Fatalf("observer configured = %v, err = %v", configured, err)
	}
	enabled, _ := store.GetState(ctx, "observer.global_enabled")
	if enabled != "false" {
		t.Fatalf("observer.global_enabled = %q, want false", enabled)
	}
	bound, err := store.ListBoundThreadIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(bound) != 0 {
		t.Fatalf("legacy bound threads = %v, want AFC isolation", bound)
	}
}

func TestAFCOffIsLogicalBeforeCleanupAndDoesNotRestoreObserver(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.FinishAFCActivation(ctx, "session-1", `{}`, true); err != nil {
		t.Fatal(err)
	}
	topics, err := store.MarkAFCOff(ctx, "session-1")
	if err != nil || len(topics) != 1 || topics[0].TelegramState != model.AFCTopicCleanup {
		t.Fatalf("topics = %#v, err = %v", topics, err)
	}
	state, err := store.GetAFCState(ctx)
	if err != nil || state.State != model.AFCStateOff {
		t.Fatalf("state = %#v, err = %v", state, err)
	}
	enabled, _ := store.GetState(ctx, "observer.global_enabled")
	if enabled != "false" {
		t.Fatalf("observer.global_enabled = %q, want false", enabled)
	}
}

func TestRecoverAFCStateMakesInterruptedActivationCleanupOnly(t *testing.T) {
	t.Parallel()
	store := openTestStore(t)
	ctx := context.Background()
	if err := store.BeginAFCActivation(ctx, "session-1", -1001); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertAFCTopic(ctx, model.AFCTopic{SessionID: "session-1", ChatID: -1001, TopicID: 11, ThreadID: "thread-1", TelegramState: model.AFCTopicConnected}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverAFCState(ctx); err != nil {
		t.Fatal(err)
	}
	state, _ := store.GetAFCState(ctx)
	if state.State != model.AFCStateOff {
		t.Fatalf("state = %#v", state)
	}
	topics, _ := store.ListAFCTopics(ctx, "session-1")
	if len(topics) != 1 || topics[0].TelegramState != model.AFCTopicCleanup {
		t.Fatalf("topics = %#v", topics)
	}
}
