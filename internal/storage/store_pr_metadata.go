package storage

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Store) ListDuePRMetadata(ctx context.Context, now, refreshSince time.Time, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		limit = 10
	}
	cutoff := ""
	if !refreshSince.IsZero() {
		cutoff = refreshSince.UTC().Format(time.RFC3339Nano)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.arcanum_pr_id FROM tasks t LEFT JOIN arcanum_pr_metadata m ON m.pr_id=t.arcanum_pr_id WHERE t.card_type='pr' AND (m.pr_id IS NULL OR m.next_check_at<=? OR (?<>'' AND m.last_attempt_at<?)) ORDER BY coalesce(m.next_check_at,''),t.arcanum_pr_id LIMIT ?`, now.UTC().Format(time.RFC3339Nano), cutoff, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) RecordPRMetadata(ctx context.Context, id, lifecycle, common string, success bool, now time.Time) error {
	if _, err := model.ParseArcanumPR(id); err != nil {
		return ErrTaskInvalid
	}
	if success && (lifecycle == "" || (lifecycle != "merged" && common == "")) {
		return ErrTaskInvalid
	}
	lastSuccess := ""
	problem := "Status unavailable"
	if success {
		lastSuccess = now.UTC().Format(time.RFC3339Nano)
		problem = ""
	} else {
		lifecycle = ""
		common = ""
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO arcanum_pr_metadata(pr_id,lifecycle,common_status,last_attempt_at,last_success_at,last_error,next_check_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(pr_id) DO UPDATE SET lifecycle=CASE WHEN ? THEN excluded.lifecycle ELSE arcanum_pr_metadata.lifecycle END,common_status=CASE WHEN ? THEN excluded.common_status ELSE arcanum_pr_metadata.common_status END,last_attempt_at=excluded.last_attempt_at,last_success_at=CASE WHEN ? THEN excluded.last_success_at ELSE arcanum_pr_metadata.last_success_at END,last_error=excluded.last_error,next_check_at=excluded.next_check_at`, id, lifecycle, common, now.UTC().Format(time.RFC3339Nano), lastSuccess, problem, now.Add(5*time.Minute).UTC().Format(time.RFC3339Nano), success, success, success)
	return err
}

func projectPRStatus(kind, lifecycle, common, attempt, success, problem string, now time.Time) *model.PRStatus {
	if kind != model.CardPR {
		return nil
	}
	p := &model.PRStatus{Lifecycle: lifecycle, CommonStatus: common, LastAttemptAt: model.TimeString(attempt), LastSuccessAt: model.TimeString(success), Unavailable: attempt != "" && success == ""}
	if success != "" {
		at, err := time.Parse(time.RFC3339Nano, success)
		p.Stale = problem != "" || err != nil || now.Sub(at) > 10*time.Minute
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
