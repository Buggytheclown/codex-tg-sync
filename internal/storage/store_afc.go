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

func (s *Store) RecoverAFCWriterState(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	if _, err := tx.ExecContext(ctx, `UPDATE afc_message_receipts SET state=?, updated_at=?
		WHERE state=? AND session_id=(SELECT session_id FROM afc_state WHERE id=1 AND state=?)`,
		model.AFCReceiptUnknown, now, model.AFCReceiptAccepted, model.AFCStateActive); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE afc_topics SET active_turn_state=?, updated_at=?
		WHERE session_id=(SELECT session_id FROM afc_state WHERE id=1 AND state=?) AND active_turn_state IN (?,?)`,
		model.AFCTurnUnknown, now, model.AFCStateActive, model.AFCTurnStarting, model.AFCTurnActive); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE afc_topic_drafts SET state=?, updated_at=?
		WHERE session_id=(SELECT session_id FROM afc_state WHERE id=1 AND state=?) AND state=?`,
		model.AFCDraftUnknown, now, model.AFCStateActive, model.AFCDraftStarting); err != nil {
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
		telegram_state, status_message_id, status_turn_id, last_render_fp, last_final_fp, active_turn_id, active_turn_state,
		writer_generation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, topic_id) DO UPDATE SET thread_id=excluded.thread_id, rank=excluded.rank,
		title=excluded.title, telegram_state=excluded.telegram_state, status_message_id=excluded.status_message_id,
		status_turn_id=excluded.status_turn_id, last_render_fp=excluded.last_render_fp,
		last_final_fp=excluded.last_final_fp, updated_at=excluded.updated_at`,
		topic.SessionID, topic.ChatID, topic.TopicID, topic.ThreadID, topic.Rank, topic.Title,
		topic.TelegramState, topic.StatusMessageID, nullable(topic.StatusTurnID), nullable(topic.LastRenderFP), nullable(topic.LastFinalFP),
		nullable(topic.ActiveTurnID), nullable(topic.ActiveTurnState), topic.WriterGeneration, now, now)
	return err
}

func (s *Store) CreateAFCTopicDraft(ctx context.Context, draft model.AFCTopicDraft) error {
	now := model.NowString()
	result, err := s.db.ExecContext(ctx, `INSERT INTO afc_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,
		project_name,directory_name,state,source_message_id,created_at,updated_at)
		SELECT ?,?,?,?,?,?,?,?,?,0,?,? WHERE EXISTS (
			SELECT 1 FROM afc_state WHERE id=1 AND session_id=? AND chat_id=? AND state=?
		)`, draft.SessionID, draft.ChatID, draft.TopicID, draft.Rank, draft.Title, draft.CWD,
		nullable(draft.ProjectName), nullable(draft.DirectoryName), model.AFCDraftReady, now, now,
		draft.SessionID, draft.ChatID, model.AFCStateActive)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC draft session is stale")
	}
	return nil
}

func (s *Store) ListAFCTopicDrafts(ctx context.Context, sessionID string) ([]model.AFCTopicDraft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,chat_id,topic_id,rank,title,cwd,coalesce(project_name,''),
		coalesce(directory_name,''),state,source_message_id,created_at,updated_at
		FROM afc_topic_drafts WHERE session_id=? ORDER BY rank,topic_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drafts []model.AFCTopicDraft
	for rows.Next() {
		var draft model.AFCTopicDraft
		if err := rows.Scan(&draft.SessionID, &draft.ChatID, &draft.TopicID, &draft.Rank, &draft.Title, &draft.CWD,
			&draft.ProjectName, &draft.DirectoryName, &draft.State, &draft.SourceMessageID, &draft.CreatedAt, &draft.UpdatedAt); err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	return drafts, rows.Err()
}

func (s *Store) GetActiveAFCTopicDraft(ctx context.Context, chatID, topicID int64) (*model.AFCTopicDraft, error) {
	var draft model.AFCTopicDraft
	err := s.db.QueryRowContext(ctx, `SELECT d.session_id,d.chat_id,d.topic_id,d.rank,d.title,d.cwd,
		coalesce(d.project_name,''),coalesce(d.directory_name,''),d.state,d.source_message_id,d.created_at,d.updated_at
		FROM afc_topic_drafts d JOIN afc_state s ON s.session_id=d.session_id
		WHERE s.id=1 AND s.state=? AND s.chat_id=? AND d.topic_id=?`, model.AFCStateActive, chatID, topicID).Scan(
		&draft.SessionID, &draft.ChatID, &draft.TopicID, &draft.Rank, &draft.Title, &draft.CWD,
		&draft.ProjectName, &draft.DirectoryName, &draft.State, &draft.SourceMessageID, &draft.CreatedAt, &draft.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &draft, err
}

func (s *Store) ClaimAFCTopicDraftMessage(ctx context.Context, chatID, topicID, messageID int64) (model.AFCTopicDraft, model.AFCMessageReceipt, bool, error) {
	if messageID <= 0 {
		return model.AFCTopicDraft{}, model.AFCMessageReceipt{}, false, errors.New("telegram source message id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AFCTopicDraft{}, model.AFCMessageReceipt{}, false, err
	}
	defer rollback(tx)
	var existing model.AFCMessageReceipt
	err = tx.QueryRowContext(ctx, `SELECT chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at
		FROM afc_message_receipts WHERE chat_id=? AND topic_id=? AND message_id=?`, chatID, topicID, messageID).Scan(
		&existing.ChatID, &existing.TopicID, &existing.MessageID, &existing.SessionID, &existing.ThreadID,
		&existing.State, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		return model.AFCTopicDraft{}, existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.AFCTopicDraft{}, model.AFCMessageReceipt{}, false, err
	}
	var draft model.AFCTopicDraft
	err = tx.QueryRowContext(ctx, `SELECT d.session_id,d.chat_id,d.topic_id,d.rank,d.title,d.cwd,
		coalesce(d.project_name,''),coalesce(d.directory_name,''),d.state,d.source_message_id,d.created_at,d.updated_at
		FROM afc_topic_drafts d JOIN afc_state s ON s.session_id=d.session_id
		WHERE s.id=1 AND s.state=? AND s.chat_id=? AND d.topic_id=?`, model.AFCStateActive, chatID, topicID).Scan(
		&draft.SessionID, &draft.ChatID, &draft.TopicID, &draft.Rank, &draft.Title, &draft.CWD,
		&draft.ProjectName, &draft.DirectoryName, &draft.State, &draft.SourceMessageID, &draft.CreatedAt, &draft.UpdatedAt)
	if err != nil {
		return model.AFCTopicDraft{}, model.AFCMessageReceipt{}, false, err
	}
	if draft.State != model.AFCDraftReady {
		return draft, model.AFCMessageReceipt{}, false, errors.New("AFC draft already has unfinished work")
	}
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `UPDATE afc_topic_drafts SET state=?,source_message_id=?,updated_at=?
		WHERE session_id=? AND topic_id=? AND state=? AND source_message_id=0`, model.AFCDraftStarting, messageID, now,
		draft.SessionID, draft.TopicID, model.AFCDraftReady)
	if err != nil {
		return model.AFCTopicDraft{}, model.AFCMessageReceipt{}, false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return draft, model.AFCMessageReceipt{}, false, errors.New("AFC draft claim is stale")
	}
	receipt := model.AFCMessageReceipt{ChatID: chatID, TopicID: topicID, MessageID: messageID,
		SessionID: draft.SessionID, State: model.AFCReceiptAccepted, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO afc_message_receipts(chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, "",
		receipt.State, receipt.CreatedAt, receipt.UpdatedAt); err != nil {
		return model.AFCTopicDraft{}, model.AFCMessageReceipt{}, false, err
	}
	draft.State, draft.SourceMessageID, draft.UpdatedAt = model.AFCDraftStarting, messageID, now
	if err := tx.Commit(); err != nil {
		return model.AFCTopicDraft{}, model.AFCMessageReceipt{}, false, err
	}
	return draft, receipt, true, nil
}

func (s *Store) ResetAFCTopicDraftMessage(ctx context.Context, draft model.AFCTopicDraft, receipt model.AFCMessageReceipt) error {
	return s.finishAFCTopicDraftMessage(ctx, draft, receipt, model.AFCDraftReady, model.AFCReceiptRejected, 0)
}

func (s *Store) MarkAFCTopicDraftUnknown(ctx context.Context, draft model.AFCTopicDraft, receipt model.AFCMessageReceipt) error {
	return s.finishAFCTopicDraftMessage(ctx, draft, receipt, model.AFCDraftUnknown, model.AFCReceiptUnknown, receipt.MessageID)
}

func (s *Store) finishAFCTopicDraftMessage(ctx context.Context, draft model.AFCTopicDraft, receipt model.AFCMessageReceipt, draftState, receiptState string, sourceMessageID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `UPDATE afc_message_receipts SET state=?,updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, receiptState, now,
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.AFCReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC draft receipt transition is stale")
	}
	result, err = tx.ExecContext(ctx, `UPDATE afc_topic_drafts SET state=?,source_message_id=?,updated_at=?
		WHERE session_id=? AND topic_id=? AND state=? AND source_message_id=?`, draftState, sourceMessageID, now,
		draft.SessionID, draft.TopicID, model.AFCDraftStarting, receipt.MessageID)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC draft transition is stale")
	}
	return tx.Commit()
}

func (s *Store) MaterializeAFCTopicDraft(ctx context.Context, draft model.AFCTopicDraft, receipt model.AFCMessageReceipt, threadID, title string, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `INSERT INTO afc_topics(session_id,chat_id,topic_id,thread_id,rank,title,telegram_state,
		active_turn_state,writer_generation,created_at,updated_at)
		SELECT session_id,chat_id,topic_id,?,rank,?,?,?, ?,created_at,?
		FROM afc_topic_drafts WHERE session_id=? AND topic_id=? AND state=? AND source_message_id=?`,
		threadID, title, model.AFCTopicConnected, model.AFCTurnStarting, generation, now,
		draft.SessionID, draft.TopicID, model.AFCDraftStarting, receipt.MessageID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC draft materialization is stale")
	}
	result, err = tx.ExecContext(ctx, `UPDATE afc_message_receipts SET thread_id=?,updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=? AND thread_id=''`,
		threadID, now, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.AFCReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC draft receipt materialization is stale")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM afc_topic_drafts WHERE session_id=? AND topic_id=?`, draft.SessionID, draft.TopicID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DematerializeAFCTopicDraft(ctx context.Context, draft model.AFCTopicDraft, receipt model.AFCMessageReceipt, threadID, title string, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `INSERT INTO afc_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,
		project_name,directory_name,state,source_message_id,created_at,updated_at)
		SELECT session_id,chat_id,topic_id,rank,?,?,?,?,?,0,created_at,?
		FROM afc_topics WHERE session_id=? AND topic_id=? AND thread_id=? AND active_turn_state=? AND writer_generation=?`,
		title, draft.CWD, nullable(draft.ProjectName), nullable(draft.DirectoryName), model.AFCDraftReady, now,
		draft.SessionID, draft.TopicID, threadID, model.AFCTurnStarting, generation)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC draft dematerialization is stale")
	}
	result, err = tx.ExecContext(ctx, `UPDATE afc_message_receipts SET state=?,updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND thread_id=? AND state=?`,
		model.AFCReceiptRejected, now, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, threadID, model.AFCReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC draft receipt dematerialization is stale")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM afc_topics WHERE session_id=? AND topic_id=? AND thread_id=?`,
		draft.SessionID, draft.TopicID, threadID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConvertEmptyAFCTopicToDraft(ctx context.Context, topic model.AFCTopic, draft model.AFCTopicDraft, receipt model.AFCMessageReceipt, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `INSERT INTO afc_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,
		project_name,directory_name,state,source_message_id,created_at,updated_at)
		SELECT session_id,chat_id,topic_id,rank,?,?,?,?,?, ?,created_at,?
		FROM afc_topics WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=?
		AND status_message_id=0 AND coalesce(status_turn_id,'')='' AND coalesce(last_render_fp,'')=''
		AND coalesce(last_final_fp,'')='' AND coalesce(active_turn_id,'')='' AND active_turn_state=? AND writer_generation=?`,
		draft.Title, draft.CWD, nullable(draft.ProjectName), nullable(draft.DirectoryName), model.AFCDraftStarting,
		receipt.MessageID, now, topic.SessionID, topic.TopicID, topic.ThreadID, model.AFCTopicConnected,
		model.AFCTurnStarting, generation)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC empty topic is not eligible for draft conversion")
	}
	result, err = tx.ExecContext(ctx, `UPDATE afc_message_receipts SET thread_id='',updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND thread_id=? AND state=?`,
		now, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, topic.ThreadID, model.AFCReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC empty topic receipt conversion is stale")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM afc_topics WHERE session_id=? AND topic_id=? AND thread_id=?`,
		topic.SessionID, topic.TopicID, topic.ThreadID); err != nil {
		return err
	}
	return tx.Commit()
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
		status_message_id, coalesce(status_turn_id,''), coalesce(last_render_fp,''), coalesce(last_final_fp,''), coalesce(active_turn_id,''),
		coalesce(active_turn_state,''), writer_generation, created_at, updated_at
		FROM afc_topics WHERE session_id=? ORDER BY rank, thread_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AFCTopic
	for rows.Next() {
		var topic model.AFCTopic
		if err := rows.Scan(&topic.SessionID, &topic.ChatID, &topic.TopicID, &topic.ThreadID, &topic.Rank,
			&topic.Title, &topic.TelegramState, &topic.StatusMessageID, &topic.StatusTurnID, &topic.LastRenderFP, &topic.LastFinalFP,
			&topic.ActiveTurnID, &topic.ActiveTurnState, &topic.WriterGeneration, &topic.CreatedAt, &topic.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, topic)
	}
	return out, rows.Err()
}

func (s *Store) GetActiveAFCTopicByThread(ctx context.Context, sessionID, threadID string) (*model.AFCTopic, error) {
	topics, err := s.ListAFCTopics(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, topic := range topics {
		if topic.ThreadID == threadID && topic.TelegramState == model.AFCTopicConnected {
			copy := topic
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *Store) AcceptAFCMessage(ctx context.Context, chatID, topicID, messageID int64) (model.AFCMessageReceipt, bool, error) {
	if messageID <= 0 {
		return model.AFCMessageReceipt{}, false, errors.New("telegram source message id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AFCMessageReceipt{}, false, err
	}
	defer rollback(tx)
	var receipt model.AFCMessageReceipt
	err = tx.QueryRowContext(ctx, `SELECT chat_id, topic_id, message_id, session_id, thread_id, state, created_at, updated_at
		FROM afc_message_receipts WHERE chat_id=? AND topic_id=? AND message_id=?`, chatID, topicID, messageID).Scan(
		&receipt.ChatID, &receipt.TopicID, &receipt.MessageID, &receipt.SessionID, &receipt.ThreadID, &receipt.State, &receipt.CreatedAt, &receipt.UpdatedAt)
	if err == nil {
		return receipt, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.AFCMessageReceipt{}, false, err
	}
	var sessionID, threadID string
	err = tx.QueryRowContext(ctx, `SELECT s.session_id, t.thread_id FROM afc_state s JOIN afc_topics t
		ON t.session_id=s.session_id WHERE s.id=1 AND s.state=? AND s.chat_id=? AND t.topic_id=? AND t.telegram_state=?`,
		model.AFCStateActive, chatID, topicID, model.AFCTopicConnected).Scan(&sessionID, &threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AFCMessageReceipt{}, false, errors.New("AFC topic is not active")
	}
	if err != nil {
		return model.AFCMessageReceipt{}, false, err
	}
	now := model.NowString()
	_, err = tx.ExecContext(ctx, `INSERT INTO afc_message_receipts(chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, chatID, topicID, messageID, sessionID, threadID, model.AFCReceiptAccepted, now, now)
	if err != nil {
		return model.AFCMessageReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.AFCMessageReceipt{}, false, err
	}
	return model.AFCMessageReceipt{ChatID: chatID, TopicID: topicID, MessageID: messageID, SessionID: sessionID, ThreadID: threadID, State: model.AFCReceiptAccepted, CreatedAt: now, UpdatedAt: now}, true, nil
}

func (s *Store) MarkAFCDispatchState(ctx context.Context, receipt model.AFCMessageReceipt, receiptState, turnID, turnState string, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `UPDATE afc_message_receipts SET state=?, updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, receiptState, now,
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.AFCReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC receipt transition is stale")
	}
	result, err = tx.ExecContext(ctx, `UPDATE afc_topics SET active_turn_id=?, active_turn_state=?, writer_generation=?, updated_at=?
		WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=?`, nullable(turnID), nullable(turnState), generation, now,
		receipt.SessionID, receipt.TopicID, receipt.ThreadID, model.AFCTopicConnected)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC topic dispatch transition is stale")
	}
	return tx.Commit()
}

func (s *Store) MarkAFCStarting(ctx context.Context, receipt model.AFCMessageReceipt, generation uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE afc_topics SET active_turn_id=NULL, active_turn_state=?, writer_generation=?, updated_at=?
		WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=?
		AND coalesce(active_turn_state,'') NOT IN (?,?,?)`, model.AFCTurnStarting, generation, model.NowString(),
		receipt.SessionID, receipt.TopicID, receipt.ThreadID, model.AFCTopicConnected,
		model.AFCTurnStarting, model.AFCTurnActive, model.AFCTurnUnknown)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC topic already has unfinished work")
	}
	return nil
}

func (s *Store) ResolveAFCSharedDaemonUnknown(ctx context.Context, sessionID string, topicID int64, threadID string, generation uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE afc_topics SET active_turn_id=NULL, active_turn_state=?, writer_generation=0, updated_at=?
		WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=? AND active_turn_state=? AND writer_generation=?`,
		model.AFCTurnTerminal, model.NowString(), sessionID, topicID, threadID, model.AFCTopicConnected, model.AFCTurnUnknown, generation)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC shared-daemon unknown transition is stale")
	}
	return nil
}

func (s *Store) MarkAFCReceiptState(ctx context.Context, receipt model.AFCMessageReceipt, state string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE afc_message_receipts SET state=?, updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, state, model.NowString(),
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.AFCReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC receipt transition is stale")
	}
	return nil
}

func (s *Store) MarkAFCDispatchFailure(ctx context.Context, receipt model.AFCMessageReceipt, state, turnState string, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	if _, err := tx.ExecContext(ctx, `UPDATE afc_message_receipts SET state=?, updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, state, now,
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.AFCReceiptAccepted); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE afc_topics SET active_turn_state=?, writer_generation=?, updated_at=?
		WHERE session_id=? AND topic_id=? AND thread_id=? AND writer_generation=?`, turnState, generation, now,
		receipt.SessionID, receipt.TopicID, receipt.ThreadID, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkAFCDispatched(ctx context.Context, sessionID string, topicID, messageID int64, turnID string, generation uint64) error {
	receipt, err := s.GetAFCReceipt(ctx, topicID, messageID)
	if err != nil {
		return err
	}
	if receipt == nil || receipt.SessionID != sessionID {
		return errors.New("AFC receipt not found")
	}
	return s.MarkAFCDispatchState(ctx, *receipt, model.AFCReceiptDispatched, turnID, model.AFCTurnActive, generation)
}

func (s *Store) GetAFCReceipt(ctx context.Context, topicID, messageID int64) (*model.AFCMessageReceipt, error) {
	var receipt model.AFCMessageReceipt
	err := s.db.QueryRowContext(ctx, `SELECT chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at
		FROM afc_message_receipts WHERE topic_id=? AND message_id=?`, topicID, messageID).Scan(&receipt.ChatID, &receipt.TopicID, &receipt.MessageID, &receipt.SessionID, &receipt.ThreadID, &receipt.State, &receipt.CreatedAt, &receipt.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &receipt, err
}

func (s *Store) MarkAFCTerminal(ctx context.Context, sessionID, threadID, turnID string, generation uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE afc_topics SET active_turn_state=?, updated_at=?
		WHERE session_id=? AND thread_id=? AND active_turn_id=? AND writer_generation=? AND active_turn_state IN (?,?)`,
		model.AFCTurnTerminal, model.NowString(), sessionID, threadID, turnID, generation, model.AFCTurnActive, model.AFCTurnStarting)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC terminal evidence is stale")
	}
	return nil
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
	if _, err := tx.ExecContext(ctx, `UPDATE afc_topic_drafts SET state=?, updated_at=? WHERE session_id=?`, model.AFCDraftCleanup, now, sessionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListAFCCleanupTargets(ctx, sessionID)
}

func (s *Store) MarkAFCDraining(ctx context.Context, sessionID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE afc_state SET state=? WHERE id=1 AND session_id=? AND state IN (?,?)`,
		model.AFCStateDraining, sessionID, model.AFCStateActive, model.AFCStateDraining)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC session cannot enter draining")
	}
	return nil
}

func (s *Store) UpdateAFCTopicDelivery(ctx context.Context, sessionID string, topicID, statusMessageID int64, statusTurnID, renderFP, finalFP string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE afc_topics SET status_message_id=?, status_turn_id=?, last_render_fp=?, last_final_fp=?, updated_at=? WHERE session_id=? AND topic_id=?`,
		statusMessageID, nullable(statusTurnID), nullable(renderFP), nullable(finalFP), string(model.NowString()), sessionID, topicID)
	return err
}

func (s *Store) ResetAFCTopicStatusDelivery(ctx context.Context, sessionID string, topicID, statusMessageID int64, statusTurnID string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE afc_topics SET status_message_id=0, status_turn_id=NULL, last_render_fp=NULL, updated_at=?
		WHERE session_id=? AND topic_id=? AND telegram_state=? AND status_message_id=? AND coalesce(status_turn_id,'')=?`,
		model.NowString(), sessionID, topicID, model.AFCTopicConnected, statusMessageID, statusTurnID)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (s *Store) UpdateAFCTopicTitle(ctx context.Context, sessionID string, topicID int64, title string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE afc_topics SET title=?,updated_at=?
		WHERE session_id=? AND topic_id=? AND telegram_state=?`, title, model.NowString(), sessionID, topicID, model.AFCTopicConnected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("AFC topic title update is stale")
	}
	return nil
}

func (s *Store) DeleteAFCTopic(ctx context.Context, sessionID string, topicID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `DELETE FROM afc_topics WHERE session_id=? AND topic_id=?`, sessionID, topicID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM afc_topic_drafts WHERE session_id=? AND topic_id=?`, sessionID, topicID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListAFCCleanupTargets(ctx context.Context, sessionID string) ([]model.AFCTopic, error) {
	topics, err := s.ListAFCTopics(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	drafts, err := s.ListAFCTopicDrafts(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, draft := range drafts {
		if draft.State != model.AFCDraftCleanup {
			continue
		}
		topics = append(topics, model.AFCTopic{SessionID: draft.SessionID, ChatID: draft.ChatID, TopicID: draft.TopicID,
			Rank: draft.Rank, Title: draft.Title, TelegramState: model.AFCTopicCleanup, CreatedAt: draft.CreatedAt, UpdatedAt: draft.UpdatedAt})
	}
	return topics, nil
}
