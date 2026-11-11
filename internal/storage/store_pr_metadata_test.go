package storage

import (
	"context"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestPassivePRStatusDoesNotMutateCardOrPolling(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	pr, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "PR", CardType: model.CardPR, SourceURL: "42", Column: model.TaskTODO})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	due, err := s.ListDuePRMetadata(ctx, now, time.Time{}, 10)
	if err != nil || len(due) != 1 || due[0] != "42" {
		t.Fatalf("due: %v %v", due, err)
	}
	if err = s.RecordPRMetadata(ctx, "42", "open", "waiting_for_review", true, now); err != nil {
		t.Fatal(err)
	}
	card, err := s.GetTaskCard(ctx, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if card.Version != pr.Version || card.Column != model.TaskTODO || card.Polling != nil || len(card.Requests) != 0 || card.PRStatus == nil || card.PRStatus.Label != "Waiting for review" || card.PRStatus.Stale {
		t.Fatalf("passive status affected task: %#v", card)
	}
	if err = s.RecordPRMetadata(ctx, "42", "", "", false, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	card, err = s.GetTaskCard(ctx, pr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if card.PRStatus.Label != "Waiting for review" || !card.PRStatus.Stale || card.PRStatus.Unavailable {
		t.Fatalf("error lost last status: %#v", card.PRStatus)
	}
	events, err := s.ListTaskEvents(ctx, pr.ID)
	if err != nil || len(events) != 1 {
		t.Fatalf("metadata wrote history: %v %v", events, err)
	}
	if due, err = s.ListDuePRMetadata(ctx, now.Add(2*time.Minute), time.Time{}, 10); err != nil || len(due) != 0 {
		t.Fatalf("retry too soon: %v %v", due, err)
	}
}

func TestPRMetadataActivationRefreshesFreshCacheWithoutMutatingDeadline(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"42", "43"} {
		if _, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "PR", CardType: model.CardPR, SourceURL: id, Column: model.TaskDone}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := s.RecordPRMetadata(ctx, "42", "open", "waiting_for_review", true, now); err != nil {
		t.Fatal(err)
	}
	activation := now.Add(time.Minute)
	due, err := s.ListDuePRMetadata(ctx, activation, activation, 10)
	if err != nil || len(due) != 2 {
		t.Fatalf("activation missed fresh PR: %v %v", due, err)
	}
	if err := s.RecordPRMetadata(ctx, "42", "", "", false, activation.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	due, err = s.ListDuePRMetadata(ctx, activation.Add(2*time.Second), activation, 10)
	if err != nil || len(due) != 1 || due[0] != "43" {
		t.Fatalf("attempt repeated during one activation: %v %v", due, err)
	}
	due, err = s.ListDuePRMetadata(ctx, activation.Add(2*time.Second), time.Time{}, 10)
	if err != nil || len(due) != 1 || due[0] != "43" {
		t.Fatalf("ordinary deadline changed: %v %v", due, err)
	}
}
