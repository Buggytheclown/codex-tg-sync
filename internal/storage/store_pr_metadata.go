package storage

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Store) RecordPRMetadata(ctx context.Context, id, lifecycle, common string, success bool, now time.Time) error {
	number, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return ErrTaskInvalid
	}
	return s.PublishPRObservation(ctx, number, lifecycle, common, now, now, success)
}

func (s *Store) PRIsMerged(ctx context.Context, id int64) (bool, error) {
	var lifecycle string
	err := s.db.QueryRowContext(ctx, `SELECT lifecycle FROM arcanum_pr_metadata WHERE pr_id=?`, strconv.FormatInt(id, 10)).Scan(&lifecycle)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return lifecycle == "merged", err
}

// Incoming times describe the original IO, never a cache hit or local retry.
func (s *Store) PublishPRObservation(ctx context.Context, id int64, lifecycle, common string, started, finished time.Time, success bool) error {
	if id <= 0 || started.IsZero() || finished.Before(started) {
		return ErrTaskInvalid
	}
	if success && (lifecycle == "" || (lifecycle != "merged" && common == "")) {
		return ErrTaskInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previousLifecycle, previousAt string
	err = tx.QueryRowContext(ctx, `SELECT lifecycle,last_attempt_at FROM arcanum_pr_metadata WHERE pr_id=?`, strconv.FormatInt(id, 10)).Scan(&previousLifecycle, &previousAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	previousTime, _ := time.Parse(time.RFC3339Nano, previousAt)
	if previousLifecycle == "merged" || (previousTime.After(started) && lifecycle != "merged") {
		return nil
	}
	at := started.UTC().Format(time.RFC3339Nano)
	good := ""
	problem := "Status unavailable"
	if success {
		good = at
		problem = ""
	} else {
		lifecycle = ""
		common = ""
	}
	next := started.Add(30 * time.Second).UTC().Format(time.RFC3339Nano)
	if lifecycle == "merged" {
		next = ""
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO arcanum_pr_metadata(pr_id,lifecycle,common_status,last_attempt_at,last_success_at,last_error,next_check_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(pr_id) DO UPDATE SET
 lifecycle=CASE WHEN ? THEN excluded.lifecycle ELSE arcanum_pr_metadata.lifecycle END,
 common_status=CASE WHEN ? THEN excluded.common_status ELSE arcanum_pr_metadata.common_status END,
 last_attempt_at=excluded.last_attempt_at,last_success_at=CASE WHEN ? THEN excluded.last_success_at ELSE arcanum_pr_metadata.last_success_at END,
 last_error=excluded.last_error,next_check_at=excluded.next_check_at
 WHERE arcanum_pr_metadata.lifecycle<>'merged'`, strconv.FormatInt(id, 10), lifecycle, common, at, good, problem, next, success, success, success)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func projectPRStatus(kind, lifecycle, common, attempt, success, problem string, now time.Time) *model.PRStatus {
	if kind != model.CardPR {
		return nil
	}
	p := &model.PRStatus{Lifecycle: lifecycle, CommonStatus: common, LastAttemptAt: model.TimeString(attempt), LastSuccessAt: model.TimeString(success), Unavailable: attempt != "" && success == ""}
	if success != "" {
		at, err := time.Parse(time.RFC3339Nano, success)
		p.Stale = lifecycle != "merged" && (problem != "" || err != nil || now.Sub(at) > time.Minute)
		p.Label = model.PRStatusLabel(lifecycle, common)
	}
	return p
}
func (s *Store) taskPRStatus(ctx context.Context, t *model.Task) error {
	if t.CardType != model.CardPR {
		return nil
	}
	var lifecycle, common, attempt, success, problem string
	err := s.db.QueryRowContext(ctx, `SELECT lifecycle,common_status,last_attempt_at,last_success_at,last_error FROM arcanum_pr_metadata WHERE pr_id=?`, t.ArcanumPRID).Scan(&lifecycle, &common, &attempt, &success, &problem)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	t.PRStatus = projectPRStatus(t.CardType, lifecycle, common, attempt, success, problem, time.Now())
	return nil
}
