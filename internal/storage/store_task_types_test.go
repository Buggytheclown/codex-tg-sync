package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestCardTypesParentAndReview(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Development", CardType: model.CardTask, Column: model.TaskReview, ThreadID: "development-chat", TicketURL: "TEAM-42"})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Fix review", CardType: model.CardPR, SourceURL: "https://arcanum.yandex-team.ru/review/42/details", ThreadID: "review-chat"})
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Another source", CardType: model.CardPR, SourceURL: "42"})
	if err != nil || duplicate.ID != pr.ID {
		t.Fatalf("PR identity: %#v %v", duplicate, err)
	}
	if err = s.SetTaskParent(ctx, pr.ID, model.SetTaskParentInput{ExpectedVersion: pr.Version, ParentTaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	pr, _ = s.GetTask(ctx, pr.ID)
	if pr.ParentTaskID != task.ID || pr.ThreadID != "review-chat" || pr.ArcanumPRID != "42" {
		t.Fatalf("linked PR: %#v", pr)
	}
	if err = s.SetTaskParent(ctx, pr.ID, model.SetTaskParentInput{ExpectedVersion: pr.Version, ParentTaskID: task.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.MoveTask(ctx, pr.ID, model.MoveTaskInput{ExpectedVersion: pr.Version, Column: model.TaskReview}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("PR Review move: %v", err)
	}
	if err = s.SetTaskTicket(ctx, pr.ID, model.SetTaskTicketInput{ExpectedVersion: pr.Version, TicketURL: "TEAM-9"}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("PR Tracker: %v", err)
	}
	if err = s.SetTaskParent(ctx, task.ID, model.SetTaskParentInput{ExpectedVersion: task.Version, ParentTaskID: pr.ID}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("invalid parent type: %v", err)
	}
	if err = s.SetTaskParent(ctx, pr.ID, model.SetTaskParentInput{ExpectedVersion: pr.Version - 1, ParentTaskID: ""}); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale link: %v", err)
	}
	if err = s.SetTaskParent(ctx, pr.ID, model.SetTaskParentInput{ExpectedVersion: pr.Version, ParentTaskID: ""}); err != nil {
		t.Fatal(err)
	}
	task, _ = s.GetTask(ctx, task.ID)
	if task.Column != model.TaskReview || task.ThreadID != "development-chat" || task.TicketURL == "" {
		t.Fatalf("parent changed: %#v", task)
	}
	if _, err = s.CreateTask(ctx, model.CreateTaskInput{Title: "Invalid", CardType: model.CardTask, SourceURL: "42"}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("Task PR: %v", err)
	}
	if _, err = s.CreateTask(ctx, model.CreateTaskInput{Title: "Invalid", CardType: model.CardPR}); !errors.Is(err, ErrTaskInvalid) {
		t.Fatalf("missing PR: %v", err)
	}
}

func TestPRSourceDiscoveryReusesManualCardWithoutLaunch(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	pr, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Existing", CardType: model.CardPR, SourceURL: "42", ThreadID: "original-chat"})
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"pr_review_source", "pr_changes_source"} {
		source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: kind, Column: model.TaskPollingColumn, Polling: &model.TaskPolling{Type: kind, Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "{{pr_title}}", Config: model.TaskConfig{InitialPrompt: "Review"}}}}})
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := s.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Events: []model.PollingEvent{{Key: "event", Payload: []byte(`{}`), SourceKey: kind + ":42", SourceURL: "https://a.yandex-team.ru/review/42", Title: "New title", Variables: map[string]string{"pr_id": "42"}}}}, 77)
		if err != nil || !accepted {
			t.Fatalf("discovery: %v %v", accepted, err)
		}
		events, err := s.ListTaskEvents(ctx, source.ID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range events {
			if event.ResultTaskID == pr.ID {
				found = true
			}
		}
		if !found {
			t.Fatal("event did not reuse PR")
		}
	}
	cards, err := s.ListTaskCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, card := range cards {
		if card.CardType == model.CardPR {
			count++
			if card.ID != pr.ID || card.ThreadID != "original-chat" || len(card.Requests) != 0 {
				t.Fatalf("discovery replaced/launched PR: %#v", card)
			}
		}
	}
	if count != 1 {
		t.Fatalf("%d PR cards", count)
	}
}
