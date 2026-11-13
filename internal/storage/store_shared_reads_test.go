package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
	"time"
)

func TestArchiveExcludedFromRegularCardsAndTerminalIsMonotone(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	active, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "active", Column: model.TaskTODO})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "archived", Column: model.TaskDone})
	if err != nil {
		t.Fatal(err)
	}
	cards, err := s.ListTaskCards(ctx)
	if err != nil || len(cards) != 1 || cards[0].ID != active.ID {
		t.Fatalf("cards %+v %v", cards, err)
	}
	archive, err := s.ListDoneTaskCards(ctx)
	if err != nil || len(archive) != 1 || archive[0].ID != done.ID {
		t.Fatalf("archive %+v %v", archive, err)
	}
	card, err := s.GetTaskCard(ctx, done.ID)
	if err != nil || card == nil {
		t.Fatalf("point read %v", err)
	}
	now := time.Now().UTC()
	if err := s.PublishPRObservation(ctx, 42, "merged", "", now, now, true); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishPRObservation(ctx, 42, "open", "waiting_for_review", now.Add(time.Minute), now.Add(time.Minute), true); err != nil {
		t.Fatal(err)
	}
	merged, err := s.PRIsMerged(ctx, 42)
	if err != nil || !merged {
		t.Fatalf("terminal %v %v", merged, err)
	}
}

func TestOlderStartedTerminalOverridesNewerOpenAndPreciseTimesFenceOrdinaryRows(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.PublishPRObservation(ctx, 42, "open", "waiting_for_changes", now.Add(2*time.Nanosecond), now.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishPRObservation(ctx, 42, "open", "waiting_for_review", now.Add(time.Nanosecond), now.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	var common string
	if err := s.db.QueryRowContext(ctx, `SELECT common_status FROM arcanum_pr_metadata WHERE pr_id='42'`).Scan(&common); err != nil || common != "waiting_for_changes" {
		t.Fatalf("precision fence %s %v", common, err)
	}
	if err := s.PublishPRObservation(ctx, 42, "merged", "merged", now, now.Add(2*time.Second), true); err != nil {
		t.Fatal(err)
	}
	merged, err := s.PRIsMerged(ctx, 42)
	if err != nil || !merged {
		t.Fatalf("terminal rejected: %v %v", merged, err)
	}
}

func TestActiveParentKeepsCompactArchivedPRReferenceWithoutFooterDemand(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	parent, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Development", Column: model.TaskTODO})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "Archived PR", CardType: model.CardPR, SourceURL: "42", Column: model.TaskDone})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskParent(ctx, pr.ID, model.SetTaskParentInput{ExpectedVersion: pr.Version, ParentTaskID: parent.ID}); err != nil {
		t.Fatal(err)
	}
	cards, err := s.ListTaskCards(ctx)
	if err != nil || len(cards) != 1 || len(cards[0].RelatedCards) != 1 || cards[0].RelatedCards[0].ID != pr.ID {
		t.Fatalf("linked archive reference: %+v %v", cards, err)
	}
	demand, err := s.ListPRFooterDemand(ctx)
	if err != nil || len(demand) != 0 {
		t.Fatalf("archived relation created Arc demand: %+v %v", demand, err)
	}
	links, err := s.TaskLinks(ctx, parent.ID, "")
	if err != nil || len(links) != 1 || links[0].ID != pr.ID {
		t.Fatalf("explicit links: %+v %v", links, err)
	}
}
