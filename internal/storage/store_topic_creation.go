package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// TopicCreation is the durable intent written before Telegram IO. Target is a
// logical source identity, never a visual label. Empty titles are legacy unknowns.
type TopicCreation struct {
	ID, SessionID, Target, Title, ThreadID string
	SupersedesID                           string
	LegacyTarget                           string
	ChatID, TopicID                        int64
	Rank                                   int
	Draft                                  *model.SyncTopicDraft
}

func (s *Store) PrepareTopicCreation(ctx context.Context, op TopicCreation) (bool, error) {
	data, err := json.Marshal(op)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	// A confirmed, expired presentation may be recreated. Uncertain creation may not.
	var n int
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_topic_creations c WHERE session_id=? AND target=? AND c.rowid=(SELECT max(latest.rowid) FROM sync_topic_creations latest WHERE latest.session_id=c.session_id AND latest.target=c.target) AND
 (state!='confirmed' OR EXISTS(SELECT 1 FROM sync_topics t WHERE t.session_id=c.session_id AND t.topic_id=c.topic_id)
 OR EXISTS(SELECT 1 FROM sync_topic_drafts d WHERE d.session_id=c.session_id AND d.topic_id=c.topic_id))`, op.SessionID, op.Target).Scan(&n)
	if err != nil {
		return false, err
	}
	if op.SupersedesID == "" {
		if n != 0 {
			return false, nil
		}
	} else {
		var head string
		err = tx.QueryRowContext(ctx, `SELECT id FROM sync_topic_creations c WHERE session_id=? AND target=? AND NOT EXISTS(SELECT 1 FROM sync_topic_creations child WHERE child.supersedes_id=c.id) ORDER BY rowid DESC LIMIT 1`, op.SessionID, op.Target).Scan(&head)
		if err != nil || head != op.SupersedesID {
			return false, err
		}
		var eligible int
		err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_topic_creations WHERE id=? AND state IN ('failed','unknown') AND topic_id=0`, head).Scan(&eligible)
		if err != nil || eligible != 1 {
			return false, err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sync_topic_creations(id,session_id,chat_id,target,title,payload,state,topic_id,supersedes_id) VALUES(?,?,?,?,?,?,'sending',0,?)`, op.ID, op.SessionID, op.ChatID, op.Target, op.Title, string(data), nullable(op.SupersedesID))
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) TopicCreationResult(ctx context.Context, id string, topicID int64, outcome string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if topicID > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE sync_topic_creations SET topic_id=? WHERE id=? AND (topic_id=0 OR topic_id=?)`, topicID, id, topicID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE sync_topic_creations SET state=? WHERE id=? AND state!='confirmed'`, outcome, id)
	}
	if err != nil {
		return err
	}
	if outcome != "confirmed" {
		var op TopicCreation
		var payload string
		if err = tx.QueryRowContext(ctx, `SELECT payload FROM sync_topic_creations WHERE id=?`, id).Scan(&payload); err != nil {
			return err
		}
		if err = json.Unmarshal([]byte(payload), &op); err != nil {
			return err
		}
		if err = topicCreationNotice(ctx, tx, op, outcome); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func topicCreationNotice(ctx context.Context, tx *sql.Tx, op TopicCreation, reason string) error {
	id := "topic-create:" + op.ID + ":" + reason
	text := fmt.Sprintf("Sync topic %q: %s. Codex work is not stopped. Use sync off/on to rebuild known topics; an untracked leftover may need manual deletion.", op.Title, reason)
	return enqueueDelivery(ctx, tx, model.DeliveryQueueItem{EventID: id, ChatKey: model.ChatKey(op.ChatID, 0), ChatID: op.ChatID, Kind: "health", Status: model.DeliveryStatusPending, PayloadJSON: MustJSON(model.DeliveryPayload{Text: text, EventID: id})})
}

// RecordTopicCreated commits before the polling offset is acknowledged.
func (s *Store) RecordTopicCreated(ctx context.Context, chatID, topicID, updateID int64, title string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sync_topic_created_events(chat_id,topic_id,update_id,title) VALUES(?,?,?,?) ON CONFLICT(chat_id,topic_id) DO NOTHING`, chatID, topicID, updateID, title)
	return err
}

func (s *Store) PendingTopicCreations(ctx context.Context) ([]TopicCreation, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, `SELECT id,payload,topic_id,title FROM sync_topic_creations WHERE state IN ('sending','unknown')`)
	if err != nil {
		return nil, err
	}
	var ops []TopicCreation
	for rows.Next() {
		var op TopicCreation
		var payload string
		if err = rows.Scan(&op.ID, &payload, &op.TopicID, &op.Title); err != nil {
			rows.Close()
			return nil, err
		}
		id, tid, title := op.ID, op.TopicID, op.Title
		if err = json.Unmarshal([]byte(payload), &op); err != nil {
			rows.Close()
			return nil, err
		}
		op.ID, op.TopicID, op.Title = id, tid, title
		ops = append(ops, op)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range ops {
		op := &ops[i]
		if op.TopicID > 0 {
			continue
		}
		var historical int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_topic_creations WHERE chat_id=? AND title=? AND state='confirmed'`, op.ChatID, op.Title).Scan(&historical); err != nil {
			return nil, err
		}
		var candidates, events, legacy int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_topic_creations WHERE chat_id=? AND title=? AND state IN ('sending','unknown') AND topic_id=0`, op.ChatID, op.Title).Scan(&candidates); err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_topic_creations WHERE chat_id=? AND title='' AND state IN ('sending','unknown')`, op.ChatID).Scan(&legacy); err != nil {
			return nil, err
		}
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_topic_created_events e WHERE chat_id=? AND title=? AND NOT EXISTS(SELECT 1 FROM sync_topic_creations c WHERE c.chat_id=e.chat_id AND c.topic_id=e.topic_id)`, op.ChatID, op.Title).Scan(&events); err != nil {
			return nil, err
		}
		if candidates == 1 && events == 1 && legacy == 0 && historical == 0 {
			err = tx.QueryRowContext(ctx, `SELECT topic_id FROM sync_topic_created_events e WHERE chat_id=? AND title=? AND NOT EXISTS(SELECT 1 FROM sync_topic_creations c WHERE c.chat_id=e.chat_id AND c.topic_id=e.topic_id)`, op.ChatID, op.Title).Scan(&op.TopicID)
			if err != nil {
				return nil, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE sync_topic_creations SET topic_id=? WHERE id=?`, op.TopicID, op.ID); err != nil {
				return nil, err
			}
		} else if events > 0 {
			if err = topicCreationNotice(ctx, tx, *op, "ambiguous creation event"); err != nil {
				return nil, err
			}
		}
	}
	rows, err = tx.QueryContext(ctx, `SELECT e.chat_id,e.topic_id,e.title FROM sync_topic_created_events e WHERE NOT EXISTS(SELECT 1 FROM sync_topic_creations c WHERE c.chat_id=e.chat_id AND c.topic_id=e.topic_id) AND EXISTS(SELECT 1 FROM sync_topic_creations c WHERE c.chat_id=e.chat_id AND c.title=e.title AND c.state='confirmed')`)
	if err != nil {
		return nil, err
	}
	var extras []TopicCreation
	for rows.Next() {
		var op TopicCreation
		if err = rows.Scan(&op.ChatID, &op.TopicID, &op.Title); err != nil {
			rows.Close()
			return nil, err
		}
		op.ID = fmt.Sprintf("event:%d:%d", op.ChatID, op.TopicID)
		extras = append(extras, op)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, op := range extras {
		if err = topicCreationNotice(ctx, tx, op, "unmatched or duplicate topic"); err != nil {
			return nil, err
		}
	}
	return ops, tx.Commit()
}

// MaterializeTopicCreation atomically persists confirmation and its presentation.
// A late confirmation for an ended session creates only a cleanup target.
func (s *Store) MaterializeTopicCreation(ctx context.Context, op TopicCreation) error {
	if op.TopicID <= 0 {
		return fmt.Errorf("topic creation has no confirmed id")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var canonicalState, canonicalPayload string
	var canonicalID int64
	if err = tx.QueryRowContext(ctx, `SELECT state,topic_id,payload FROM sync_topic_creations WHERE id=?`, op.ID).Scan(&canonicalState, &canonicalID, &canonicalPayload); err != nil {
		return err
	}
	if canonicalID != op.TopicID {
		return fmt.Errorf("conflicting topic confirmation")
	}
	id := op.ID
	if err = json.Unmarshal([]byte(canonicalPayload), &op); err != nil {
		return err
	}
	op.ID = id
	op.TopicID = canonicalID
	if canonicalState == "confirmed" {
		return tx.Commit()
	}
	var active int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM sync_state WHERE id=1 AND session_id=? AND chat_id=? AND state IN (?,?)`, op.SessionID, op.ChatID, model.SyncStateActive, model.SyncStateActivating).Scan(&active); err != nil {
		return err
	}
	if active != 0 && strings.HasPrefix(op.Target, "external:") {
		var eligible int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM external_launch_requests WHERE id=? AND (status=? OR (status=? AND error_type='telegram_topic') OR (status='failed' AND dispatch_phase='not_sent'))`, strings.TrimPrefix(op.Target, "external:"), model.ExternalLaunchStarting, model.ExternalLaunchOutcomeUnknown).Scan(&eligible); err != nil {
			return err
		}
		if eligible == 0 {
			active = 0
		}
	}
	if active != 0 && op.ThreadID != "" {
		var existing int64
		err = tx.QueryRowContext(ctx, `SELECT topic_id FROM sync_topics WHERE session_id=? AND thread_id=? AND telegram_state='connected' LIMIT 1`, op.SessionID, op.ThreadID).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && existing != op.TopicID {
			if _, err = tx.ExecContext(ctx, `UPDATE sync_topic_creations SET state='confirmed' WHERE id=?`, op.ID); err != nil {
				return err
			}
			if err = topicCreationNotice(ctx, tx, op, "unmatched or duplicate topic"); err != nil {
				return err
			}
			return tx.Commit()
		}
	}
	now := model.NowString()
	state := model.SyncTopicConnected
	if active == 0 {
		state = model.SyncTopicCleanup
	}
	if op.Draft != nil {
		d := op.Draft
		ds := model.SyncDraftReady
		if strings.HasPrefix(op.Target, "external:") {
			ds = "external"
		}
		if active == 0 {
			ds = model.SyncDraftCleanup
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sync_topic_drafts(session_id,chat_id,topic_id,rank,title,cwd,project_name,directory_name,state,source_message_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,0,?,?) ON CONFLICT(session_id,topic_id) DO NOTHING`, op.SessionID, op.ChatID, op.TopicID, op.Rank, op.Title, d.CWD, d.ProjectName, d.DirectoryName, ds, now, now)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO sync_topics(session_id,chat_id,topic_id,thread_id,rank,title,telegram_state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id,topic_id) DO NOTHING`, op.SessionID, op.ChatID, op.TopicID, op.ThreadID, op.Rank, op.Title, state, now, now)
	}
	if err != nil {
		return err
	}
	var previous string
	if err = tx.QueryRowContext(ctx, `SELECT state FROM sync_topic_creations WHERE id=?`, op.ID).Scan(&previous); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sync_topic_creations SET state='confirmed',topic_id=? WHERE id=?`, op.TopicID, op.ID); err != nil {
		return err
	}
	if previous == "unknown" {
		if err = topicCreationNotice(ctx, tx, op, "recovered"); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ImportLegacyTopicCreations preserves the old no-replay fence without pausing
// unrelated targets. The marker never contained an exact submitted name.
func (s *Store) ImportLegacyTopicCreations(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT key,value FROM daemon_state WHERE key LIKE 'sync.topic_create.%'`)
	if err != nil {
		return err
	}
	type marker struct{ k, v string }
	var markers []marker
	for rows.Next() {
		var m marker
		if err = rows.Scan(&m.k, &m.v); err != nil {
			rows.Close()
			return err
		}
		markers = append(markers, m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, m := range markers {
		session := m.k[len("sync.topic_create."):]
		state, err := s.GetSyncState(ctx)
		if err != nil {
			return err
		}
		chatID := state.ChatID
		var known int64
		_ = s.db.QueryRowContext(ctx, `SELECT chat_id,topic_id FROM sync_topics WHERE session_id=? AND thread_id=?`, session, m.v).Scan(&chatID, &known)
		op := TopicCreation{ID: "legacy:" + session, SessionID: session, ChatID: chatID, Target: "thread:" + m.v, ThreadID: m.v, TopicID: known}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		status := "unknown"
		if known > 0 {
			status = "confirmed"
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sync_topic_creations(id,session_id,chat_id,target,title,payload,state,topic_id) VALUES(?,?,?,?,'',?,?,?) ON CONFLICT(id) DO NOTHING`, op.ID, session, chatID, op.Target, MustJSON(op), status, known)
		if err == nil && known == 0 {
			err = topicCreationNotice(ctx, tx, op, "legacy outcome unknown")
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM daemon_state WHERE key=?`, m.k)
		}
		if err != nil {
			rollback(tx)
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) RecoverTopicCreations(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM sync_topic_creations WHERE state='sending'`)
	if err != nil {
		return err
	}
	var ops []TopicCreation
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			rows.Close()
			return err
		}
		var op TopicCreation
		if err = json.Unmarshal([]byte(payload), &op); err != nil {
			rows.Close()
			return err
		}
		ops = append(ops, op)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, op := range ops {
		if err = topicCreationNotice(ctx, tx, op, "unknown after restart"); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE sync_topic_creations SET state='unknown' WHERE state='sending'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ReportSyncCleanup(ctx context.Context, session string, chatID, topicID int64, failed bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollback(tx)
	key := fmt.Sprintf("sync.cleanup.%s.%d", session, topicID)
	var prior string
	err = tx.QueryRowContext(ctx, `SELECT value FROM daemon_state WHERE key=?`, key).Scan(&prior)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if failed && prior == "failed" || !failed && prior != "failed" {
		return tx.Commit()
	}
	outcome := "recovered"
	if failed {
		outcome = "failed"
	}
	if err = topicCreationNotice(ctx, tx, TopicCreation{ID: key, ChatID: chatID, Title: fmt.Sprintf("cleanup topic %d", topicID)}, outcome); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO daemon_state(key,value,updated_at) VALUES(?,?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, key, outcome, model.NowString()); err != nil {
		return err
	}
	return tx.Commit()
}
