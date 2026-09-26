package storage

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Store) GetSyncState(ctx context.Context) (model.SyncState, error) {
	var state model.SyncState
	var snapshotAt, endedAt sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session_id, chat_id, state, security_state, snapshot_at,
		activation_summary_json, created_at, ended_at FROM sync_state WHERE id = 1`).Scan(
		&state.SessionID, &state.ChatID, &state.State, &state.SecurityState, &snapshotAt,
		&state.ActivationSummaryJSON, &state.CreatedAt, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SyncState{State: model.SyncStateOff, SecurityState: model.SyncSecurityUnknown}, nil
	}
	state.SnapshotAt = model.TimeString(snapshotAt.String)
	state.EndedAt = model.TimeString(endedAt.String)
	return state, err
}

func (s *Store) BeginSyncActivation(ctx context.Context, sessionID string, chatID int64) error {
	now := string(model.NowString())
	_, err := s.db.ExecContext(ctx, `INSERT INTO sync_state(id, session_id, chat_id, state, security_state, snapshot_at,
		activation_summary_json, created_at, ended_at) VALUES (1, ?, ?, ?, ?, ?, '', ?, NULL)
		ON CONFLICT(id) DO UPDATE SET session_id=excluded.session_id, chat_id=excluded.chat_id,
		state=excluded.state, security_state=excluded.security_state, snapshot_at=excluded.snapshot_at,
		activation_summary_json='', created_at=excluded.created_at, ended_at=NULL`,
		sessionID, chatID, model.SyncStateActivating, model.SyncSecurityValid, now, now)
	return err
}

// RecoverSyncState converts an interrupted activation into a logically-off
// session whose already-created topics are cleanup-only. Active sessions stay
// active and are resumed by passive polling; no writer is started here.
func (s *Store) RecoverSyncState(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var sessionID, state string
	err = tx.QueryRowContext(ctx, `SELECT session_id, state FROM sync_state WHERE id=1`).Scan(&sessionID, &state)
	if errors.Is(err, sql.ErrNoRows) || state != model.SyncStateActivating {
		return tx.Commit()
	}
	if err != nil {
		return err
	}
	now := string(model.NowString())
	if _, err := tx.ExecContext(ctx, `UPDATE sync_state SET state=?, ended_at=? WHERE id=1`, model.SyncStateOff, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topics SET telegram_state=?, updated_at=? WHERE session_id=?`, model.SyncTopicCleanup, now, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecoverSyncWriterState(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	if _, err := tx.ExecContext(ctx, `UPDATE sync_message_receipts SET state=?, updated_at=?
		WHERE state=? AND session_id=(SELECT session_id FROM sync_state WHERE id=1 AND state=?)`,
		model.SyncReceiptUnknown, now, model.SyncReceiptAccepted, model.SyncStateActive); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topics SET active_turn_state=?, updated_at=?
		WHERE session_id=(SELECT session_id FROM sync_state WHERE id=1 AND state=?) AND active_turn_state IN (?,?)`,
		model.SyncTurnUnknown, now, model.SyncStateActive, model.SyncTurnStarting, model.SyncTurnActive); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topic_drafts SET state=?, updated_at=?
		WHERE session_id=(SELECT session_id FROM sync_state WHERE id=1 AND state=?) AND state=?`,
		model.SyncDraftUnknown, now, model.SyncStateActive, model.SyncDraftStarting); err != nil {
		return err
	}
	return tx.Commit()
}

// ResetSyncOnStartup makes the previous Sync session cleanup-only without
// touching non-Sync control-plane state. Unfinished receipts remain durable and
// non-replayable for audit purposes.
func (s *Store) ResetSyncOnStartup(ctx context.Context) (string, error) {
	return s.resetSyncTransientState(ctx)
}

// ResetSyncOnTransportLoss applies the same deliberately lossy boundary as a
// bot restart: Telegram synchronization is discarded, while Codex runtime
// threads are neither interrupted nor replayed.
func (s *Store) ResetSyncOnTransportLoss(ctx context.Context) (string, error) {
	return s.resetSyncTransientState(ctx)
}

func (s *Store) resetSyncTransientState(ctx context.Context) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer rollback(tx)

	var sessionID string
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM sync_state WHERE id=1`).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", tx.Commit()
	}
	if err != nil {
		return "", err
	}

	now := model.NowString()
	if _, err := tx.ExecContext(ctx, `UPDATE sync_state SET state=?, ended_at=? WHERE id=1`,
		model.SyncStateOff, now); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_message_receipts SET state=?, updated_at=?
		WHERE session_id=? AND state=?`, model.SyncReceiptUnknown, now, sessionID, model.SyncReceiptAccepted); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topics SET telegram_state=?, active_turn_id=NULL,
		active_turn_state=?, writer_generation=0, pending_telegram_user_fp=NULL,
		pending_telegram_turn_id=NULL, updated_at=? WHERE session_id=?`,
		model.SyncTopicCleanup, model.SyncTurnTerminal, now, sessionID); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topic_drafts SET state=?, updated_at=? WHERE session_id=?`,
		model.SyncDraftCleanup, now, sessionID); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return sessionID, nil
}

func (s *Store) UpsertSyncTopic(ctx context.Context, topic model.SyncTopic) error {
	now := string(model.NowString())
	if strings.TrimSpace(topic.TelegramState) == "" {
		topic.TelegramState = model.SyncTopicConnected
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO sync_topics(session_id, chat_id, topic_id, thread_id, rank, title,
		telegram_state, status_message_id, status_turn_id, last_render_fp, last_final_fp, last_user_fp,
		pending_telegram_user_fp, pending_telegram_turn_id, active_turn_id, active_turn_state,
		writer_generation, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, topic_id) DO UPDATE SET thread_id=excluded.thread_id, rank=excluded.rank,
		title=excluded.title, telegram_state=excluded.telegram_state, status_message_id=excluded.status_message_id,
		status_turn_id=excluded.status_turn_id, last_render_fp=excluded.last_render_fp,
		last_final_fp=excluded.last_final_fp, last_user_fp=excluded.last_user_fp,
		pending_telegram_user_fp=excluded.pending_telegram_user_fp,
		pending_telegram_turn_id=excluded.pending_telegram_turn_id, updated_at=excluded.updated_at`,
		topic.SessionID, topic.ChatID, topic.TopicID, topic.ThreadID, topic.Rank, topic.Title,
		topic.TelegramState, topic.StatusMessageID, nullable(topic.StatusTurnID), nullable(topic.LastRenderFP), nullable(topic.LastFinalFP),
		nullable(topic.LastUserFP), nullable(topic.PendingTelegramUserFP), nullable(topic.PendingTelegramTurnID),
		nullable(topic.ActiveTurnID), nullable(topic.ActiveTurnState), topic.WriterGeneration, now, now)
	return err
}

func (s *Store) CreateSyncTopicDraft(ctx context.Context, draft model.SyncTopicDraft) error {
	now := model.NowString()
	result, err := s.db.ExecContext(ctx, `INSERT INTO sync_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,
		project_name,directory_name,state,source_message_id,created_at,updated_at)
		SELECT ?,?,?,?,?,?,?,?,?,0,?,? WHERE EXISTS (
			SELECT 1 FROM sync_state WHERE id=1 AND session_id=? AND chat_id=? AND state=?
		)`, draft.SessionID, draft.ChatID, draft.TopicID, draft.Rank, draft.Title, draft.CWD,
		nullable(draft.ProjectName), nullable(draft.DirectoryName), model.SyncDraftReady, now, now,
		draft.SessionID, draft.ChatID, model.SyncStateActive)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync draft session is stale")
	}
	return nil
}

func (s *Store) ListSyncTopicDrafts(ctx context.Context, sessionID string) ([]model.SyncTopicDraft, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,chat_id,topic_id,rank,title,cwd,coalesce(project_name,''),
		coalesce(directory_name,''),state,source_message_id,created_at,updated_at
		FROM sync_topic_drafts WHERE session_id=? ORDER BY rank,topic_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drafts []model.SyncTopicDraft
	for rows.Next() {
		var draft model.SyncTopicDraft
		if err := rows.Scan(&draft.SessionID, &draft.ChatID, &draft.TopicID, &draft.Rank, &draft.Title, &draft.CWD,
			&draft.ProjectName, &draft.DirectoryName, &draft.State, &draft.SourceMessageID, &draft.CreatedAt, &draft.UpdatedAt); err != nil {
			return nil, err
		}
		drafts = append(drafts, draft)
	}
	return drafts, rows.Err()
}

func (s *Store) GetActiveSyncTopicDraft(ctx context.Context, chatID, topicID int64) (*model.SyncTopicDraft, error) {
	var draft model.SyncTopicDraft
	err := s.db.QueryRowContext(ctx, `SELECT d.session_id,d.chat_id,d.topic_id,d.rank,d.title,d.cwd,
		coalesce(d.project_name,''),coalesce(d.directory_name,''),d.state,d.source_message_id,d.created_at,d.updated_at
		FROM sync_topic_drafts d JOIN sync_state s ON s.session_id=d.session_id
		WHERE s.id=1 AND s.state=? AND s.chat_id=? AND d.topic_id=?`, model.SyncStateActive, chatID, topicID).Scan(
		&draft.SessionID, &draft.ChatID, &draft.TopicID, &draft.Rank, &draft.Title, &draft.CWD,
		&draft.ProjectName, &draft.DirectoryName, &draft.State, &draft.SourceMessageID, &draft.CreatedAt, &draft.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &draft, err
}

func (s *Store) ClaimSyncTopicDraftMessage(ctx context.Context, chatID, topicID, messageID int64) (model.SyncTopicDraft, model.SyncMessageReceipt, bool, error) {
	if messageID <= 0 {
		return model.SyncTopicDraft{}, model.SyncMessageReceipt{}, false, errors.New("telegram source message id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.SyncTopicDraft{}, model.SyncMessageReceipt{}, false, err
	}
	defer rollback(tx)
	var existing model.SyncMessageReceipt
	err = tx.QueryRowContext(ctx, `SELECT chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at
		FROM sync_message_receipts WHERE chat_id=? AND topic_id=? AND message_id=?`, chatID, topicID, messageID).Scan(
		&existing.ChatID, &existing.TopicID, &existing.MessageID, &existing.SessionID, &existing.ThreadID,
		&existing.State, &existing.CreatedAt, &existing.UpdatedAt)
	if err == nil {
		return model.SyncTopicDraft{}, existing, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.SyncTopicDraft{}, model.SyncMessageReceipt{}, false, err
	}
	var draft model.SyncTopicDraft
	err = tx.QueryRowContext(ctx, `SELECT d.session_id,d.chat_id,d.topic_id,d.rank,d.title,d.cwd,
		coalesce(d.project_name,''),coalesce(d.directory_name,''),d.state,d.source_message_id,d.created_at,d.updated_at
		FROM sync_topic_drafts d JOIN sync_state s ON s.session_id=d.session_id
		WHERE s.id=1 AND s.state=? AND s.chat_id=? AND d.topic_id=?`, model.SyncStateActive, chatID, topicID).Scan(
		&draft.SessionID, &draft.ChatID, &draft.TopicID, &draft.Rank, &draft.Title, &draft.CWD,
		&draft.ProjectName, &draft.DirectoryName, &draft.State, &draft.SourceMessageID, &draft.CreatedAt, &draft.UpdatedAt)
	if err != nil {
		return model.SyncTopicDraft{}, model.SyncMessageReceipt{}, false, err
	}
	if draft.State != model.SyncDraftReady {
		return draft, model.SyncMessageReceipt{}, false, errors.New("Sync draft already has unfinished work")
	}
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `UPDATE sync_topic_drafts SET state=?,source_message_id=?,updated_at=?
		WHERE session_id=? AND topic_id=? AND state=? AND source_message_id=0`, model.SyncDraftStarting, messageID, now,
		draft.SessionID, draft.TopicID, model.SyncDraftReady)
	if err != nil {
		return model.SyncTopicDraft{}, model.SyncMessageReceipt{}, false, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return draft, model.SyncMessageReceipt{}, false, errors.New("Sync draft claim is stale")
	}
	receipt := model.SyncMessageReceipt{ChatID: chatID, TopicID: topicID, MessageID: messageID,
		SessionID: draft.SessionID, State: model.SyncReceiptAccepted, CreatedAt: now, UpdatedAt: now}
	if _, err := tx.ExecContext(ctx, `INSERT INTO sync_message_receipts(chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, "",
		receipt.State, receipt.CreatedAt, receipt.UpdatedAt); err != nil {
		return model.SyncTopicDraft{}, model.SyncMessageReceipt{}, false, err
	}
	draft.State, draft.SourceMessageID, draft.UpdatedAt = model.SyncDraftStarting, messageID, now
	if err := tx.Commit(); err != nil {
		return model.SyncTopicDraft{}, model.SyncMessageReceipt{}, false, err
	}
	return draft, receipt, true, nil
}

func (s *Store) ResetSyncTopicDraftMessage(ctx context.Context, draft model.SyncTopicDraft, receipt model.SyncMessageReceipt) error {
	return s.finishSyncTopicDraftMessage(ctx, draft, receipt, model.SyncDraftReady, model.SyncReceiptRejected, 0)
}

func (s *Store) MarkSyncTopicDraftUnknown(ctx context.Context, draft model.SyncTopicDraft, receipt model.SyncMessageReceipt) error {
	return s.finishSyncTopicDraftMessage(ctx, draft, receipt, model.SyncDraftUnknown, model.SyncReceiptUnknown, receipt.MessageID)
}

func (s *Store) finishSyncTopicDraftMessage(ctx context.Context, draft model.SyncTopicDraft, receipt model.SyncMessageReceipt, draftState, receiptState string, sourceMessageID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `UPDATE sync_message_receipts SET state=?,updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, receiptState, now,
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.SyncReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync draft receipt transition is stale")
	}
	result, err = tx.ExecContext(ctx, `UPDATE sync_topic_drafts SET state=?,source_message_id=?,updated_at=?
		WHERE session_id=? AND topic_id=? AND state=? AND source_message_id=?`, draftState, sourceMessageID, now,
		draft.SessionID, draft.TopicID, model.SyncDraftStarting, receipt.MessageID)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync draft transition is stale")
	}
	return tx.Commit()
}

func (s *Store) MaterializeSyncTopicDraft(ctx context.Context, draft model.SyncTopicDraft, receipt model.SyncMessageReceipt, threadID, title string, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `INSERT INTO sync_topics(session_id,chat_id,topic_id,thread_id,rank,title,telegram_state,
		active_turn_state,writer_generation,created_at,updated_at)
		SELECT session_id,chat_id,topic_id,?,rank,?,?,?, ?,created_at,?
		FROM sync_topic_drafts WHERE session_id=? AND topic_id=? AND state=? AND source_message_id=?`,
		threadID, title, model.SyncTopicConnected, model.SyncTurnStarting, generation, now,
		draft.SessionID, draft.TopicID, model.SyncDraftStarting, receipt.MessageID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync draft materialization is stale")
	}
	result, err = tx.ExecContext(ctx, `UPDATE sync_message_receipts SET thread_id=?,updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=? AND thread_id=''`,
		threadID, now, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.SyncReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync draft receipt materialization is stale")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_topic_drafts WHERE session_id=? AND topic_id=?`, draft.SessionID, draft.TopicID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DematerializeSyncTopicDraft(ctx context.Context, draft model.SyncTopicDraft, receipt model.SyncMessageReceipt, threadID, title string, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `INSERT INTO sync_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,
		project_name,directory_name,state,source_message_id,created_at,updated_at)
		SELECT session_id,chat_id,topic_id,rank,?,?,?,?,?,0,created_at,?
		FROM sync_topics WHERE session_id=? AND topic_id=? AND thread_id=? AND active_turn_state=? AND writer_generation=?`,
		title, draft.CWD, nullable(draft.ProjectName), nullable(draft.DirectoryName), model.SyncDraftReady, now,
		draft.SessionID, draft.TopicID, threadID, model.SyncTurnStarting, generation)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync draft dematerialization is stale")
	}
	result, err = tx.ExecContext(ctx, `UPDATE sync_message_receipts SET state=?,updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND thread_id=? AND state=?`,
		model.SyncReceiptRejected, now, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, threadID, model.SyncReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync draft receipt dematerialization is stale")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_topics WHERE session_id=? AND topic_id=? AND thread_id=?`,
		draft.SessionID, draft.TopicID, threadID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ConvertEmptySyncTopicToDraft(ctx context.Context, topic model.SyncTopic, draft model.SyncTopicDraft, receipt model.SyncMessageReceipt, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `INSERT INTO sync_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,
		project_name,directory_name,state,source_message_id,created_at,updated_at)
		SELECT session_id,chat_id,topic_id,rank,?,?,?,?,?, ?,created_at,?
		FROM sync_topics WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=?
		AND status_message_id=0 AND coalesce(status_turn_id,'')='' AND coalesce(last_render_fp,'')=''
		AND coalesce(last_final_fp,'')='' AND coalesce(active_turn_id,'')='' AND active_turn_state=? AND writer_generation=?`,
		draft.Title, draft.CWD, nullable(draft.ProjectName), nullable(draft.DirectoryName), model.SyncDraftStarting,
		receipt.MessageID, now, topic.SessionID, topic.TopicID, topic.ThreadID, model.SyncTopicConnected,
		model.SyncTurnStarting, generation)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync empty topic is not eligible for draft conversion")
	}
	result, err = tx.ExecContext(ctx, `UPDATE sync_message_receipts SET thread_id='',updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND thread_id=? AND state=?`,
		now, receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, topic.ThreadID, model.SyncReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync empty topic receipt conversion is stale")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_topics WHERE session_id=? AND topic_id=? AND thread_id=?`,
		topic.SessionID, topic.TopicID, topic.ThreadID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) FinishSyncActivation(ctx context.Context, sessionID, summaryJSON string, active bool) error {
	return s.finishSyncActivation(ctx, sessionID, summaryJSON, active, nil)
}

func (s *Store) FinishSyncActivationWithDelivery(ctx context.Context, sessionID, summaryJSON string, active bool, delivery model.DeliveryQueueItem) error {
	return s.finishSyncActivation(ctx, sessionID, summaryJSON, active, &delivery)
}

func (s *Store) finishSyncActivation(ctx context.Context, sessionID, summaryJSON string, active bool, delivery *model.DeliveryQueueItem) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	state := model.SyncStateOff
	ended := any(string(model.NowString()))
	if active {
		state, ended = model.SyncStateActive, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE sync_state SET state=?, activation_summary_json=?, ended_at=?
		WHERE id=1 AND session_id=? AND state=?`, state, summaryJSON, ended, sessionID, model.SyncStateActivating)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("sync activation generation is stale")
	}
	if delivery != nil {
		if err := enqueueDelivery(ctx, tx, *delivery); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListSyncTopics(ctx context.Context, sessionID string) ([]model.SyncTopic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id, chat_id, topic_id, thread_id, rank, title, telegram_state,
		status_message_id, coalesce(status_turn_id,''), coalesce(last_render_fp,''), coalesce(last_final_fp,''),
		coalesce(last_user_fp,''), coalesce(pending_telegram_user_fp,''), coalesce(pending_telegram_turn_id,''), coalesce(active_turn_id,''),
		coalesce(active_turn_state,''), writer_generation, created_at, updated_at
		FROM sync_topics WHERE session_id=? ORDER BY rank, thread_id`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SyncTopic
	for rows.Next() {
		var topic model.SyncTopic
		if err := rows.Scan(&topic.SessionID, &topic.ChatID, &topic.TopicID, &topic.ThreadID, &topic.Rank,
			&topic.Title, &topic.TelegramState, &topic.StatusMessageID, &topic.StatusTurnID, &topic.LastRenderFP, &topic.LastFinalFP,
			&topic.LastUserFP, &topic.PendingTelegramUserFP, &topic.PendingTelegramTurnID,
			&topic.ActiveTurnID, &topic.ActiveTurnState, &topic.WriterGeneration, &topic.CreatedAt, &topic.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, topic)
	}
	return out, rows.Err()
}

func (s *Store) GetActiveSyncTopicByThread(ctx context.Context, sessionID, threadID string) (*model.SyncTopic, error) {
	topics, err := s.ListSyncTopics(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, topic := range topics {
		if topic.ThreadID == threadID && topic.TelegramState == model.SyncTopicConnected {
			copy := topic
			return &copy, nil
		}
	}
	return nil, nil
}

// MarkStaleSyncTopicsForCleanup claims idle topics whose Codex thread has been
// inactive since cutoff, plus old empty drafts. Telegram deletion follows the
// transaction; claimed rows remain cleanup-only until it succeeds.
func (s *Store) MarkStaleSyncTopicsForCleanup(ctx context.Context, sessionID string, cutoff model.TimeString) ([]model.SyncTopic, error) {
	if strings.TrimSpace(sessionID) == "" || strings.TrimSpace(string(cutoff)) == "" {
		return nil, errors.New("Sync cleanup session and cutoff are required")
	}
	cutoffTime, err := time.Parse(time.RFC3339Nano, string(cutoff))
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)

	rows, err := tx.QueryContext(ctx, `SELECT kind,session_id,chat_id,topic_id,thread_id,rank,title,created_at,updated_at FROM (
		SELECT 'topic' AS kind,st.session_id,st.chat_id,st.topic_id,st.thread_id,st.rank,st.title,st.created_at,st.updated_at,
			CASE WHEN t.updated_at > 0 THEN t.updated_at ELSE unixepoch(st.updated_at) END AS activity_at
		FROM sync_topics st LEFT JOIN threads t ON t.thread_id=st.thread_id
		WHERE st.session_id=? AND st.telegram_state=?
		AND coalesce(st.active_turn_state,'') NOT IN (?,?,?)
		UNION ALL
		SELECT 'draft' AS kind,session_id,chat_id,topic_id,'' AS thread_id,rank,title,created_at,updated_at,
			unixepoch(updated_at) AS activity_at
		FROM sync_topic_drafts
		WHERE session_id=? AND state=?
	) WHERE activity_at<? ORDER BY activity_at,topic_id`,
		sessionID, model.SyncTopicConnected, model.SyncTurnStarting, model.SyncTurnActive, model.SyncTurnUnknown,
		sessionID, model.SyncDraftReady, cutoffTime.Unix())
	if err != nil {
		return nil, err
	}
	type candidate struct {
		kind  string
		topic model.SyncTopic
	}
	candidates := make([]candidate, 0)
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.kind, &item.topic.SessionID, &item.topic.ChatID, &item.topic.TopicID,
			&item.topic.ThreadID, &item.topic.Rank, &item.topic.Title, &item.topic.CreatedAt, &item.topic.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := model.NowString()
	targets := make([]model.SyncTopic, 0, len(candidates))
	for _, item := range candidates {
		var result sql.Result
		if item.kind == "draft" {
			result, err = tx.ExecContext(ctx, `UPDATE sync_topic_drafts SET state=?,updated_at=?
				WHERE session_id=? AND topic_id=? AND state=? AND updated_at<?`,
				model.SyncDraftCleanup, now, item.topic.SessionID, item.topic.TopicID, model.SyncDraftReady, cutoff)
		} else {
			result, err = tx.ExecContext(ctx, `UPDATE sync_topics SET telegram_state=?,updated_at=?
				WHERE session_id=? AND topic_id=? AND telegram_state=?
				AND coalesce(active_turn_state,'') NOT IN (?,?,?)`,
				model.SyncTopicCleanup, now, item.topic.SessionID, item.topic.TopicID, model.SyncTopicConnected,
				model.SyncTurnStarting, model.SyncTurnActive, model.SyncTurnUnknown)
		}
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed == 1 {
			item.topic.TelegramState = model.SyncTopicCleanup
			item.topic.UpdatedAt = now
			targets = append(targets, item.topic)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return targets, nil
}

func (s *Store) AcceptSyncMessage(ctx context.Context, chatID, topicID, messageID int64) (model.SyncMessageReceipt, bool, error) {
	if messageID <= 0 {
		return model.SyncMessageReceipt{}, false, errors.New("telegram source message id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return model.SyncMessageReceipt{}, false, err
	}
	defer rollback(tx)
	var receipt model.SyncMessageReceipt
	err = tx.QueryRowContext(ctx, `SELECT chat_id, topic_id, message_id, session_id, thread_id, state, created_at, updated_at
		FROM sync_message_receipts WHERE chat_id=? AND topic_id=? AND message_id=?`, chatID, topicID, messageID).Scan(
		&receipt.ChatID, &receipt.TopicID, &receipt.MessageID, &receipt.SessionID, &receipt.ThreadID, &receipt.State, &receipt.CreatedAt, &receipt.UpdatedAt)
	if err == nil {
		return receipt, false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.SyncMessageReceipt{}, false, err
	}
	var sessionID, threadID string
	err = tx.QueryRowContext(ctx, `SELECT s.session_id, t.thread_id FROM sync_state s JOIN sync_topics t
		ON t.session_id=s.session_id WHERE s.id=1 AND s.state=? AND s.chat_id=? AND t.topic_id=? AND t.telegram_state=?`,
		model.SyncStateActive, chatID, topicID, model.SyncTopicConnected).Scan(&sessionID, &threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SyncMessageReceipt{}, false, errors.New("Sync topic is not active")
	}
	if err != nil {
		return model.SyncMessageReceipt{}, false, err
	}
	now := model.NowString()
	_, err = tx.ExecContext(ctx, `INSERT INTO sync_message_receipts(chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?)`, chatID, topicID, messageID, sessionID, threadID, model.SyncReceiptAccepted, now, now)
	if err != nil {
		return model.SyncMessageReceipt{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.SyncMessageReceipt{}, false, err
	}
	return model.SyncMessageReceipt{ChatID: chatID, TopicID: topicID, MessageID: messageID, SessionID: sessionID, ThreadID: threadID, State: model.SyncReceiptAccepted, CreatedAt: now, UpdatedAt: now}, true, nil
}

func (s *Store) MarkSyncDispatchState(ctx context.Context, receipt model.SyncMessageReceipt, receiptState, turnID, turnState string, generation uint64) error {
	return s.markSyncDispatchState(ctx, receipt, receiptState, turnID, turnState, generation, "")
}

func (s *Store) MarkSyncDispatchStateWithTelegramUser(ctx context.Context, receipt model.SyncMessageReceipt, receiptState, turnID, turnState string, generation uint64, pendingUserFP string) error {
	if strings.TrimSpace(pendingUserFP) == "" {
		return errors.New("pending Telegram user fingerprint is required")
	}
	return s.markSyncDispatchState(ctx, receipt, receiptState, turnID, turnState, generation, pendingUserFP)
}

func (s *Store) markSyncDispatchState(ctx context.Context, receipt model.SyncMessageReceipt, receiptState, turnID, turnState string, generation uint64, pendingUserFP string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	result, err := tx.ExecContext(ctx, `UPDATE sync_message_receipts SET state=?, updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, receiptState, now,
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.SyncReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync receipt transition is stale")
	}
	if strings.TrimSpace(pendingUserFP) == "" {
		result, err = tx.ExecContext(ctx, `UPDATE sync_topics SET active_turn_id=?, active_turn_state=?, writer_generation=?, updated_at=?
			WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=?`, nullable(turnID), nullable(turnState), generation, now,
			receipt.SessionID, receipt.TopicID, receipt.ThreadID, model.SyncTopicConnected)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE sync_topics SET active_turn_id=?, active_turn_state=?, writer_generation=?,
			pending_telegram_user_fp=?, pending_telegram_turn_id=?, updated_at=?
			WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=?`, nullable(turnID), nullable(turnState), generation,
			nullable(pendingUserFP), nullable(turnID), now, receipt.SessionID, receipt.TopicID, receipt.ThreadID, model.SyncTopicConnected)
	}
	if err != nil {
		return err
	}
	changed, _ = result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync topic dispatch transition is stale")
	}
	return tx.Commit()
}

func (s *Store) UpdateSyncTopicUserDelivery(ctx context.Context, sessionID string, topicID int64, lastUserFP, pendingTurnID, pendingUserFP string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET last_user_fp=?, pending_telegram_turn_id=?,
		pending_telegram_user_fp=?, updated_at=? WHERE session_id=? AND topic_id=? AND telegram_state=?`,
		nullable(lastUserFP), nullable(pendingTurnID), nullable(pendingUserFP), model.NowString(),
		sessionID, topicID, model.SyncTopicConnected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync user delivery update is stale")
	}
	return nil
}

func (s *Store) MarkSyncStarting(ctx context.Context, receipt model.SyncMessageReceipt, generation uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET active_turn_id=NULL, active_turn_state=?, writer_generation=?, updated_at=?
		WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=?
		AND coalesce(active_turn_state,'') NOT IN (?,?,?)`, model.SyncTurnStarting, generation, model.NowString(),
		receipt.SessionID, receipt.TopicID, receipt.ThreadID, model.SyncTopicConnected,
		model.SyncTurnStarting, model.SyncTurnActive, model.SyncTurnUnknown)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync topic already has unfinished work")
	}
	return nil
}

func (s *Store) ResolveSyncSharedDaemonUnknown(ctx context.Context, sessionID string, topicID int64, threadID string, generation uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET active_turn_id=NULL, active_turn_state=?, writer_generation=0, updated_at=?
		WHERE session_id=? AND topic_id=? AND thread_id=? AND telegram_state=? AND active_turn_state=? AND writer_generation=?`,
		model.SyncTurnTerminal, model.NowString(), sessionID, topicID, threadID, model.SyncTopicConnected, model.SyncTurnUnknown, generation)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync shared-daemon unknown transition is stale")
	}
	return nil
}

func (s *Store) MarkSyncReceiptState(ctx context.Context, receipt model.SyncMessageReceipt, state string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sync_message_receipts SET state=?, updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, state, model.NowString(),
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.SyncReceiptAccepted)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync receipt transition is stale")
	}
	return nil
}

func (s *Store) MarkSyncDispatchFailure(ctx context.Context, receipt model.SyncMessageReceipt, state, turnState string, generation uint64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	now := model.NowString()
	if _, err := tx.ExecContext(ctx, `UPDATE sync_message_receipts SET state=?, updated_at=?
		WHERE chat_id=? AND topic_id=? AND message_id=? AND session_id=? AND state=?`, state, now,
		receipt.ChatID, receipt.TopicID, receipt.MessageID, receipt.SessionID, model.SyncReceiptAccepted); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topics SET active_turn_state=?, writer_generation=?, updated_at=?
		WHERE session_id=? AND topic_id=? AND thread_id=? AND writer_generation=?`, turnState, generation, now,
		receipt.SessionID, receipt.TopicID, receipt.ThreadID, generation); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkSyncDispatched(ctx context.Context, sessionID string, topicID, messageID int64, turnID string, generation uint64) error {
	receipt, err := s.GetSyncReceipt(ctx, topicID, messageID)
	if err != nil {
		return err
	}
	if receipt == nil || receipt.SessionID != sessionID {
		return errors.New("Sync receipt not found")
	}
	return s.MarkSyncDispatchState(ctx, *receipt, model.SyncReceiptDispatched, turnID, model.SyncTurnActive, generation)
}

func (s *Store) GetSyncReceipt(ctx context.Context, topicID, messageID int64) (*model.SyncMessageReceipt, error) {
	var receipt model.SyncMessageReceipt
	err := s.db.QueryRowContext(ctx, `SELECT chat_id,topic_id,message_id,session_id,thread_id,state,created_at,updated_at
		FROM sync_message_receipts WHERE topic_id=? AND message_id=?`, topicID, messageID).Scan(&receipt.ChatID, &receipt.TopicID, &receipt.MessageID, &receipt.SessionID, &receipt.ThreadID, &receipt.State, &receipt.CreatedAt, &receipt.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &receipt, err
}

func (s *Store) MarkSyncTerminal(ctx context.Context, sessionID, threadID, turnID string, generation uint64) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET active_turn_state=?, updated_at=?
		WHERE session_id=? AND thread_id=? AND active_turn_id=? AND writer_generation=? AND active_turn_state IN (?,?)`,
		model.SyncTurnTerminal, model.NowString(), sessionID, threadID, turnID, generation, model.SyncTurnActive, model.SyncTurnStarting)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync terminal evidence is stale")
	}
	return nil
}

func (s *Store) GetActiveSyncTopic(ctx context.Context, chatID, topicID int64) (*model.SyncTopic, error) {
	state, err := s.GetSyncState(ctx)
	if err != nil || state.State != model.SyncStateActive || state.ChatID != chatID {
		return nil, err
	}
	topics, err := s.ListSyncTopics(ctx, state.SessionID)
	if err != nil {
		return nil, err
	}
	for _, topic := range topics {
		if topic.TopicID == topicID && topic.TelegramState == model.SyncTopicConnected {
			copy := topic
			return &copy, nil
		}
	}
	return nil, nil
}

func (s *Store) MarkSyncOff(ctx context.Context, sessionID string) ([]model.SyncTopic, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	now := string(model.NowString())
	if _, err := tx.ExecContext(ctx, `UPDATE sync_state SET state=?, ended_at=? WHERE id=1 AND session_id=?`, model.SyncStateOff, now, sessionID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topics SET telegram_state=?, updated_at=? WHERE session_id=? AND telegram_state=?`, model.SyncTopicCleanup, now, sessionID, model.SyncTopicConnected); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sync_topic_drafts SET state=?, updated_at=? WHERE session_id=?`, model.SyncDraftCleanup, now, sessionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.ListSyncCleanupTargets(ctx, sessionID)
}

func (s *Store) MarkSyncDraining(ctx context.Context, sessionID string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sync_state SET state=? WHERE id=1 AND session_id=? AND state IN (?,?)`,
		model.SyncStateDraining, sessionID, model.SyncStateActive, model.SyncStateDraining)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync session cannot enter draining")
	}
	return nil
}

func (s *Store) UpdateSyncTopicDelivery(ctx context.Context, sessionID string, topicID, statusMessageID int64, statusTurnID, renderFP, finalFP string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET status_message_id=?, status_turn_id=?, last_render_fp=?, last_final_fp=?, updated_at=? WHERE session_id=? AND topic_id=?`,
		statusMessageID, nullable(statusTurnID), nullable(renderFP), nullable(finalFP), string(model.NowString()), sessionID, topicID)
	return err
}

func (s *Store) UpdateSyncTopicStatusDelivery(ctx context.Context, sessionID string, topicID, statusMessageID int64, statusTurnID, renderFP string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET status_message_id=?, status_turn_id=?, last_render_fp=?, updated_at=? WHERE session_id=? AND topic_id=?`,
		statusMessageID, nullable(statusTurnID), nullable(renderFP), string(model.NowString()), sessionID, topicID)
	return err
}

func (s *Store) UpdateSyncTopicFinalDelivery(ctx context.Context, sessionID string, topicID int64, finalFP string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET last_final_fp=?, updated_at=? WHERE session_id=? AND topic_id=?`,
		nullable(finalFP), string(model.NowString()), sessionID, topicID)
	return err
}

func (s *Store) UpdateSyncTopicTitle(ctx context.Context, sessionID string, topicID int64, title string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET title=?,updated_at=?
		WHERE session_id=? AND topic_id=? AND telegram_state=?`, title, model.NowString(), sessionID, topicID, model.SyncTopicConnected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return errors.New("Sync topic title update is stale")
	}
	return nil
}

func (s *Store) DeleteSyncTopic(ctx context.Context, sessionID string, topicID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_topics WHERE session_id=? AND topic_id=?`, sessionID, topicID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sync_topic_drafts WHERE session_id=? AND topic_id=?`, sessionID, topicID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListSyncCleanupTargets(ctx context.Context, sessionID string) ([]model.SyncTopic, error) {
	topics, err := s.ListSyncTopics(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	drafts, err := s.ListSyncTopicDrafts(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	for _, draft := range drafts {
		if draft.State != model.SyncDraftCleanup {
			continue
		}
		topics = append(topics, model.SyncTopic{SessionID: draft.SessionID, ChatID: draft.ChatID, TopicID: draft.TopicID,
			Rank: draft.Rank, Title: draft.Title, TelegramState: model.SyncTopicCleanup, CreatedAt: draft.CreatedAt, UpdatedAt: draft.UpdatedAt})
	}
	return topics, nil
}

func (s *Store) ListAllSyncCleanupTargets(ctx context.Context, chatID int64) ([]model.SyncTopic, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,chat_id,topic_id,thread_id,rank,title,created_at,updated_at FROM (
		SELECT session_id,chat_id,topic_id,thread_id,rank,title,created_at,updated_at
		FROM sync_topics WHERE chat_id=? AND telegram_state=?
		UNION ALL
		SELECT session_id,chat_id,topic_id,'' AS thread_id,rank,title,created_at,updated_at
		FROM sync_topic_drafts WHERE chat_id=? AND state=?
	) ORDER BY updated_at,topic_id`, chatID, model.SyncTopicCleanup, chatID, model.SyncDraftCleanup)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	targets := []model.SyncTopic{}
	for rows.Next() {
		var topic model.SyncTopic
		if err := rows.Scan(&topic.SessionID, &topic.ChatID, &topic.TopicID, &topic.ThreadID,
			&topic.Rank, &topic.Title, &topic.CreatedAt, &topic.UpdatedAt); err != nil {
			return nil, err
		}
		topic.TelegramState = model.SyncTopicCleanup
		targets = append(targets, topic)
	}
	return targets, rows.Err()
}
