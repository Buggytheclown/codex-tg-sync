package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func legacyTypeDB(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
 CREATE TABLE tasks (id TEXT PRIMARY KEY,title TEXT NOT NULL,description TEXT NOT NULL DEFAULT '',column TEXT NOT NULL CHECK(column IN ('inbox','todo','polling','done')),position REAL NOT NULL DEFAULT 0,thread_id TEXT,source_url TEXT,source_key TEXT,source_task_id TEXT,task_config_json TEXT NOT NULL DEFAULT '{}',version INTEGER NOT NULL DEFAULT 3,created_at TEXT NOT NULL DEFAULT '2026-10-01T10:00:00Z',updated_at TEXT NOT NULL DEFAULT '2026-10-01T10:00:00Z',source_author TEXT,priority TEXT NOT NULL DEFAULT 'normal',ticket_url TEXT NOT NULL DEFAULT '',done_at TEXT NOT NULL DEFAULT '');
 CREATE TABLE task_polling(task_id TEXT PRIMARY KEY REFERENCES tasks(id),type TEXT NOT NULL,mode TEXT NOT NULL,config_json TEXT NOT NULL,action_json TEXT NOT NULL,cursor_json TEXT NOT NULL DEFAULT '{}',generation INTEGER NOT NULL DEFAULT 9,next_check_at TEXT NOT NULL DEFAULT '',last_attempt_at TEXT NOT NULL DEFAULT '',last_success_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '');
 CREATE TABLE task_events(id TEXT PRIMARY KEY,task_id TEXT NOT NULL REFERENCES tasks(id),kind TEXT NOT NULL,event_key TEXT,result_task_id TEXT REFERENCES tasks(id),payload_json TEXT NOT NULL,created_at TEXT NOT NULL);
 INSERT INTO tasks(id,title,column,thread_id,source_url,ticket_url,done_at) VALUES('dev','Development','done','dev-chat','https://a.yandex-team.ru/review/42','https://st.yandex-team.ru/TEAM-42','2026-10-03T10:00:00Z');
 INSERT INTO tasks(id,title,column,source_key) VALUES('src','Feedback','polling','source:arcanum_changes');
 INSERT INTO tasks(id,title,column,thread_id,source_url,source_key,source_task_id,ticket_url) VALUES('pr','Review','todo','pr-chat','https://a.yandex-team.ru/review/42','arcanum_changes:42','src','https://st.yandex-team.ru/TEAM-42');
 INSERT INTO tasks(id,title,column,thread_id,source_key) VALUES('schedule','Reading','inbox','cron-chat','cron:reading:slot');
 INSERT INTO task_polling(task_id,type,mode,config_json,action_json) VALUES('src','pr_changes_source','repeat','{}','{"type":"create_task","child_definition":{"title":"PR","config":{"initial_prompt":"Review"}}}');
 INSERT INTO task_events(rowid,id,task_id,kind,result_task_id,payload_json,created_at) VALUES(77,'old-event','src','polling_event','pr','{"preserved":true}','2026-10-01T10:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return path, db
}

func TestCardTypeMigrationPreviewFenceRollbackAndHistory(t *testing.T) {
	ctx := context.Background()
	path, db := legacyTypeDB(t)
	if s, err := Open(path); !errors.Is(err, ErrCardMigrationRequired) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("old startup must require offline migration: %v", err)
	}
	report, err := PreviewCardTypeMigration(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Cards) != 4 {
		t.Fatalf("preview: %#v", report)
	}
	if _, err = db.Exec(`UPDATE tasks SET title='changed' WHERE id='dev'`); err != nil {
		t.Fatal(err)
	}
	if err = ApplyCardTypeMigration(ctx, path, report); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale map: %v", err)
	}
	report, err = PreviewCardTypeMigration(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`CREATE TRIGGER reject_history BEFORE INSERT ON task_events BEGIN SELECT RAISE(ABORT,'history failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err = ApplyCardTypeMigration(ctx, path, report); err == nil {
		t.Fatal("migration ignored failed history")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM pragma_table_info('tasks') WHERE name='card_type'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial schema: %d %v", count, err)
	}
	if _, err = db.Exec(`DROP TRIGGER reject_history`); err != nil {
		t.Fatal(err)
	}
	if err = ApplyCardTypeMigration(ctx, path, report); err != nil {
		t.Fatal(err)
	}
	if err = ApplyCardTypeMigration(ctx, path, report); err != nil {
		t.Fatal("repeat apply", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dev, err := s.GetTask(ctx, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if dev.CardType != model.CardTask || dev.SourceURL != "" || dev.ThreadID != "dev-chat" || dev.DoneAt != "2026-10-03T10:00:00Z" || dev.Version != 3 {
		t.Fatalf("dev lost data: %#v", dev)
	}
	pr, err := s.GetTask(ctx, "pr")
	if err != nil {
		t.Fatal(err)
	}
	if pr.CardType != model.CardPR || pr.ParentTaskID != "" || pr.TicketURL != "" || pr.ThreadID != "pr-chat" || pr.Column != "todo" {
		t.Fatalf("PR lost data: %#v", pr)
	}
	schedule, _ := s.GetTask(ctx, "schedule")
	if schedule.CardType != model.CardSchedule {
		t.Fatalf("cron kind: %#v", schedule)
	}
	events, err := s.ListTaskEvents(ctx, "dev")
	if err != nil || len(events) != 1 || events[0].Kind != "legacy_pr_unlinked" {
		t.Fatalf("history: %#v %v", events, err)
	}
	var rowid int64
	if err = s.db.QueryRow(`SELECT rowid FROM task_events WHERE id='old-event'`).Scan(&rowid); err != nil || rowid != 77 {
		t.Fatalf("event ownership changed: %d %v", rowid, err)
	}
	var fk int
	if err = s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("FK disabled: %d %v", fk, err)
	}
	if _, err = s.MoveTask(ctx, "dev", model.MoveTaskInput{ExpectedVersion: dev.Version, Column: model.TaskReview}); err != nil {
		t.Fatal("new CHECK", err)
	}
}

func TestCardTypeMigrationPreservesEarlierDoneAndSourceBackfills(t *testing.T) {
	ctx := context.Background()
	path, db := legacyTypeDB(t)
	_, err := db.Exec(`ALTER TABLE tasks DROP COLUMN done_at; ALTER TABLE tasks DROP COLUMN source_task_id;
 UPDATE tasks SET source_key='legacy-review-cycle' WHERE id='pr';
 INSERT INTO task_events(id,task_id,kind,payload_json,created_at) VALUES('done-move','dev','moved','{"from":"todo","to":"done"}','2026-10-03T10:00:00Z');`)
	if err != nil {
		t.Fatal(err)
	}
	report, err := PreviewCardTypeMigration(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyCardTypeMigration(ctx, path, report); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dev, err := s.GetTask(ctx, "dev")
	if err != nil || dev.DoneAt != "2026-10-03T10:00:00Z" {
		t.Fatalf("Done date lost: %#v %v", dev, err)
	}
	pr, err := s.GetTask(ctx, "pr")
	if err != nil || pr.CardType != model.CardPR || pr.SourceTaskID != "src" {
		t.Fatalf("provenance lost: %#v %v", pr, err)
	}
}

func TestCardTypeMigrationEmptySchemasAndRepeat(t *testing.T) {
	ctx := context.Background()
	path, db := legacyTypeDB(t)
	if _, err := db.Exec(`DELETE FROM task_events; DELETE FROM task_polling; DELETE FROM tasks`); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(path); !errors.Is(err, ErrCardMigrationRequired) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("legacy empty schema accepted: %v", err)
	}
	report, err := PreviewCardTypeMigration(ctx, path)
	if err != nil || report.AlreadyMigrated || len(report.Cards) != 0 {
		t.Fatalf("empty preview: %#v %v", report, err)
	}
	for i := 0; i < 2; i++ {
		if err = ApplyCardTypeMigration(ctx, path, report); err != nil {
			t.Fatal("repeat empty apply", err)
		}
	}
	typed, err := PreviewCardTypeMigration(ctx, path)
	if err != nil || !typed.AlreadyMigrated {
		t.Fatalf("new empty schema not detected: %#v %v", typed, err)
	}
	freshPath := filepath.Join(t.TempDir(), "fresh.db")
	s, err := Open(freshPath)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	fresh, err := PreviewCardTypeMigration(ctx, freshPath)
	if err != nil || !fresh.AlreadyMigrated {
		t.Fatalf("fresh empty: %#v %v", fresh, err)
	}
}

func TestCardTypeMigrationFencesReferencedLegacyDefinitions(t *testing.T) {
	ctx := context.Background()
	path, db := legacyTypeDB(t)
	_, err := db.Exec(`CREATE TABLE task_templates(id TEXT PRIMARY KEY,definition_json TEXT NOT NULL);
 INSERT INTO task_templates VALUES('definition','{"title":"Original PR","config":{"initial_prompt":"Review"}}');
 UPDATE task_polling SET action_json='{"type":"create_task","template_id":"definition"}' WHERE task_id='src';`)
	if err != nil {
		t.Fatal(err)
	}
	report, err := PreviewCardTypeMigration(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE task_templates SET definition_json='{"title":"Changed PR","config":{"initial_prompt":"Review"}}'`); err != nil {
		t.Fatal(err)
	}
	if err = ApplyCardTypeMigration(ctx, path, report); !errors.Is(err, ErrTaskConflict) {
		t.Fatalf("stale definition accepted: %v", err)
	}
	report, err = PreviewCardTypeMigration(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err = ApplyCardTypeMigration(ctx, path, report); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	source, err := s.GetTask(ctx, "src")
	if err != nil || source.Polling.Action.ChildDefinition == nil || source.Polling.Action.ChildDefinition.Title != "Changed PR" {
		t.Fatalf("owned definition: %#v %v", source, err)
	}
}

func TestCardTypePreviewKeepsExistingExplicitManualPRType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "typed.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	pr, err := s.CreateTask(context.Background(), model.CreateTaskInput{Title: "Manual PR", CardType: model.CardPR, SourceURL: "42"})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	report, err := PreviewCardTypeMigration(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if !report.AlreadyMigrated || len(report.Cards) != 1 || report.Cards[0].ID != pr.ID || report.Cards[0].CardType != model.CardPR || report.Cards[0].ArchivePR {
		t.Fatalf("typed preview reclassifies card: %#v", report)
	}
}
