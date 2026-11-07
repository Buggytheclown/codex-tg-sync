package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"path/filepath"
	"testing"
)

func TestTopicCreationUnknownSurvivesRestartAndDoesNotBlockOtherTargets(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := TopicCreation{ID: "a", SessionID: "s", ChatID: -1, Target: "thread:a", Title: "same", ThreadID: "a"}
	if fresh, err := s.PrepareTopicCreation(ctx, a); err != nil || !fresh {
		t.Fatal(fresh, err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if fresh, err := s.PrepareTopicCreation(ctx, a); err != nil || fresh {
		t.Fatal(fresh, err)
	}
	b := a
	b.ID = "b"
	b.Target = "thread:b"
	b.ThreadID = "b"
	if fresh, err := s.PrepareTopicCreation(ctx, b); err != nil || !fresh {
		t.Fatal(fresh, err)
	}
}

func TestTopicCreationAmbiguousNameNeverBinds(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"a", "b"} {
		_, err = s.PrepareTopicCreation(ctx, TopicCreation{ID: id, SessionID: "s", ChatID: -1, Target: "thread:" + id, ThreadID: id, Title: "same"})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordTopicCreated(ctx, -1, 20, 10, "same"); err != nil {
		t.Fatal(err)
	}
	ops, err := s.PendingTopicCreations(ctx)
	if err != nil || len(ops) != 2 {
		t.Fatal(ops, err)
	}
	for _, op := range ops {
		if op.TopicID != 0 {
			t.Fatal("ambiguous binding", op)
		}
	}
}

func TestTopicCreationEventRecoveryIsAtomicAndLateOffCleans(t *testing.T) {
	for _, off := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "off"}[off], func(t *testing.T) {
			ctx := context.Background()
			s, err := Open(filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.BeginSyncActivation(ctx, "s", -1); err != nil {
				t.Fatal(err)
			}
			op := TopicCreation{ID: "a", SessionID: "s", ChatID: -1, Target: "thread:a", ThreadID: "a", Title: "name"}
			if _, err = s.PrepareTopicCreation(ctx, op); err != nil {
				t.Fatal(err)
			}
			if err = s.TopicCreationResult(ctx, "a", 0, "unknown"); err != nil {
				t.Fatal(err)
			}
			if off {
				if _, err = s.MarkSyncOff(ctx, "s"); err != nil {
					t.Fatal(err)
				}
			}
			if err = s.RecordTopicCreated(ctx, -1, 30, 10, "name"); err != nil {
				t.Fatal(err)
			}
			ops, err := s.PendingTopicCreations(ctx)
			if err != nil || len(ops) != 1 || ops[0].TopicID != 30 {
				t.Fatal(ops, err)
			}
			if err = s.MaterializeTopicCreation(ctx, ops[0]); err != nil {
				t.Fatal(err)
			}
			topics, err := s.ListSyncTopics(ctx, "s")
			if err != nil || len(topics) != 1 {
				t.Fatal(topics, err)
			}
			want := "connected"
			if off {
				want = "cleanup"
			}
			if topics[0].TelegramState != want {
				t.Fatal(topics)
			}
			if err = s.RecordTopicCreated(ctx, -1, 30, 10, "name"); err != nil {
				t.Fatal(err)
			}
			ops, err = s.PendingTopicCreations(ctx)
			if err != nil || len(ops) != 0 {
				t.Fatal(ops, err)
			}
		})
	}
}

func TestTopicCreationLegacyUnknownNeverMatchesNewName(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.BeginSyncActivation(ctx, "old", -1); err != nil {
		t.Fatal(err)
	}
	if err = s.SetState(ctx, "sync.topic_create.old", "old-thread"); err != nil {
		t.Fatal(err)
	}
	if err = s.ImportLegacyTopicCreations(ctx); err != nil {
		t.Fatal(err)
	}
	if err = s.BeginSyncActivation(ctx, "new", -1); err != nil {
		t.Fatal(err)
	}
	_, err = s.PrepareTopicCreation(ctx, TopicCreation{ID: "new", SessionID: "new", ChatID: -1, Target: "thread:new", ThreadID: "new", Title: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTopicCreated(ctx, -1, 30, 10, "same"); err != nil {
		t.Fatal(err)
	}
	ops, err := s.PendingTopicCreations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range ops {
		if op.TopicID != 0 {
			t.Fatal("legacy event guessed as new operation", op)
		}
	}
}

func TestTopicCreationConfirmationCannotRecreateConsumedDraft(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.BeginSyncActivation(ctx, "s", -1); err != nil {
		t.Fatal(err)
	}
	op := TopicCreation{ID: "a", SessionID: "s", ChatID: -1, Target: "draft:a", Title: "name", Draft: &model.SyncTopicDraft{CWD: "/tmp"}}
	if _, err = s.PrepareTopicCreation(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.TopicID = 30
	if err = s.TopicCreationResult(ctx, op.ID, 30, "confirmed"); err != nil {
		t.Fatal(err)
	}
	if err = s.MaterializeTopicCreation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteSyncTopic(ctx, "s", 30); err != nil {
		t.Fatal(err)
	}
	if err = s.MaterializeTopicCreation(ctx, op); err != nil {
		t.Fatal(err)
	}
	drafts, err := s.ListSyncTopicDrafts(ctx, "s")
	if err != nil || len(drafts) != 0 {
		t.Fatal("confirmation recreated consumed draft", drafts, err)
	}
}

func TestTopicCreationBindingFailureRollsBackAndCanRecover(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.BeginSyncActivation(ctx, "s", -1); err != nil {
		t.Fatal(err)
	}
	op := TopicCreation{ID: "a", SessionID: "s", ChatID: -1, Target: "thread:a", ThreadID: "a", Title: "name"}
	if _, err = s.PrepareTopicCreation(ctx, op); err != nil {
		t.Fatal(err)
	}
	op.TopicID = 30
	if err = s.TopicCreationResult(ctx, op.ID, 30, "confirmed"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER fail_topic_binding BEFORE INSERT ON sync_topics BEGIN SELECT RAISE(ABORT,'injected bind failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err = s.MaterializeTopicCreation(ctx, op); err == nil {
		t.Fatal("binding failure was ignored")
	}
	pending, err := s.PendingTopicCreations(ctx)
	if err != nil || len(pending) != 1 || pending[0].TopicID != 30 {
		t.Fatal(pending, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER fail_topic_binding`); err != nil {
		t.Fatal(err)
	}
	if err = s.MaterializeTopicCreation(ctx, pending[0]); err != nil {
		t.Fatal(err)
	}
	topics, err := s.ListSyncTopics(ctx, "s")
	if err != nil || len(topics) != 1 {
		t.Fatal(topics, err)
	}
}
