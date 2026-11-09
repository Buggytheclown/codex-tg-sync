package storage

import (
	"context"
	"errors"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// Only this journal, not a Telegram message, is evidence of an external RPC.
func (s *Store) SetExternalDispatchPhase(ctx context.Context, id, from, to string) error {
	if to == model.ExternalDispatchTurnSending {
		r, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET status='dismissed',error_type='task_cancelled',telegram_rendered_status='',telegram_rendered_fp='' WHERE id=? AND status='starting' AND dispatch_phase='thread_ready' AND error_type='task_cancel_pending'`, id)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n != 0 {
			return errors.New("first turn cancelled after thread creation")
		}
	}
	r, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET dispatch_phase=?,updated_at=? WHERE id=? AND status='starting' AND dispatch_phase=?`, to, model.NowString(), id, from)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("external dispatch phase changed")
	}
	return nil
}

func (s *Store) RecordExternalThread(ctx context.Context, id, threadID string) error {
	if threadID == "" {
		return errors.New("external thread id is required")
	}
	r, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET thread_id=?,dispatch_phase='thread_ready',updated_at=? WHERE id=? AND status='starting' AND dispatch_phase IN ('thread_sending','not_sent','thread_ready') AND (coalesce(thread_id,'')='' OR thread_id=?)`, threadID, model.NowString(), id, threadID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("external thread confirmation changed")
	}
	if err = s.LinkExternalTopicHistory(ctx, id, threadID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = bindExternalTaskThreadTx(ctx, tx, id, threadID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordExternalTurn(ctx context.Context, id, threadID, turnID string) error {
	if threadID == "" || turnID == "" {
		return errors.New("external turn identity is required")
	}
	r, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET thread_id=?,turn_id=?,dispatch_phase='turn_confirmed',updated_at=? WHERE id=? AND status='starting' AND dispatch_phase='turn_sending' AND thread_id=?`, threadID, turnID, model.NowString(), id, threadID)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return errors.New("external turn confirmation changed")
	}
	return nil
}

func (s *Store) ListExternalExecutionThreadIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT thread_id FROM external_launch_requests WHERE coalesce(thread_id,'')<>'' AND status IN ('starting','session_started','outcome_unknown')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) migrateExternalDispatch(ctx context.Context) error {
	// Known pre-RPC legacy branches only. Null ids alone are not evidence.
	_, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET dispatch_phase='not_sent',status='failed',telegram_rendered_status='',telegram_rendered_fp='' WHERE dispatch_phase='' AND status IN ('failed','outcome_unknown') AND error_type IN ('telegram_topic','telegram_prompt') AND coalesce(thread_id,'')='' AND coalesce(turn_id,'')='' AND NOT EXISTS(SELECT 1 FROM daemon_state d WHERE d.key='external.launch.receipt.'||external_launch_requests.id)`)
	return err
}

func (s *Store) RecoverExternalDispatchPhases(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO daemon_state(key,value,updated_at) SELECT 'turn_origin.telegram.'||thread_id||'.'||turn_id,'telegram',? FROM external_launch_requests WHERE dispatch_phase='turn_confirmed' AND coalesce(thread_id,'')<>'' AND coalesce(turn_id,'')<>'' ON CONFLICT(key) DO NOTHING`, model.NowString()); err != nil {
		return err
	}
	// Recovery is local only. Interrupted sends never receive a new RPC.
	_, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET status=CASE WHEN dispatch_phase IN ('not_sent','thread_ready') THEN 'failed' ELSE 'outcome_unknown' END,error_type='daemon_restart',error_summary='Daemon restart interrupted dispatch; use the available recovery action.',telegram_rendered_status='',telegram_rendered_fp='' WHERE status='starting' AND dispatch_phase<>'turn_confirmed'`)
	if err != nil {
		return err
	}
	reqs, err := s.ListExternalLaunchRequestsByStatus(ctx, []string{model.ExternalLaunchStarting}, 1000)
	if err != nil {
		return err
	}
	for _, r := range reqs {
		if r.DispatchPhase == model.ExternalDispatchTurnConfirmed {
			if _, err = s.CompleteExternalLaunchRequest(ctx, r.ID, model.ExternalLaunchSessionStarted, r.ThreadID, r.TurnID, "", ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) ExternalExecutionsForThread(ctx context.Context, thread string) ([]model.ExternalLaunchRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+externalLaunchRequestColumns+` FROM external_launch_requests WHERE thread_id=? AND status IN ('starting','session_started','outcome_unknown')`, thread)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ExternalLaunchRequest
	for rows.Next() {
		var r model.ExternalLaunchRequest
		if err = scanExternalLaunchRequest(rows, &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) HasExternalLaunchThread(ctx context.Context, thread string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM external_launch_requests WHERE thread_id=?)`, thread).Scan(&n)
	return n != 0, err
}
