package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

const taskSchemaSQL = `CREATE TABLE IF NOT EXISTS tasks (
 id TEXT PRIMARY KEY, title TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
 column TEXT NOT NULL CHECK(column IN ('inbox','todo','review','polling','done')),
 position REAL NOT NULL DEFAULT 0, thread_id TEXT, source_url TEXT, source_key TEXT, source_task_id TEXT,
 task_config_json TEXT NOT NULL, version INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 source_author TEXT, priority TEXT NOT NULL DEFAULT 'normal', ticket_url TEXT NOT NULL DEFAULT '', done_at TEXT NOT NULL DEFAULT '',
 card_type TEXT NOT NULL DEFAULT 'task' CHECK(card_type IN ('','task','pr','schedule')),
 arcanum_pr_id TEXT NOT NULL DEFAULT '', parent_task_id TEXT REFERENCES tasks(id),
 CHECK(card_type='pr' OR (arcanum_pr_id='' AND parent_task_id IS NULL)),
 CHECK(card_type<>'pr' OR arcanum_pr_id<>''),
 CHECK(column<>'review' OR card_type='task'));
`
const taskTypeIndexesSQL = `
 CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_thread ON tasks(thread_id) WHERE thread_id IS NOT NULL AND thread_id<>'';
 CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_source ON tasks(source_key) WHERE source_key IS NOT NULL AND source_key<>'';
 CREATE INDEX IF NOT EXISTS idx_tasks_column ON tasks(column,position);
 CREATE INDEX IF NOT EXISTS idx_tasks_parent ON tasks(parent_task_id);
 CREATE UNIQUE INDEX IF NOT EXISTS idx_tasks_pr ON tasks(arcanum_pr_id) WHERE card_type='pr';
 CREATE TABLE IF NOT EXISTS arcanum_pr_metadata (
 pr_id TEXT PRIMARY KEY, lifecycle TEXT NOT NULL DEFAULT '', common_status TEXT NOT NULL DEFAULT '',
 last_attempt_at TEXT NOT NULL DEFAULT '',last_success_at TEXT NOT NULL DEFAULT '',last_error TEXT NOT NULL DEFAULT '',next_check_at TEXT NOT NULL DEFAULT '');
`

func normalizeCard(t *model.Task) error {
	if t.IsSource() {
		if t.Column == model.TaskReview {
			return fmt.Errorf("%w: Review is only available for Task cards", ErrTaskInvalid)
		}
		if t.CardType != "" {
			return fmt.Errorf("%w: sources have no ordinary card type", ErrTaskInvalid)
		}
		t.ArcanumPRID = ""
		return nil
	}
	if t.CardType == "" {
		t.CardType = model.CardTask
		// Compatibility for clients predating explicit card types.
		if _, err := model.ParseArcanumPR(t.SourceURL); err == nil {
			t.CardType = model.CardPR
		}
	}
	switch t.CardType {
	case model.CardTask:
		if _, err := model.ParseArcanumPR(t.SourceURL); err == nil {
			return fmt.Errorf("%w: link a PR card to this Task instead of setting a PR URL", ErrTaskInvalid)
		}
		t.ArcanumPRID = ""
	case model.CardPR:
		id, err := model.ParseArcanumPR(t.SourceURL)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrTaskInvalid, err)
		}
		if t.TicketURL != "" {
			return fmt.Errorf("%w: PR cards cannot have Tracker tickets", ErrTaskInvalid)
		}
		t.ArcanumPRID = id
		t.SourceURL = "https://a.yandex-team.ru/review/" + id
	case model.CardSchedule:
		t.ArcanumPRID = ""
	default:
		return fmt.Errorf("%w: unknown card type", ErrTaskInvalid)
	}
	if t.Column == model.TaskReview && t.CardType != model.CardTask {
		return fmt.Errorf("%w: Review is only available for Task cards", ErrTaskInvalid)
	}
	return validateCardPolling(*t, t.Polling)
}

func validateCardPolling(t model.Task, p *model.TaskPolling) error {
	if p == nil {
		return nil
	}
	if (p.Action.Type == model.PollingCreateTask) != t.IsSource() {
		return fmt.Errorf("%w: source and ordinary card identity cannot be changed", ErrTaskInvalid)
	}
	if t.CardType != model.CardPR {
		return nil
	}
	var check func(model.TaskPolling) error
	check = func(p model.TaskPolling) error {
		if p.Type == model.PollingRules {
			var cfg model.PollingRulesConfig
			if err := json.Unmarshal(p.Config, &cfg); err != nil {
				return err
			}
			for _, r := range cfg.Rules {
				if err := check(r.Polling(p)); err != nil {
					return err
				}
			}
		} else if p.Type == "pr_wait" {
			var cfg struct {
				PRID string `json:"pr_id"`
			}
			if err := json.Unmarshal(p.Config, &cfg); err != nil {
				return err
			}
			if cfg.PRID != t.ArcanumPRID {
				return fmt.Errorf("%w: PR waiting rules must refer to the card's PR; save the URL and rules together", ErrTaskInvalid)
			}
		}
		return nil
	}
	return check(*p)
}

func findPRCardTx(ctx context.Context, tx *sql.Tx, prID string) (*model.Task, error) {
	if prID == "" {
		return nil, nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE card_type='pr' AND arcanum_pr_id=?`, prID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return getTaskTx(ctx, tx, id)
}

func rejectPendingPRChangeTx(ctx context.Context, tx *sql.Tx, id string) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM external_launch_requests WHERE task_id=? AND status NOT IN ('dismissed','rejected_sender','session_completed','session_interrupted','session_failed')`, id).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("%w: handle pending or uncertain launch requests before changing the PR", ErrTaskConflict)
	}
	return nil
}

func prSourceOwnedTx(ctx context.Context, tx *sql.Tx, t model.Task) (bool, error) {
	if t.PRSourceOwned() {
		return true, nil
	}
	if t.SourceTaskID == "" {
		return false, nil
	}
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT type FROM task_polling WHERE task_id=?`, t.SourceTaskID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return model.PRSourceType(kind), err
}

func (s *Store) SetTaskParent(ctx context.Context, id string, in model.SetTaskParentInput) error {
	if in.ExpectedVersion <= 0 {
		return ErrTaskInvalid
	}
	in.ParentTaskID = strings.TrimSpace(in.ParentTaskID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := getTaskTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if err = checkTaskVersion(t, in.ExpectedVersion); err != nil {
		return err
	}
	if t.CardType != model.CardPR || t.IsSource() {
		return fmt.Errorf("%w: only PR cards have a parent Task", ErrTaskInvalid)
	}
	if in.ParentTaskID != "" {
		parent, err := getTaskTx(ctx, tx, in.ParentTaskID)
		if err != nil {
			return err
		}
		if parent.CardType != model.CardTask || parent.IsSource() {
			return fmt.Errorf("%w: parent must be a Task card", ErrTaskInvalid)
		}
	}
	if t.ParentTaskID == in.ParentTaskID {
		return nil
	}
	if _, err = tx.ExecContext(ctx, `UPDATE tasks SET parent_task_id=?,version=version+1,updated_at=? WHERE id=?`, nullable(in.ParentTaskID), model.NowString(), id); err != nil {
		return err
	}
	if err = addTaskEventTx(ctx, tx, id, "parent_task_changed", map[string]string{"from": t.ParentTaskID, "to": in.ParentTaskID}); err != nil {
		return err
	}
	return tx.Commit()
}
