package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/tasks"
)

func TestManualPRReplacementChecksAuthorityAndFrozenRequests(t *testing.T) {
	for _, state := range []string{"completed", "inProgress", "missing", "pending"} {
		t.Run(state, func(t *testing.T) {
			s := newTestService(t)
			s.cfg.AppServerMode = "websocket"
			ctx := context.Background()
			pr, err := s.store.CreateTask(ctx, model.CreateTaskInput{Title: "PR", CardType: model.CardPR, SourceURL: "42", ThreadID: "review-chat"})
			if err != nil {
				t.Fatal(err)
			}
			poll := &stubSession{threadReads: map[string]map[string]any{"review-chat": boardThreadPayload("review-chat", state, "turn", "result")}}
			if state == "pending" {
				poll.threadReads["review-chat"] = boardThreadPayload("review-chat", "completed", "turn", "result")
				request := daemonExternalRequest("test:pr-pending", "Fix old PR")
				request.TaskID = pr.ID
				request.TriggerEventID = "event:" + pr.ID
				policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
				request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
				if _, err = s.store.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{request}); err != nil {
					t.Fatal(err)
				}
			}
			if state == "missing" {
				poll.threadReads = map[string]map[string]any{}
			}
			s.poll, s.pollConnected = poll, true
			updated, err := s.UpdateTask(ctx, pr.ID, model.UpdateTaskInput{ExpectedVersion: pr.Version, Title: pr.Title, Config: pr.Config, SourceURL: "https://a.yandex-team.ru/review/43"})
			if state == "completed" {
				if err != nil || updated.ArcanumPRID != "43" {
					t.Fatalf("idle replacement: %#v %v", updated, err)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe identity replacement accepted")
				}
				got, _ := s.GetTask(ctx, pr.ID)
				if got.ArcanumPRID != "42" || got.Version != pr.Version {
					t.Fatalf("rejected change mutated card: %#v", got)
				}
				if state == "pending" && !errors.Is(err, tasks.ErrConflict) {
					t.Fatalf("pending: %v", err)
				}
			}
			if len(poll.threadResumeCalls)+len(poll.threadStartCalls)+len(poll.turnStartCalls) != 0 {
				t.Fatal("PR edit executed model work")
			}
		})
	}
}

func TestPRSameIdentityFormattingNeedsNoAuthority(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	pr, err := s.store.CreateTask(ctx, model.CreateTaskInput{Title: "PR", CardType: model.CardPR, SourceURL: "42", SourceKey: "arcanum_changes:42", ThreadID: "offline-chat"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.UpdateTask(ctx, pr.ID, model.UpdateTaskInput{ExpectedVersion: pr.Version, Title: pr.Title, Config: pr.Config, SourceURL: "https://arcanum.yandex-team.ru/review/42/details"})
	if err != nil || updated.ArcanumPRID != "42" {
		t.Fatalf("format edit: %#v %v", updated, err)
	}
	_, err = s.UpdateTask(ctx, pr.ID, model.UpdateTaskInput{ExpectedVersion: updated.Version, Title: pr.Title, Config: pr.Config, SourceURL: "43"})
	if !errors.Is(err, tasks.ErrInvalid) {
		t.Fatalf("source PR identity changed: %v", err)
	}
}
