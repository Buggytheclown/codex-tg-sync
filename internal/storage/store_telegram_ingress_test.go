package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTelegramOffCancelsUnsentInboxWithoutLosingProcessing(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.BeginSyncActivation(ctx, "s", -1); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		if err = s.RecordTelegramIngress(ctx, TelegramIngress{BotID: 9, UpdateID: id, Payload: "{}"}, false); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.ClaimTelegramIngress(ctx, 9, 0)
	if err != nil || first.UpdateID != 1 {
		t.Fatal(first, err)
	}
	if err = s.RecordTelegramIngress(ctx, TelegramIngress{BotID: 9, UpdateID: 3, Payload: "{}"}, true); err != nil {
		t.Fatal(err)
	}
	off, err := s.ClaimTelegramIngress(ctx, 9, 0)
	if err != nil || off.UpdateID != 3 || off.SessionID != "s" {
		t.Fatal(off, err)
	}
	if err = s.FinishTelegramIngress(ctx, 9, 3); err != nil {
		t.Fatal(err)
	}
	if err = s.RecoverTelegramIngress(ctx, 9); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.ClaimTelegramIngress(ctx, 9, 0)
	if err != nil || recovered != nil {
		t.Fatal(recovered, err)
	}

	next, err := s.ClaimTelegramIngress(ctx, 9, 0)
	if err != nil || next != nil {
		t.Fatal("canceled command replayed", next, err)
	}
}

func TestLaterOffFencesActivationBeforeNewSession(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.BeginSyncActivation(ctx, "old", -1); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTelegramIngress(ctx, TelegramIngress{BotID: 9, UpdateID: 2, Payload: "{}"}, true); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginSyncActivation(ctx, "stale-on", -1, 9, 1); err != ErrSyncActivationSuperseded {
		t.Fatalf("older on was not fenced: %v", err)
	}
	if err = s.BeginSyncActivation(ctx, "new-on", -1, 9, 3); err != nil {
		t.Fatal(err)
	}
}
