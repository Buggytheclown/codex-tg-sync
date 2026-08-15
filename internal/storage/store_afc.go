package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Store) GetAFCState(ctx context.Context) (model.AFCState, error) {
	var state model.AFCState
	var snapshotAt, endedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session_id, chat_id, state, security_state, snapshot_at,
		activation_summary_json, created_at, ended_at FROM afc_state WHERE id = 1`).Scan(
		&state.SessionID, &state.ChatID, &state.State, &state.SecurityState, &snapshotAt,
		&state.ActivationSummaryJSON, &state.CreatedAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AFCState{State: model.AFCStateOff, SecurityState: model.AFCSecurityUnknown}, nil
	}
	state.SnapshotAt = model.TimeString(snapshotAt.String)
	state.EndedAt = model.TimeString(endedAt.String)
	return state, err
}

func (s *Store) BeginAFCActivation(ctx context.Context, sessionID string, chatID int64) error {
	now := string(model.NowString())
	_, err := s.db.ExecContext(ctx, `INSERT INTO afc_state(id, session_id, chat_id, state, security_state, snapshot_at,
		activation_summary_json, created_at, ended_at) VALUES (1, ?, ?, ?, ?, ?, '', ?, NULL)
		ON CONFLICT(id) DO UPDATE SET session_id=excluded.session_id, chat_id=excluded.chat_id,
		state=excluded.state, security_state=excluded.security_state, snapshot_at=excluded.snapshot_at,
		activation_summary_json='', created_at=excluded.created_at, ended_at=NULL`,
		sessionID, chatID, model.AFCStateActivating, model.AFCSecurityValid, now, now)
	return err
}

// RecoverAFCState converts an interrupted activation into a logically-off
// session whose already-created topics are cleanup-only. Active sessions stay
// active and are resumed by passive polling; no writer is started here.
func (s *Store) RecoverAFCState(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var sessionID, state string
	err = tx.QueryRowContext(ctx, `SELECT session_id, state FROM afc_state WHERE id=1`).Scan(&sessionID, &state)
	if errors.Is(err, sql.ErrNoRows) || state != model.AFCStateActivating {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	now := string(model.NowString())
	if _, err := tx.ExecContext(ctx, `UPDATE afc_state SET state=?, ended_at=? WHERE id=1`, model.AFCStateOff, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE afc_topics SET telegram_state=?, updated_at=? WHERE session_id=?`, model.AFCTopicCleanup, now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpsertAFCTopic(ctx context.Context, topic model.AFCTopic) error {
	now := string(model.NowString())
	if strings.TrimSpace(topic.TelegramState) == "" {
		topic.TelegramState = model.AFCTopicConnected
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO afc_topics(session_id, chat_id, topic_id, thread_id, rank, title,
		telegram_state, status_message_id, last_render_fp, last_final_fp, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, topic_id) DO UPDATE SET thread_id=excluded.thread_id, rank=excluded.rank,
		title=excluded.title, telegram_state=excluded.telegram_state, status_message_id=excluded.status_message_id,
		last_render_fp=excluded.last_render_fp, last_final_fp=excluded.last_final_fp, updated_at=excluded.updated_at`,
		topic.SessionID, topic.ChatID, topic.TopicID, topic.ThreadID, topic.Rank, topic.Title,
		topic.TelegramState, topic.StatusMessageID, nullable(topic.LastRenderFP), nullable(topic.LastFinalFP), now, now)
	return err
}

func (s *Store) FinishAFCActivation(ctx context.Context, sessionID, summaryJSON string, active bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	state := model.AFCStateOff
	ended := any(string(model.NowString()))
	if active {
		state, ended = model.AFCStateActive, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE afc_state SET state=?, activation_summary_json=?, ended_at=?
		WHERE id=1 AND session_id=? AND state=?`, state, summaryJSON, ended, sessionID, model.AFCStateActivating)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("afc activation generation is stale")
	}
	if active {
		now := string(model.NowString())
		if _, err := tx.ExecContext(ctx, `INSERT INTO daemon_state(key,value,updated_at) VALUES ('observer.global_enabled','false',?)
			ON CONFLICT(key) DO UPDATE SET value='false', updated_at=excluded.updated_at`, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListAFCTopics(ctx context.Context, sessionID string) ([]model.AFCTopic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id, chat_id, topic_id, thread_id, rank, title, telegram_state,
		status_message_id, coalesce(last_render_fp,''), coalesce(last_final_fp,''), created_at, updated_at
		FROM afc_topics WHERE session_id=? ORDER BY rank, thread_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AFCTopic
	for rows.Next() {
		var topic model.AFCTopic
		if err := rows.Scan(&topic.SessionID, &topic.ChatID, &topic.TopicID, &topic.ThreadID, &topic.Rank,
			&topic.Title, &topic.TelegramState, &topic.StatusMessageID, &topic.LastRenderFP, &topic.LastFinalFP,
			&topic.CreatedAt, &topic.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, topic)
	}
	return out, rows.Err()
}

func (s *Store) GetActiveAFCTopic(ctx context.Context, chatID, topicID int64) (*model.AFCTopic, error) {
	state, err := s.GetAFCState(ctx)
	if err != nil || state.State != model.AFCStateActive || state.ChatID != chatID {
		return nil, err
	}
	topics, err := s.ListAFCTopics(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	for _, topic := range topics {
		if topic.TopicID == topicID && topic.TelegramState == model.AFCTopicConnected {
			copy := topic
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *Store) MarkAFCOff(ctx context.Context, sessionID string) ([]model.AFCTopic, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	now := string(model.NowString())
	if _, err := tx.ExecContext(ctx, `UPDATE afc_state SET state=?, ended_at=? WHERE id=1 AND session_id=?`, model.AFCStateOff, now, sessionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE afc_topics SET telegram_state=?, updated_at=? WHERE session_id=? AND telegram_state=?`, model.AFCTopicCleanup, now, sessionID, model.AFCTopicConnected); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListAFCTopics(ctx, sessionID)
}

func (s *Store) UpdateAFCTopicDelivery(ctx context.Context, sessionID string, topicID, statusMessageID int64, renderFP, finalFP string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE afc_topics SET status_message_id=?, last_render_fp=?, last_final_fp=?, updated_at=? WHERE session_id=? AND topic_id=?`,
		statusMessageID, nullable(renderFP), nullable(finalFP), string(model.NowString()), sessionID, topicID)
	return err
}

func (s *Store) DeleteAFCTopic(ctx context.Context, sessionID string, topicID int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM afc_topics WHERE session_id=? AND topic_id=?`, sessionID, topicID)
	return err
}
