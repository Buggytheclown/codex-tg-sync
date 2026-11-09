package storage

import (
	"context"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/model"
	"path/filepath"
	"testing"
)

func TestNewExternalCardPrecedesFullHistoricalBatch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		r := testExternalLaunchRequest(fmt.Sprintf("old:%02d", i))
		r.CreatedAt = "2026-01-01T00:00:00Z"
		if _, err := s.IngestExternalLaunchRequests(ctx, "test", int64(i+1), []model.ExternalLaunchRequest{r}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET status='session_completed',telegram_rendered_status='session_completed',thread_id=?,telegram_message_id=? WHERE id=?`, r.ID, i+1, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	fresh := testExternalLaunchRequest("new:manual")
	if _, err := s.IngestExternalLaunchRequests(ctx, "test", 31, []model.ExternalLaunchRequest{fresh}); err != nil {
		t.Fatal(err)
	}
	requests, err := s.ListExternalLaunchRequestsForTelegram(ctx, 1)
	if err != nil || len(requests) != 1 || requests[0].ID != fresh.ID {
		t.Fatalf("selection=%+v err=%v", requests, err)
	}
	foreground, err := s.ListExternalTelegramPriority(ctx, 20, false)
	if err != nil || len(foreground) != 1 || foreground[0].ID != fresh.ID {
		t.Fatal(foreground, err)
	}
	background, err := s.ListExternalTelegramPriority(ctx, 20, true)
	if err != nil || len(background) != 20 {
		t.Fatal(len(background), err)
	}
	for _, r := range background {
		if r.TelegramForeground() {
			t.Fatal("maintenance consumed foreground", r.ID)
		}
	}
}

func TestManualTelegramPrioritySurvivesReopenAndLateRender(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	r := testExternalLaunchRequest("manual:recovery")
	if _, err = s.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{r}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE external_launch_requests SET status='session_completed',telegram_rendered_status='session_completed',thread_id='old',telegram_message_id=1 WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.PrioritizeExternalTelegram(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ := s.GetExternalLaunchRequest(ctx, r.ID)
	if !got.TelegramForeground() || got.TelegramRenderedStatus != model.ExternalLaunchSessionCompleted {
		t.Fatal(got)
	}
	p, err := s.ThreadTelegramProjection(ctx, "", "old")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PrioritizeExternalTelegram(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.MarkExternalTelegramFingerprint(ctx, r.ID, "old-fp", p, got.TelegramPriorityVersion); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetExternalLaunchRequest(ctx, r.ID)
	if !got.TelegramForeground() || got.TelegramRenderedPriorityVersion != 0 {
		t.Fatal("late render consumed newer intent", got)
	}
	if err = s.MarkExternalTelegramFingerprint(ctx, r.ID, "new-fp", p, got.TelegramPriorityVersion); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetExternalLaunchRequest(ctx, r.ID)
	if got.TelegramForeground() {
		t.Fatal("priority never acknowledged", got)
	}
	// An actual old schema opens without treating historical cards as new intent.
	for _, col := range []string{"telegram_priority_version", "telegram_rendered_priority_version"} {
		if _, err = s.db.Exec("ALTER TABLE external_launch_requests DROP COLUMN " + col); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, _ = s.GetExternalLaunchRequest(ctx, r.ID)
	if got.TelegramForeground() || got.TelegramRenderedStatus != model.ExternalLaunchSessionCompleted {
		t.Fatal("upgrade fabricated manual intent", got)
	}
}

func TestManualKnownTopicCompletionRemainsForeground(t *testing.T) {
	for _, message := range []string{"", "delivery failed"} {
		t.Run(message, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			r := testExternalLaunchRequest("manual:known-topic")
			if _, err := s.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{r}); err != nil {
				t.Fatal(err)
			}
			if err := s.BeginSyncActivation(ctx, "s", -1); err != nil {
				t.Fatal(err)
			}
			if err := s.FinishSyncActivation(ctx, "s", "{}", true); err != nil {
				t.Fatal(err)
			}
			if err := s.UpsertSyncTopic(ctx, model.SyncTopic{SessionID: "s", ChatID: -1, TopicID: 42, ThreadID: "t", TelegramState: model.SyncTopicConnected}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(`UPDATE external_launch_requests SET status='session_completed',telegram_rendered_status='session_completed',telegram_message_id=1,thread_id='t' WHERE id=?`, r.ID); err != nil {
				t.Fatal(err)
			}
			if err := s.SetSyncDeliveryError(ctx, "s", 42, 0, "old error"); err != nil {
				t.Fatal(err)
			}
			p, err := s.ThreadTelegramProjection(ctx, "s", "t")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.PutCallbackRoute(ctx, model.CallbackRoute{Token: "manual", RequestID: r.ID, ThreadID: r.ID, Action: "external_launch_telegram_retry", Status: model.CallbackStatusActive, PayloadJSON: "{}", CreatedAt: model.NowString()}); err != nil {
				t.Fatal(err)
			}
			if op, ok, err := s.ClaimTelegramProjectionRetry(ctx, "manual", "unused", p); err != nil || !ok || op != nil {
				t.Fatal(op, ok, err)
			}
			intermediate, _ := s.ThreadTelegramProjection(ctx, "s", "t")
			got, _ := s.GetExternalLaunchRequest(ctx, r.ID)
			if err := s.MarkExternalTelegramFingerprint(ctx, r.ID, "accepted", intermediate, got.TelegramPriorityVersion); err != nil {
				t.Fatal(err)
			}
			if err := s.SetSyncDeliveryError(ctx, "s", 42, intermediate.Revision, message); err != nil {
				t.Fatal(err)
			}
			got, _ = s.GetExternalLaunchRequest(ctx, r.ID)
			if !got.TelegramForeground() {
				t.Fatal("manual result downgraded after early card", got)
			}
			version := got.TelegramPriorityVersion
			if err := s.SetSyncDeliveryError(ctx, "s", 42, intermediate.Revision, "late old result"); err != nil {
				t.Fatal(err)
			}
			got, _ = s.GetExternalLaunchRequest(ctx, r.ID)
			if got.TelegramPriorityVersion != version {
				t.Fatal("old delivery repeated manual completion", got)
			}
		})
	}
}

func TestManualTopicOutcomePriorityIsAtomicAcrossRestart(t *testing.T) {
	for _, outcome := range []string{"failed", "unknown-after-restart", "confirmed"} {
		t.Run(outcome, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "state.sqlite")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			r := testExternalLaunchRequest("manual:creation")
			if _, err = s.IngestExternalLaunchRequests(ctx, "test", 1, []model.ExternalLaunchRequest{r}); err != nil {
				t.Fatal(err)
			}
			if err = s.BeginSyncActivation(ctx, "s", -1); err != nil {
				t.Fatal(err)
			}
			if err = s.FinishSyncActivation(ctx, "s", "{}", true); err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.Exec(`UPDATE external_launch_requests SET status='session_completed',telegram_rendered_status='session_completed',telegram_message_id=1,thread_id='t' WHERE id=?`, r.ID); err != nil {
				t.Fatal(err)
			}
			op := TopicCreation{ID: "manual-operation", ManualRequestID: r.ID, SessionID: "s", ChatID: -1, ThreadID: "t", Target: "thread:t", Title: "manual recovery"}
			if _, err = s.PrepareTopicCreation(ctx, op); err != nil {
				t.Fatal(err)
			}
			if err = s.PrioritizeExternalTelegram(ctx, r.ID); err != nil {
				t.Fatal(err)
			}
			p, _ := s.ThreadTelegramProjection(ctx, "s", "t")
			got, _ := s.GetExternalLaunchRequest(ctx, r.ID)
			if err = s.MarkExternalTelegramFingerprint(ctx, r.ID, "intermediate", p, got.TelegramPriorityVersion); err != nil {
				t.Fatal(err)
			}
			if outcome == "failed" {
				err = s.TopicCreationResult(ctx, op.ID, 0, "failed")
			} else if outcome == "confirmed" {
				err = s.TopicCreationResult(ctx, op.ID, 42, "confirmed")
			}
			if err != nil {
				t.Fatal(err)
			}
			s.Close()
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if outcome == "unknown-after-restart" {
				if err = s.RecoverTopicCreations(ctx); err != nil {
					t.Fatal(err)
				}
			}
			got, _ = s.GetExternalLaunchRequest(ctx, r.ID)
			if !got.TelegramForeground() {
				t.Fatal("durable outcome lost manual priority", got)
			}
			if outcome == "confirmed" {
				p, _ = s.ThreadTelegramProjection(ctx, "s", "t")
				if err = s.MarkExternalTelegramFingerprint(ctx, r.ID, "confirmed-before-binding", p, got.TelegramPriorityVersion); err != nil {
					t.Fatal(err)
				}
				op.TopicID = 42
				if err = s.MaterializeTopicCreation(ctx, op); err != nil {
					t.Fatal(err)
				}
				got, _ = s.GetExternalLaunchRequest(ctx, r.ID)
				var marker string
				if err = s.db.QueryRow(`SELECT delivery_request_id FROM sync_topics WHERE session_id='s' AND topic_id=42`).Scan(&marker); err != nil {
					t.Fatal(err)
				}
				if !got.TelegramForeground() || marker != r.ID {
					t.Fatal("binding did not arm manual delivery", got, marker)
				}
			}
		})
	}
}
