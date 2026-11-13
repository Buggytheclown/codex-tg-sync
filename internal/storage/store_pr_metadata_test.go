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
}
