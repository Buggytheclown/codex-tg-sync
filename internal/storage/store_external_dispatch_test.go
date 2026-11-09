package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"path/filepath"
	"testing"
)

func TestExternalDispatchRecoveryPhases(t *testing.T) {
	for _, phase := range []string{model.ExternalDispatchNotSent, model.ExternalDispatchThreadSending, model.ExternalDispatchThreadReady, model.ExternalDispatchTurnSending, model.ExternalDispatchTurnConfirmed, ""} {
		t.Run(phase, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			r := testExternalLaunchRequest("test:phase")
			if _, err = s.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{r}); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.ClaimExternalLaunchRequest(ctx, r.ID); err != nil || !ok {
				t.Fatal(ok, err)
			}
			thread, turn := "", ""
			if phase == model.ExternalDispatchThreadReady || phase == model.ExternalDispatchTurnSending || phase == model.ExternalDispatchTurnConfirmed {
				thread = "thread"
			}
			if phase == model.ExternalDispatchTurnConfirmed {
				turn = "turn"
			}
			if _, err = s.db.Exec(`UPDATE external_launch_requests SET dispatch_phase=?,thread_id=?,turn_id=? WHERE id=?`, phase, thread, turn, r.ID); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err = s.RecoverStartingExternalLaunchRequests(ctx); err != nil {
				t.Fatal(err)
			}
			got, err := s.GetExternalLaunchRequest(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			expected := model.ExternalLaunchOutcomeUnknown
			if phase == model.ExternalDispatchNotSent || phase == model.ExternalDispatchThreadReady {
				expected = model.ExternalLaunchFailed
			}
			if phase == model.ExternalDispatchTurnConfirmed {
				expected = model.ExternalLaunchSessionStarted
			}
			if got.Status != expected || got.ThreadID != thread || got.TurnID != turn {
				t.Fatalf("%#v expected=%s", got, expected)
			}
			if ok, err := s.RetryExternalLaunchRequest(ctx, r.ID); err != nil || ok != (expected == model.ExternalLaunchFailed) {
				t.Fatal(ok, err)
			}
		})
	}
}
func TestTelegramRetryIsConsumedAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.BeginSyncActivation(ctx, "s", -1); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE sync_state SET state='active'`); err != nil {
		t.Fatal(err)
	}
	op := TopicCreation{ID: "old", SessionID: "s", ChatID: -1, Target: "thread:t", ThreadID: "t", Title: "same"}
	if _, err = s.PrepareTopicCreation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = s.TopicCreationResult(ctx, "old", 0, "unknown"); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"one", "two"} {
		if err = s.PutCallbackRoute(ctx, model.CallbackRoute{Token: token, Action: "external_launch_telegram_retry", ThreadID: "request", Status: model.CallbackStatusActive, PayloadJSON: "{}", CreatedAt: model.NowString()}); err != nil {
			t.Fatal(err)
		}
	}
	p, err := s.ThreadTelegramProjection(ctx, "s", "t")
	if err != nil {
		t.Fatal(err)
	}
	next, changed, err := s.ClaimTelegramProjectionRetry(ctx, "one", "next", p)
	if err != nil || !changed || next == nil {
		t.Fatal(next, changed, err)
	}
	if _, changed, err = s.ClaimTelegramProjectionRetry(ctx, "two", "duplicate", p); err != nil || changed {
		t.Fatal(changed, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RecoverTopicCreations(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = s.db.QueryRow(`SELECT state FROM sync_topic_creations WHERE id='old'`).Scan(&state); err != nil || state != "unknown" {
		t.Fatal(state, err)
	}
	if _, changed, err = s.ClaimTelegramProjectionRetry(ctx, "one", "replay", p); err != nil || changed {
		t.Fatal(changed, err)
	}
	if fresh, err := s.PrepareTopicCreation(ctx, TopicCreation{ID: "background", SessionID: "s", ChatID: -1, Target: "thread:t", ThreadID: "t", Title: "same"}); err != nil || fresh {
		t.Fatal(fresh, err)
	}
}

func TestKnownLegacyTopicAliasBlocksParallelDiscovery(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.BeginSyncActivation(ctx, "s", -1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE sync_state SET state='active'`); err != nil {
		t.Fatal(err)
	}
	r := testExternalLaunchRequest("test:legacy-alias")
	if _, err := s.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{r}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE external_launch_requests SET thread_id='recovered',status='outcome_unknown',dispatch_phase='' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	op := TopicCreation{ID: "legacy", SessionID: "s", ChatID: -1, Target: "external:" + r.ID, Title: "same", Draft: &model.SyncTopicDraft{CWD: "/project"}}
	if _, err := s.PrepareTopicCreation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err := s.TopicCreationResult(ctx, op.ID, 0, "unknown"); err != nil {
		t.Fatal(err)
	}
	p, err := s.ThreadTelegramProjection(ctx, "s", "recovered")
	if err != nil || p.State != "unknown" || p.AttemptID != "legacy" {
		t.Fatal(p, err)
	}
	if fresh, err := s.PrepareTopicCreation(ctx, TopicCreation{ID: "background", SessionID: "s", ChatID: -1, Target: "thread:recovered", ThreadID: "recovered", Title: "same"}); err != nil || fresh {
		t.Fatal(fresh, err)
	}
}
func TestBindingMessageDoesNotReactivateExpiredApproval(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	r := model.CallbackRoute{Token: "approval", Action: "sync_approval", ThreadID: "t", Status: model.CallbackStatusActive, PayloadJSON: "{}", CreatedAt: model.NowString()}
	if err := s.PutCallbackRoute(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireCallbackRoute(ctx, r.Token); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.BindActiveCallbackMessage(ctx, r.Token, 50); err != nil || ok {
		t.Fatal(ok, err)
	}
	got, err := s.GetCallbackRoute(ctx, r.Token)
	if err != nil || got.Status != model.CallbackStatusExpired {
		t.Fatal(got, err)
	}
}

func TestLegacyNormalizationBeforeMaterializationUsesCanonicalPayload(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	if err := s.BeginSyncActivation(ctx, "s", -1); err != nil {
		t.Fatal(err)
	}
	r := testExternalLaunchRequest("test:legacy-materialize")
	if _, err := s.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{r}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE external_launch_requests SET thread_id='recovered',status='starting' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	old := TopicCreation{ID: "legacy", SessionID: "s", ChatID: -1, Target: "external:" + r.ID, Title: "same", Draft: &model.SyncTopicDraft{CWD: "/project"}}
	if _, err := s.PrepareTopicCreation(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.TopicCreationResult(ctx, old.ID, 25, "confirmed"); err != nil {
		t.Fatal(err)
	}
	old.TopicID = 25
	if err := s.NormalizeExternalTopicAliases(ctx, "recovered"); err != nil {
		t.Fatal(err)
	}
	if err := s.MaterializeTopicCreation(ctx, old); err != nil {
		t.Fatal(err)
	}
	topic, err := s.GetActiveSyncTopicByThread(ctx, "s", "recovered")
	if err != nil || topic == nil || topic.TopicID != 25 {
		t.Fatal(topic, err)
	}
	drafts, err := s.ListSyncTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 0 {
		t.Fatal(drafts, err)
	}
}
