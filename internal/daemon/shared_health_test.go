package daemon

import (
	"context"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
	"time"
)

func TestRareReadDeadlinesAndTerminalHealth(t *testing.T) {
	s := activeSyncService(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	if err := s.RegisterTaskPoller(ctx, "rare", "checks", 10*time.Minute, true); err != nil {
		t.Fatal(err)
	}
	s.NoteExternalPollResult(ctx, "board.task.rare", nil)
	due := now.Add(10 * time.Minute)
	if err := s.SetTaskPollSchedule(ctx, "rare", model.TimeString(due.Format(time.RFC3339Nano)), false); err != nil {
		t.Fatal(err)
	}
	now = now.Add(6 * time.Minute)
	state := s.loadExternalPollerState(ctx, "board.task.rare")
	if _, label := externalPollerDisplayState(state, now); label == "stale" {
		t.Fatal("normal rate limit was stale")
	}
	summary, err := s.loadExternalPollHealthSummary(ctx)
	if err != nil || summary.Blocked {
		t.Fatalf("normal wait blocked recovery: %+v %v", summary, err)
	}
	now = due.Add(91 * time.Second)
	if !pollerObservationStale(state, now) {
		t.Fatal("overdue observation was healthy")
	}
	if err := s.SetTaskPollSchedule(ctx, "rare", "", true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(24 * time.Hour)
	state = s.loadExternalPollerState(ctx, "board.task.rare")
	if pollerObservationStale(state, now) {
		t.Fatal("terminal observation aged into stale")
	}
	s.NoteExternalPollResult(ctx, "board.task.rare", errors.New("independent read error"))
	summary, err = s.loadExternalPollHealthSummary(ctx)
	if err != nil || !summary.Blocked {
		t.Fatalf("actual error allowed recovery: %+v %v", summary, err)
	}
}
