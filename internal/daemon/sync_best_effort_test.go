package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestSyncOffRetainsWriterUntilAuthoritativeTerminal(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{}
	s.liveFactory = func() Session { return writer }
	s.SetSyncForum(&fakeSyncForum{})
	if _, err := s.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "work", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.deactivateSync(ctx); err != nil {
		t.Fatal(err)
	}
	if len(writer.turnInterruptCalls) != 0 || s.syncWriter.Snapshot().Active != 1 {
		t.Fatal("off interrupted or discarded writer")
	}
	topics, err := s.store.ListDetachedSyncWork(ctx, -1001)
	if err != nil || len(topics) != 1 {
		t.Fatal(topics, err)
	}
	if topics[0].TelegramState != "deleted" || topics[0].ActiveTurnID != "started-turn" {
		t.Fatal(topics)
	}
	writer.threadReads = map[string]map[string]any{"thread-1": syncCompletedPayload("thread-1", "started-turn", "finished")}
	s.reconcileSync(ctx)
	if s.syncWriter.Snapshot().Active != 0 {
		t.Fatal("detached terminal did not release writer", s.syncWriter.Snapshot())
	}
}

func TestSyncTopicTitleExactlyMatchesWireTrimming(t *testing.T) {
	got := syncTopicTitle(model.Thread{ID: "t", Title: strings.Repeat("a", 127) + " " + "suffix"})
	if got != strings.TrimSpace(got) {
		t.Fatalf("stored title differs from Telegram request: %q", got)
	}
}

func TestDetachedUnknownDispatchDoesNotUsePreviousTerminalAsCompletion(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncCompletedPayload("thread-1", "previous-turn", "previous result")}}
	s.liveFactory = func() Session { return writer }
	s.SetSyncForum(&fakeSyncForum{})
	lease, err := s.syncWriter.Reserve(ctx, "thread-1")
	if err != nil {
		t.Fatal(err)
	}
	s.syncLeases["thread-1"] = lease
	receipt, _, err := s.store.AcceptSyncMessage(ctx, -1001, 11, 501)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.store.MarkSyncStarting(ctx, receipt, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if err = s.syncWriter.MarkUnknown(lease); err != nil {
		t.Fatal(err)
	}
	if err = s.store.MarkSyncDispatchFailure(ctx, receipt, model.SyncReceiptUnknown, model.SyncTurnUnknown, lease.Generation); err != nil {
		t.Fatal(err)
	}
	if _, err = s.deactivateSync(ctx); err != nil {
		t.Fatal(err)
	}
	s.reconcileSync(ctx)
	if s.syncWriter.Snapshot().Unknown != 1 {
		t.Fatal("old terminal closed unresolved writer", s.syncWriter.Snapshot())
	}
}

func TestExternalTopicWaitReleasesBothLocksInOrder(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.WithValue(context.Background(), syncExternalRequestKey{}, "request")
	ctx = context.WithValue(ctx, syncCreationLockKey{}, true)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		s.syncMu.Lock()
		s.taskLaunchMu.Lock()
		_, _ = s.createTopicIO(ctx, "s", func(context.Context) (int64, error) { close(entered); <-release; return 20, nil })
		s.taskLaunchMu.Unlock()
		s.syncMu.Unlock()
		close(done)
	}()
	<-entered
	acquired := make(chan struct{})
	go func() {
		s.syncMu.Lock()
		s.taskLaunchMu.Lock()
		s.taskLaunchMu.Unlock()
		s.syncMu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("external IO retained a launch lock")
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("external IO could not reacquire locks")
	}
}

func TestDetachedKnownTurnDoesNotCompleteFromOlderTerminal(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	writer := &stubSession{threadReads: map[string]map[string]any{"thread-1": syncCompletedPayload("thread-1", "previous-turn", "previous result")}}
	s.liveFactory = func() Session { return writer }
	s.SetSyncForum(&fakeSyncForum{})
	if _, err := s.HandleMessageWithID(ctx, -1001, 11, 501, 123456789, "work", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.deactivateSync(ctx); err != nil {
		t.Fatal(err)
	}
	s.reconcileSync(ctx)
	if s.syncWriter.Snapshot().Active != 1 {
		t.Fatal("older terminal released a different known turn", s.syncWriter.Snapshot())
	}
}
