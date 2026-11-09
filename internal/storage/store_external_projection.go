package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/model"
)

type ThreadTelegramProjection struct {
	SessionID, ThreadID, State, Error, AttemptID, Title string
	TopicID, Revision                                   int64
}

func (p ThreadTelegramProjection) Retryable() bool { return p.State == "error" || p.State == "unknown" }

func (s *Store) ThreadTelegramProjection(ctx context.Context, session, thread string) (ThreadTelegramProjection, error) {
	if err := s.NormalizeExternalTopicAliases(ctx, thread); err != nil {
		return ThreadTelegramProjection{}, err
	}
	return threadTelegramProjection(ctx, s.db, session, thread)
}

type projectionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func threadTelegramProjection(ctx context.Context, q projectionReader, session, thread string) (ThreadTelegramProjection, error) {
	p := ThreadTelegramProjection{SessionID: session, ThreadID: thread, State: "pending"}
	var active int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM sync_state WHERE id=1 AND session_id=? AND state='active'`, session).Scan(&active); err != nil {
		return p, err
	}
	if active == 0 {
		p.State = "off"
		return p, nil
	}
	if thread == "" {
		return p, nil
	}
	err := q.QueryRowContext(ctx, `SELECT topic_id,delivery_error,delivery_revision FROM sync_topics WHERE session_id=? AND thread_id=? AND telegram_state='connected' LIMIT 1`, session, thread).Scan(&p.TopicID, &p.Error, &p.Revision)
	if err == nil {
		p.State = "connected"
		if p.Error != "" {
			p.State = "error"
		}
		return p, nil
	}
	if err != sql.ErrNoRows {
		return p, err
	}
	var state string
	err = q.QueryRowContext(ctx, `SELECT id,title,state,topic_id FROM sync_topic_creations c WHERE session_id=? AND target=? AND NOT EXISTS(SELECT 1 FROM sync_topic_creations child WHERE child.supersedes_id=c.id) ORDER BY rowid DESC LIMIT 1`, session, "thread:"+thread).Scan(&p.AttemptID, &p.Title, &state, &p.TopicID)
	if err == sql.ErrNoRows {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	switch state {
	case "failed":
		p.State = "error"
		p.Error = "Telegram topic creation failed"
	case "unknown":
		p.State = "unknown"
		p.Error = "Telegram topic creation outcome is unknown; Retry may leave an extra topic"
	}
	return p, nil
}

// Both a binding and the creation head are checked while consuming the route.
// A claimed successor is an IO intent, never permission to repeat after restart.
func (s *Store) ClaimTelegramProjectionRetry(ctx context.Context, token, newID string, expected ThreadTelegramProjection) (*TopicCreation, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer rollback(tx)
	var active int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_state WHERE id=1 AND session_id=? AND state='active'`, expected.SessionID).Scan(&active)
	if err != nil || active != 1 {
		return nil, false, err
	}
	if expected.TopicID > 0 && expected.AttemptID == "" {
		r, e := tx.ExecContext(ctx, `UPDATE sync_topics SET delivery_request_id=(SELECT request_id FROM callback_routes WHERE route_token=?),delivery_error='',delivery_revision=delivery_revision+1 WHERE session_id=? AND topic_id=? AND thread_id=? AND delivery_revision=? AND delivery_error<>'' AND telegram_state='connected'`, token, expected.SessionID, expected.TopicID, expected.ThreadID, expected.Revision)
		if e != nil {
			return nil, false, e
		}
		n, _ := r.RowsAffected()
		if n != 1 {
			return nil, false, nil
		}
		r, e = tx.ExecContext(ctx, `UPDATE callback_routes SET status='expired' WHERE route_token=? AND status='active' AND action='external_launch_telegram_retry'`, token)
		if e != nil {
			return nil, false, e
		}
		n, _ = r.RowsAffected()
		if n != 1 {
			return nil, false, nil
		}
		if _, err = tx.ExecContext(ctx, `UPDATE external_launch_requests SET telegram_priority_version=telegram_priority_version+1 WHERE id=(SELECT request_id FROM callback_routes WHERE route_token=?)`, token); err != nil {
			return nil, false, err
		}
		return nil, true, tx.Commit()
	}
	var payload string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM sync_topic_creations c WHERE id=? AND session_id=? AND target=? AND state IN ('failed','unknown') AND topic_id=0 AND NOT EXISTS(SELECT 1 FROM sync_topic_creations child WHERE child.supersedes_id=c.id) AND NOT EXISTS(SELECT 1 FROM sync_topics t WHERE t.session_id=c.session_id AND t.thread_id=? AND t.telegram_state='connected')`, expected.AttemptID, expected.SessionID, "thread:"+expected.ThreadID, expected.ThreadID).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var op TopicCreation
	if err = json.Unmarshal([]byte(payload), &op); err != nil {
		return nil, false, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(request_id,'') FROM callback_routes WHERE route_token=?`, token).Scan(&op.ManualRequestID); err != nil {
		return nil, false, err
	}
	op.ID = newID
	op.SupersedesID = expected.AttemptID
	op.TopicID = 0
	data, err := json.Marshal(op)
	if err != nil {
		return nil, false, err
	}
	r, err := tx.ExecContext(ctx, `UPDATE callback_routes SET status='expired' WHERE route_token=? AND status='active' AND action='external_launch_telegram_retry'`, token)
	if err != nil {
		return nil, false, err
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return nil, false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sync_topic_creations(id,session_id,chat_id,target,title,payload,state,topic_id,supersedes_id) VALUES(?,?,?,?,?,?,'sending',0,?)`, op.ID, op.SessionID, op.ChatID, op.Target, op.Title, string(data), op.SupersedesID)
	if err != nil {
		return nil, false, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE external_launch_requests SET telegram_priority_version=telegram_priority_version+1 WHERE id=(SELECT request_id FROM callback_routes WHERE route_token=?)`, token); err != nil {
		return nil, false, err
	}
	return &op, true, tx.Commit()
}

// Completion consumes the manual marker only for the delivery revision that
// claimed it. A late old worker cannot acknowledge a newer recovery action.
func (s *Store) SetSyncDeliveryError(ctx context.Context, session string, topic int64, revision int64, message string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var request string
	err = tx.QueryRowContext(ctx, `SELECT delivery_request_id FROM sync_topics WHERE session_id=? AND topic_id=? AND delivery_revision=? AND telegram_state='connected'`, session, topic, revision).Scan(&request)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	r, err := tx.ExecContext(ctx, `UPDATE sync_topics SET delivery_error=?,delivery_revision=delivery_revision+1,delivery_request_id='' WHERE session_id=? AND topic_id=? AND delivery_revision=? AND (delivery_error<>? OR delivery_request_id<>'') AND telegram_state='connected'`, message, session, topic, revision, message)
	if err != nil {
		return err
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE external_launch_requests SET telegram_rendered_fp='',telegram_priority_version=telegram_priority_version+CASE WHEN id=? THEN 1 ELSE 0 END WHERE thread_id IN (SELECT thread_id FROM sync_topics WHERE session_id=? AND topic_id=?)`, request, session, topic)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SyncDeliveryRevision(ctx context.Context, session string, topic int64) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT delivery_revision FROM sync_topics WHERE session_id=? AND topic_id=?`, session, topic).Scan(&n)
	return n, err
}
func (s *Store) MarkExternalTelegramFingerprint(ctx context.Context, id, fp string, expected ThreadTelegramProjection, priorityVersion int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	current, err := threadTelegramProjection(ctx, tx, expected.SessionID, expected.ThreadID)
	if err != nil {
		return err
	}
	if current != expected {
		return nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE external_launch_requests SET telegram_rendered_fp=?,telegram_rendered_priority_version=? WHERE id=? AND telegram_priority_version=?`, fp, priorityVersion, id, priorityVersion)
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) InvalidateExternalProjection(ctx context.Context, thread string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET telegram_rendered_fp='' WHERE thread_id=?`, thread)
	return err
}

// Upgrade an old request-owned draft to the now-known authoritative chat.
// The original identity and all unknown outcomes remain in the audit payload.
func (s *Store) LinkExternalTopicHistory(ctx context.Context, requestID, thread string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, `SELECT id,payload,state,topic_id FROM sync_topic_creations WHERE target=? OR substr(target,1,?)=?`, "external:"+requestID, len("external:"+requestID+":"), "external:"+requestID+":")
	if err != nil {
		return err
	}
	type record struct {
		op    TopicCreation
		state string
	}
	var records []record
	for rows.Next() {
		var id, payload, state string
		var topic int64
		if err = rows.Scan(&id, &payload, &state, &topic); err != nil {
			rows.Close()
			return err
		}
		var op TopicCreation
		if err = json.Unmarshal([]byte(payload), &op); err != nil {
			rows.Close()
			return err
		}
		op.ID = id
		op.TopicID = topic
		op.LegacyTarget = op.Target
		op.Target = "thread:" + thread
		op.ThreadID = thread
		op.Draft = nil
		records = append(records, record{op, state})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, r := range records {
		op := r.op
		data, _ := json.Marshal(op)
		if _, err = tx.ExecContext(ctx, `UPDATE sync_topic_creations SET target=?,payload=? WHERE id=?`, op.Target, string(data), op.ID); err != nil {
			return err
		}
		if r.state == "confirmed" && op.TopicID > 0 {
			_, err = tx.ExecContext(ctx, `INSERT INTO sync_topics(session_id,chat_id,topic_id,thread_id,rank,title,telegram_state,created_at,updated_at) SELECT ?,?,?,?,?,?,'connected',?,? WHERE EXISTS(SELECT 1 FROM sync_state WHERE id=1 AND session_id=? AND state='active') AND NOT EXISTS(SELECT 1 FROM sync_topics WHERE session_id=? AND thread_id=? AND telegram_state='connected') ON CONFLICT(session_id,topic_id) DO NOTHING`, op.SessionID, op.ChatID, op.TopicID, thread, op.Rank, op.Title, model.NowString(), model.NowString(), op.SessionID, op.SessionID, thread)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM sync_topic_drafts WHERE session_id=? AND topic_id=? AND state IN ('external','ready')`, op.SessionID, op.TopicID); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (s *Store) AttachExternalTopicOwner(ctx context.Context, session, thread, turn, state string, generation uint64) error {
	if thread == "" {
		return errors.New("thread required")
	}
	r, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET active_turn_id=?,active_turn_state=?,writer_generation=? WHERE session_id=? AND thread_id=? AND telegram_state='connected' AND (writer_generation=0 OR writer_generation=?)`, turn, state, generation, session, thread, generation)
	if err != nil {
		return fmt.Errorf("attach external projection: %w", err)
	}
	_, err = r.RowsAffected()
	return err
}

func (s *Store) NormalizeExternalTopicAliases(ctx context.Context, thread string) error {
	if thread == "" {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT r.id FROM external_launch_requests r JOIN sync_topic_creations c ON c.target='external:'||r.id OR substr(c.target,1,length('external:'||r.id||':'))='external:'||r.id||':' WHERE r.thread_id=?`, thread)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = s.LinkExternalTopicHistory(ctx, id, thread); err != nil {
			return err
		}
	}
	return nil
}

// Attach transport metadata without resurrecting an already resolved route.
func (s *Store) BindActiveCallbackMessage(ctx context.Context, token string, messageID int64) (bool, error) {
	r, err := s.db.ExecContext(ctx, `UPDATE callback_routes SET telegram_message_id=? WHERE route_token=? AND status='active'`, messageID, token)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n == 1, err
}

func (s *Store) TerminalRequestsNeedingTelegram(ctx context.Context, limit int) ([]model.ExternalLaunchRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+externalLaunchRequestColumns+` FROM external_launch_requests r WHERE status IN ('session_completed','session_failed','session_interrupted') AND coalesce(thread_id,'')<>'' AND EXISTS(
 SELECT 1 FROM sync_state st WHERE st.id=1 AND st.state='active' AND (
 EXISTS(SELECT 1 FROM sync_topics t WHERE t.session_id=st.session_id AND t.thread_id=r.thread_id AND t.telegram_state='connected' AND t.delivery_error<>'') OR (
 NOT EXISTS(SELECT 1 FROM sync_topics t WHERE t.session_id=st.session_id AND t.thread_id=r.thread_id AND t.telegram_state='connected') AND
 EXISTS(SELECT 1 FROM sync_topic_creations c WHERE c.session_id=st.session_id AND c.target='thread:'||r.thread_id AND c.state IN ('failed','unknown') AND NOT EXISTS(SELECT 1 FROM sync_topic_creations child WHERE child.supersedes_id=c.id))))) ORDER BY created_at,id LIMIT ?`, limit)
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
