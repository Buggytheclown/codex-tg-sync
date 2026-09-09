package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

var telegramBotCredentialPattern = regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`)

func redactTelegramBotCredentials(value []byte) []byte {
	return telegramBotCredentialPattern.ReplaceAll(value, []byte("bot<redacted>"))
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) initialize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA journal_mode=WAL`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout=5000`); err != nil {
		return err
	}
	schema := `
	CREATE TABLE IF NOT EXISTS threads (
		thread_id TEXT PRIMARY KEY,
		title TEXT NOT NULL,
		cwd TEXT,
		project_name TEXT NOT NULL,
		directory_name TEXT,
		updated_at INTEGER NOT NULL,
		status TEXT,
		last_preview TEXT,
		active_turn_id TEXT,
		preferred_model TEXT,
		permissions_mode TEXT,
		archived INTEGER NOT NULL DEFAULT 0,
		raw_json TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS thread_snapshots (
		thread_id TEXT PRIMARY KEY,
		last_live_event_at TEXT,
		last_poll_at TEXT,
		next_poll_after TEXT,
		last_seen_thread_status TEXT,
		last_seen_turn_id TEXT,
		last_seen_turn_status TEXT,
		last_progress_fp TEXT,
		last_progress_sent_at TEXT,
		last_final_fp TEXT,
		last_completion_fp TEXT,
		last_approval_fp TEXT,
		snapshot_json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS callback_routes (
		route_token TEXT PRIMARY KEY,
		action TEXT NOT NULL,
		thread_id TEXT NOT NULL,
		turn_id TEXT,
		request_id TEXT,
		telegram_message_id INTEGER,
		status TEXT NOT NULL,
		expires_at TEXT,
		payload_json TEXT NOT NULL,
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS delivery_queue (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL,
		chat_key TEXT NOT NULL,
		chat_id INTEGER NOT NULL,
		topic_id INTEGER NOT NULL,
		thread_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		status TEXT NOT NULL,
		retry_count INTEGER NOT NULL DEFAULT 0,
		available_at TEXT NOT NULL,
		last_error TEXT,
		payload_json TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE UNIQUE INDEX IF NOT EXISTS idx_delivery_queue_event_target
		ON delivery_queue(event_id, chat_key);

	CREATE TABLE IF NOT EXISTS delivery_attempts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		queue_id INTEGER NOT NULL,
		attempt_no INTEGER NOT NULL,
		status TEXT NOT NULL,
		error_text TEXT,
		created_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS daemon_state (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE IF NOT EXISTS external_launch_requests (
		id TEXT PRIMARY KEY,
		source TEXT NOT NULL,
		external_id TEXT NOT NULL,
		sender TEXT NOT NULL,
		title TEXT NOT NULL,
		safe_preview TEXT,
		source_url TEXT,
		prompt TEXT NOT NULL,
		cwd TEXT,
		model TEXT,
		reasoning_effort TEXT,
		status TEXT NOT NULL,
		telegram_topic_id INTEGER NOT NULL,
		telegram_message_id INTEGER NOT NULL DEFAULT 0,
		telegram_rendered_status TEXT,
		thread_id TEXT,
		turn_id TEXT,
		auto_start INTEGER NOT NULL DEFAULT 0,
		source_chat_id TEXT,
		source_message_id INTEGER NOT NULL DEFAULT 0,
		source_thread_id INTEGER NOT NULL DEFAULT 0,
		ack_status TEXT,
		ack_text TEXT,
		ack_message_id INTEGER NOT NULL DEFAULT 0,
		ack_attempts INTEGER NOT NULL DEFAULT 0,
		ack_available_at TEXT,
		ack_error TEXT,
		reply_status TEXT,
		reply_text TEXT,
		reply_message_id INTEGER NOT NULL DEFAULT 0,
		reply_attempts INTEGER NOT NULL DEFAULT 0,
		reply_available_at TEXT,
		reply_error TEXT,
		error_type TEXT,
		error_summary TEXT,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(source, external_id)
	);

	CREATE TABLE IF NOT EXISTS sync_state (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		session_id TEXT NOT NULL,
		chat_id INTEGER NOT NULL,
		state TEXT NOT NULL,
		security_state TEXT NOT NULL,
		snapshot_at TEXT,
		activation_summary_json TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		ended_at TEXT
	);

	CREATE TABLE IF NOT EXISTS sync_topics (
		session_id TEXT NOT NULL,
		chat_id INTEGER NOT NULL,
		topic_id INTEGER NOT NULL,
		thread_id TEXT NOT NULL,
		rank INTEGER NOT NULL DEFAULT 0,
		title TEXT NOT NULL,
		telegram_state TEXT NOT NULL,
		status_message_id INTEGER NOT NULL DEFAULT 0,
		status_turn_id TEXT,
		last_render_fp TEXT,
		last_final_fp TEXT,
		last_user_fp TEXT,
		pending_telegram_user_fp TEXT,
		pending_telegram_turn_id TEXT,
		active_turn_id TEXT,
		active_turn_state TEXT,
		writer_generation INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(session_id, topic_id),
		UNIQUE(session_id, thread_id)
	);

	CREATE TABLE IF NOT EXISTS sync_topic_drafts (
		session_id TEXT NOT NULL,
		chat_id INTEGER NOT NULL,
		topic_id INTEGER NOT NULL,
		rank INTEGER NOT NULL DEFAULT 0,
		title TEXT NOT NULL,
		cwd TEXT NOT NULL,
		project_name TEXT,
		directory_name TEXT,
		state TEXT NOT NULL,
		source_message_id INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(session_id, topic_id)
	);

	CREATE TABLE IF NOT EXISTS sync_message_receipts (
		chat_id INTEGER NOT NULL,
		topic_id INTEGER NOT NULL,
		message_id INTEGER NOT NULL,
		session_id TEXT NOT NULL,
		thread_id TEXT NOT NULL,
		state TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(chat_id, topic_id, message_id)
	);

	CREATE INDEX IF NOT EXISTS idx_threads_updated_at ON threads(updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_threads_project_updated_at ON threads(project_name, updated_at DESC);
	CREATE INDEX IF NOT EXISTS idx_delivery_queue_status_available_at ON delivery_queue(status, available_at);
	CREATE INDEX IF NOT EXISTS idx_external_launch_status_updated_at ON external_launch_requests(status, updated_at);
	CREATE INDEX IF NOT EXISTS idx_sync_topics_session_state ON sync_topics(session_id, telegram_state, rank);
	CREATE INDEX IF NOT EXISTS idx_sync_topic_drafts_session_state ON sync_topic_drafts(session_id, state, rank);
	CREATE INDEX IF NOT EXISTS idx_sync_receipts_session_thread ON sync_message_receipts(session_id, thread_id, updated_at);
	`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return err
	}
	// This obsolete cache captured every top-level Messenger message observed by
	// the robot. Thread roots are now fetched on demand from History API.
	if _, err := s.db.ExecContext(ctx, `DROP TABLE IF EXISTS external_source_messages`); err != nil {
		return err
	}

	if err := s.ensureColumn(ctx, "sync_topics", "active_turn_id", `ALTER TABLE sync_topics ADD COLUMN active_turn_id TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sync_topics", "active_turn_state", `ALTER TABLE sync_topics ADD COLUMN active_turn_state TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sync_topics", "writer_generation", `ALTER TABLE sync_topics ADD COLUMN writer_generation INTEGER NOT NULL DEFAULT 0`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sync_topics", "status_turn_id", `ALTER TABLE sync_topics ADD COLUMN status_turn_id TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sync_topics", "last_user_fp", `ALTER TABLE sync_topics ADD COLUMN last_user_fp TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sync_topics", "pending_telegram_user_fp", `ALTER TABLE sync_topics ADD COLUMN pending_telegram_user_fp TEXT`); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "sync_topics", "pending_telegram_turn_id", `ALTER TABLE sync_topics ADD COLUMN pending_telegram_turn_id TEXT`); err != nil {
		return err
	}
	externalColumns := []struct {
		name string
		sql  string
	}{
		{"auto_start", `ALTER TABLE external_launch_requests ADD COLUMN auto_start INTEGER NOT NULL DEFAULT 0`},
		{"source_chat_id", `ALTER TABLE external_launch_requests ADD COLUMN source_chat_id TEXT`},
		{"source_message_id", `ALTER TABLE external_launch_requests ADD COLUMN source_message_id INTEGER NOT NULL DEFAULT 0`},
		{"source_thread_id", `ALTER TABLE external_launch_requests ADD COLUMN source_thread_id INTEGER NOT NULL DEFAULT 0`},
		{"ack_status", `ALTER TABLE external_launch_requests ADD COLUMN ack_status TEXT`},
		{"ack_text", `ALTER TABLE external_launch_requests ADD COLUMN ack_text TEXT`},
		{"ack_message_id", `ALTER TABLE external_launch_requests ADD COLUMN ack_message_id INTEGER NOT NULL DEFAULT 0`},
		{"ack_attempts", `ALTER TABLE external_launch_requests ADD COLUMN ack_attempts INTEGER NOT NULL DEFAULT 0`},
		{"ack_available_at", `ALTER TABLE external_launch_requests ADD COLUMN ack_available_at TEXT`},
		{"ack_error", `ALTER TABLE external_launch_requests ADD COLUMN ack_error TEXT`},
		{"reply_status", `ALTER TABLE external_launch_requests ADD COLUMN reply_status TEXT`},
		{"reply_text", `ALTER TABLE external_launch_requests ADD COLUMN reply_text TEXT`},
		{"reply_message_id", `ALTER TABLE external_launch_requests ADD COLUMN reply_message_id INTEGER NOT NULL DEFAULT 0`},
		{"reply_attempts", `ALTER TABLE external_launch_requests ADD COLUMN reply_attempts INTEGER NOT NULL DEFAULT 0`},
		{"reply_available_at", `ALTER TABLE external_launch_requests ADD COLUMN reply_available_at TEXT`},
		{"reply_error", `ALTER TABLE external_launch_requests ADD COLUMN reply_error TEXT`},
		{"model", `ALTER TABLE external_launch_requests ADD COLUMN model TEXT`},
		{"reasoning_effort", `ALTER TABLE external_launch_requests ADD COLUMN reasoning_effort TEXT`},
	}
	for _, column := range externalColumns {
		if err := s.ensureColumn(ctx, "external_launch_requests", column.name, column.sql); err != nil {
			return err
		}
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_external_launch_auto_start ON external_launch_requests(auto_start, status, updated_at)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_external_reply_status_available ON external_launch_requests(reply_status, reply_available_at)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_external_ack_status_available ON external_launch_requests(ack_status, ack_available_at)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET status_turn_id=(
		SELECT last_seen_turn_id FROM thread_snapshots WHERE thread_snapshots.thread_id=sync_topics.thread_id
	) WHERE status_message_id<>0 AND coalesce(status_turn_id,'')='' AND EXISTS (
		SELECT 1 FROM thread_snapshots WHERE thread_snapshots.thread_id=sync_topics.thread_id
		AND coalesce(thread_snapshots.last_seen_turn_id,'')<>''
	)`); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE sync_topics SET last_user_fp=(
		SELECT coalesce(json_extract(thread_snapshots.snapshot_json, '$.compact_json.LatestUserMessageFP'),'')
		FROM thread_snapshots WHERE thread_snapshots.thread_id=sync_topics.thread_id
	) WHERE status_message_id<>0 AND coalesce(last_user_fp,'')='' AND EXISTS (
		SELECT 1 FROM thread_snapshots WHERE thread_snapshots.thread_id=sync_topics.thread_id
		AND coalesce(json_extract(thread_snapshots.snapshot_json, '$.compact_json.LatestUserMessageFP'),'')<>''
	)`); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureColumn(ctx context.Context, tableName, columnName, alterSQL string) error {
	exists, err := s.hasColumn(ctx, tableName, columnName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, alterSQL); err != nil {
		return err
	}
	exists, err = s.hasColumn(ctx, tableName, columnName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("column %s.%s was not created", tableName, columnName)
	}
	return nil
}

func (s *Store) hasColumn(ctx context.Context, tableName, columnName string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if strings.EqualFold(name, columnName) {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) UpsertThread(ctx context.Context, thread model.Thread) error {
	raw := thread.Raw
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	raw = redactTelegramBotCredentials(raw)
	if existing, err := s.GetThread(ctx, thread.ID); err == nil && existing != nil && threadIndexEqual(*existing, thread, raw) {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
	INSERT INTO threads(thread_id, title, cwd, project_name, directory_name, updated_at, status, last_preview, active_turn_id, preferred_model, permissions_mode, archived, raw_json)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(thread_id) DO UPDATE SET
		title = excluded.title,
		cwd = excluded.cwd,
		project_name = excluded.project_name,
		directory_name = excluded.directory_name,
		updated_at = excluded.updated_at,
		status = excluded.status,
		last_preview = excluded.last_preview,
		active_turn_id = excluded.active_turn_id,
		preferred_model = excluded.preferred_model,
		permissions_mode = excluded.permissions_mode,
		archived = excluded.archived,
		raw_json = excluded.raw_json`,
		thread.ID, thread.Title, nullable(thread.CWD), thread.ProjectName, nullable(thread.DirectoryName), thread.UpdatedAt,
		nullable(thread.Status), nullable(thread.LastPreview), nullable(thread.ActiveTurnID), nullable(thread.PreferredModel),
		nullable(thread.PermissionsMode), boolToInt(thread.Archived), string(raw),
	)
	return err
}

func threadIndexEqual(existing, current model.Thread, redactedRaw []byte) bool {
	return existing.ID == current.ID &&
		existing.Title == current.Title &&
		existing.CWD == current.CWD &&
		existing.ProjectName == current.ProjectName &&
		existing.DirectoryName == current.DirectoryName &&
		existing.UpdatedAt == current.UpdatedAt &&
		existing.Status == current.Status &&
		existing.LastPreview == current.LastPreview &&
		existing.ActiveTurnID == current.ActiveTurnID &&
		existing.PreferredModel == current.PreferredModel &&
		existing.PermissionsMode == current.PermissionsMode &&
		existing.Archived == current.Archived &&
		string(existing.Raw) == string(redactedRaw)
}

func (s *Store) GetThread(ctx context.Context, threadID string) (*model.Thread, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT thread_id, title, cwd, project_name, directory_name, updated_at, status, last_preview, active_turn_id, preferred_model, permissions_mode, archived, raw_json
	FROM threads WHERE thread_id = ?`, threadID)
	return scanThread(row)
}

func (s *Store) ListThreads(ctx context.Context, limit int, search string) ([]model.Thread, error) {
	if limit <= 0 {
		limit = 10
	}
	query := `
	SELECT thread_id, title, cwd, project_name, directory_name, updated_at, status, last_preview, active_turn_id, preferred_model, permissions_mode, archived, raw_json
	FROM threads WHERE ` + visibleThreadPredicateSQL
	args := make([]any, 0, 2)
	if trimmed := strings.TrimSpace(search); trimmed != "" {
		query += ` AND (lower(title) LIKE ? OR lower(project_name) LIKE ? OR lower(last_preview) LIKE ? OR lower(thread_id) LIKE ?)`
		pattern := "%" + strings.ToLower(trimmed) + "%"
		args = append(args, pattern, pattern, pattern, pattern)
	}
	query += ` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Thread{}
	for rows.Next() {
		thread, err := scanThread(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *thread)
	}
	return out, rows.Err()
}

const visibleThreadPredicateSQL = `(
	lower(trim(cast(coalesce(json_extract(raw_json, '$.ephemeral'), json_extract(raw_json, '$.thread.ephemeral'), '') as text))) NOT IN ('1', 'true', 'yes')
	AND trim(cast(coalesce(json_extract(raw_json, '$.source.subAgent'), json_extract(raw_json, '$.thread.source.subAgent'), '') as text)) = ''
)`

func (s *Store) CountThreads(ctx context.Context) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT count(*) FROM threads`)
	var count int
	return count, row.Scan(&count)
}

func (s *Store) ListProjectGroups(ctx context.Context) (map[string][]model.Thread, error) {
	rows, err := s.ListThreads(ctx, 500, "")
	if err != nil {
		return nil, err
	}
	grouped := map[string][]model.Thread{}
	for _, thread := range rows {
		grouped[thread.ProjectName] = append(grouped[thread.ProjectName], thread)
	}
	return grouped, nil
}

func (s *Store) UpsertSnapshot(ctx context.Context, threadID string, snapshot model.ThreadSnapshotState) error {
	var existingPayload string
	if err := s.db.QueryRowContext(ctx, `SELECT snapshot_json FROM thread_snapshots WHERE thread_id = ?`, threadID).Scan(&existingPayload); err == nil {
		var existing model.ThreadSnapshotState
		if json.Unmarshal([]byte(existingPayload), &existing) == nil && snapshotsEqualIgnoringPoll(existing, snapshot) {
			_, updateErr := s.db.ExecContext(ctx, `
			UPDATE thread_snapshots SET last_poll_at = ?, updated_at = ? WHERE thread_id = ?`,
				nullable(string(snapshot.LastPollAt)), string(model.NowString()), threadID)
			return updateErr
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	payload = redactTelegramBotCredentials(payload)
	updatedAt := string(model.NowString())
	_, err = s.db.ExecContext(ctx, `
	INSERT INTO thread_snapshots(
		thread_id, last_live_event_at, last_poll_at, next_poll_after, last_seen_thread_status, last_seen_turn_id, last_seen_turn_status,
		last_progress_fp, last_progress_sent_at, last_final_fp, last_completion_fp, last_approval_fp, snapshot_json, updated_at
	)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(thread_id) DO UPDATE SET
		last_live_event_at = excluded.last_live_event_at,
		last_poll_at = excluded.last_poll_at,
		next_poll_after = excluded.next_poll_after,
		last_seen_thread_status = excluded.last_seen_thread_status,
		last_seen_turn_id = excluded.last_seen_turn_id,
		last_seen_turn_status = excluded.last_seen_turn_status,
		last_progress_fp = excluded.last_progress_fp,
		last_progress_sent_at = excluded.last_progress_sent_at,
		last_final_fp = excluded.last_final_fp,
		last_completion_fp = excluded.last_completion_fp,
		last_approval_fp = excluded.last_approval_fp,
		snapshot_json = excluded.snapshot_json,
		updated_at = excluded.updated_at`,
		threadID,
		nullable(string(snapshot.LastRichLiveEventAt)),
		nullable(string(snapshot.LastPollAt)),
		nullable(string(snapshot.NextPollAfter)),
		nullable(snapshot.LastSeenThreadStatus),
		nullable(snapshot.LastSeenTurnID),
		nullable(snapshot.LastSeenTurnStatus),
		nullable(snapshot.LastProgressFP),
		nullable(string(snapshot.LastProgressSentAt)),
		nullable(snapshot.LastFinalFP),
		nullable(snapshot.LastCompletionFP),
		nullable(snapshot.LastApprovalFP),
		string(payload),
		updatedAt,
	)
	return err
}

func snapshotsEqualIgnoringPoll(left, right model.ThreadSnapshotState) bool {
	left.LastPollAt = ""
	right.LastPollAt = ""
	return reflect.DeepEqual(left, right)
}

func (s *Store) GetSnapshot(ctx context.Context, threadID string) (*model.ThreadSnapshotState, error) {
	row := s.db.QueryRowContext(ctx, `SELECT snapshot_json, last_poll_at FROM thread_snapshots WHERE thread_id = ?`, threadID)
	var payload string
	var lastPollAt sql.NullString
	if err := row.Scan(&payload, &lastPollAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	var snapshot model.ThreadSnapshotState
	if err := json.Unmarshal([]byte(payload), &snapshot); err != nil {
		return nil, err
	}
	snapshot.LastPollAt = model.TimeString(lastPollAt.String)
	return &snapshot, nil
}

func (s *Store) MarkLiveEvent(ctx context.Context, threadID string, when model.TimeString) error {
	snapshot, err := s.GetSnapshot(ctx, threadID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		snapshot = &model.ThreadSnapshotState{}
	}
	snapshot.LastRichLiveEventAt = when
	return s.UpsertSnapshot(ctx, threadID, *snapshot)
}

func (s *Store) PutCallbackRoute(ctx context.Context, route model.CallbackRoute) error {
	_, err := s.db.ExecContext(ctx, `
	INSERT OR REPLACE INTO callback_routes(route_token, action, thread_id, turn_id, request_id, telegram_message_id, status, expires_at, payload_json, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		route.Token, route.Action, route.ThreadID, nullable(route.TurnID), nullable(route.RequestID), route.TelegramMessageID, route.Status, nullable(route.ExpiresAt), route.PayloadJSON, route.CreatedAt,
	)
	return err
}

func (s *Store) GetCallbackRoute(ctx context.Context, token string) (*model.CallbackRoute, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT route_token, action, thread_id, coalesce(turn_id,''), coalesce(request_id,''), coalesce(telegram_message_id,0), status, coalesce(expires_at,''), payload_json, created_at
	FROM callback_routes WHERE route_token = ?`, token)
	var route model.CallbackRoute
	err := row.Scan(&route.Token, &route.Action, &route.ThreadID, &route.TurnID, &route.RequestID, &route.TelegramMessageID, &route.Status, &route.ExpiresAt, &route.PayloadJSON, &route.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &route, nil
}

func (s *Store) ExpireCallbackRoute(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE callback_routes SET status = ? WHERE route_token = ?`, model.CallbackStatusExpired, token)
	return err
}

func (s *Store) ExpireSyncCallbackRoutes(ctx context.Context, threadID, turnID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE callback_routes SET status=? WHERE action LIKE 'sync_%' AND thread_id=? AND turn_id=?`,
		model.CallbackStatusExpired, threadID, turnID)
	return err
}

func (s *Store) ExpireSyncCallbackRoutesByRequest(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE callback_routes SET status=? WHERE action LIKE 'sync_%' AND request_id=?`,
		model.CallbackStatusExpired, requestID)
	return err
}

func (s *Store) EnqueueDelivery(ctx context.Context, item model.DeliveryQueueItem) error {
	return enqueueDelivery(ctx, s.db, item)
}

type deliveryExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func enqueueDelivery(ctx context.Context, execer deliveryExecer, item model.DeliveryQueueItem) error {
	now := string(model.NowString())
	if item.AvailableAt == "" {
		item.AvailableAt = model.TimeString(now)
	}
	if item.CreatedAt == "" {
		item.CreatedAt = model.TimeString(now)
	}
	if item.UpdatedAt == "" {
		item.UpdatedAt = model.TimeString(now)
	}
	_, err := execer.ExecContext(ctx, `
	INSERT INTO delivery_queue(event_id, chat_key, chat_id, topic_id, thread_id, kind, status, retry_count, available_at, last_error, payload_json, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(event_id, chat_key) DO NOTHING`,
		item.EventID, item.ChatKey, item.ChatID, item.TopicID, item.ThreadID, item.Kind, item.Status, item.RetryCount, item.AvailableAt, nullable(item.LastError), item.PayloadJSON, item.CreatedAt, item.UpdatedAt,
	)
	return err
}

func (s *Store) ClaimDeliveryBatch(ctx context.Context, limit int) ([]model.DeliveryQueueItem, error) {
	if limit <= 0 {
		limit = 10
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, `
	SELECT id, event_id, chat_key, chat_id, topic_id, thread_id, kind, status, retry_count, available_at, coalesce(last_error,''), payload_json, created_at, updated_at
	FROM delivery_queue
	WHERE status IN ('pending', 'retry') AND available_at <= ?
	ORDER BY id
	LIMIT ?`, string(model.NowString()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.DeliveryQueueItem{}
	ids := []int64{}
	for rows.Next() {
		var item model.DeliveryQueueItem
		if err := rows.Scan(&item.ID, &item.EventID, &item.ChatKey, &item.ChatID, &item.TopicID, &item.ThreadID, &item.Kind, &item.Status, &item.RetryCount, &item.AvailableAt, &item.LastError, &item.PayloadJSON, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
		ids = append(ids, item.ID)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_queue SET status = ?, updated_at = ? WHERE id = ?`, model.DeliveryStatusProcessing, string(model.NowString()), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) CompleteDelivery(ctx context.Context, queueID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE delivery_queue SET status = ?, updated_at = ? WHERE id = ?`, model.DeliveryStatusDelivered, string(model.NowString()), queueID)
	return err
}

func (s *Store) SupersedeDelivery(ctx context.Context, queueID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE delivery_queue SET status = ?, updated_at = ? WHERE id = ?`, model.DeliveryStatusSuperseded, string(model.NowString()), queueID)
	return err
}

func (s *Store) RetireUnsupportedTelegramDeliveries(ctx context.Context, syncGroupID int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer rollback(tx)
	now := string(model.NowString())
	if _, err := tx.ExecContext(ctx, `
	UPDATE delivery_queue
	SET status = ?, available_at = ?, updated_at = ?
	WHERE status = ? AND chat_id = ? AND kind IN ('health', 'external_terminal', 'sync_activation')`,
		model.DeliveryStatusRetry, now, now, model.DeliveryStatusProcessing, syncGroupID); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
	UPDATE delivery_queue
	SET status = ?, updated_at = ?
	WHERE status IN (?, ?, ?)
	  AND (chat_id <> ? OR kind NOT IN ('health', 'external_terminal', 'sync_activation'))`,
		model.DeliveryStatusSuperseded,
		now,
		model.DeliveryStatusPending,
		model.DeliveryStatusRetry,
		model.DeliveryStatusProcessing,
		syncGroupID,
	)
	if err != nil {
		return 0, err
	}
	retired, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return retired, nil
}

func (s *Store) DeliveryStatusForEvent(ctx context.Context, eventID, chatKey string) (string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT status FROM delivery_queue WHERE event_id = ? AND chat_key = ?`, strings.TrimSpace(eventID), strings.TrimSpace(chatKey))
	var status string
	if err := row.Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return status, nil
}

func (s *Store) FailDelivery(ctx context.Context, queueID int64, retryCount int, availableAt time.Time, errText string, dead bool) error {
	status := model.DeliveryStatusRetry
	if dead {
		status = model.DeliveryStatusDead
	}
	_, err := s.db.ExecContext(ctx, `
	UPDATE delivery_queue SET status = ?, retry_count = ?, available_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		status, retryCount, availableAt.UTC().Format(time.RFC3339Nano), nullable(errText), string(model.NowString()), queueID,
	)
	return err
}

func (s *Store) RecordDeliveryAttempt(ctx context.Context, queueID int64, attemptNo int, status, errText string) error {
	_, err := s.db.ExecContext(ctx, `
	INSERT INTO delivery_attempts(queue_id, attempt_no, status, error_text, created_at)
	VALUES (?, ?, ?, ?, ?)`,
		queueID, attemptNo, status, nullable(errText), string(model.NowString()),
	)
	return err
}

func (s *Store) DeliveryQueueBacklog(ctx context.Context) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_queue WHERE status IN ('pending', 'retry', 'processing')`)
	var count int
	return count, row.Scan(&count)
}

func (s *Store) DeliveryQueueDeadCount(ctx context.Context) (int, error) {
	row := s.db.QueryRowContext(ctx, `SELECT count(*) FROM delivery_queue WHERE status = 'dead'`)
	var count int
	return count, row.Scan(&count)
}

func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
	INSERT INTO daemon_state(key, value, updated_at) VALUES (?, ?, ?)
	ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, string(model.NowString()),
	)
	return err
}

func (s *Store) GetState(ctx context.Context, key string) (string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM daemon_state WHERE key = ?`, key)
	var value string
	err := row.Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

func (s *Store) DeleteState(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM daemon_state WHERE key = ?`, key)
	return err
}

func (s *Store) ListState(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM daemon_state ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		out[key] = value
	}
	return out, rows.Err()
}

func scanThread(scanner interface{ Scan(...any) error }) (*model.Thread, error) {
	var thread model.Thread
	var cwd, directoryName, status, lastPreview, activeTurnID, preferredModel, permissionsMode sql.NullString
	var raw string
	var archived int
	if err := scanner.Scan(&thread.ID, &thread.Title, &cwd, &thread.ProjectName, &directoryName, &thread.UpdatedAt, &status, &lastPreview, &activeTurnID, &preferredModel, &permissionsMode, &archived, &raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	thread.CWD = cwd.String
	thread.DirectoryName = directoryName.String
	thread.Status = status.String
	thread.LastPreview = lastPreview.String
	thread.ActiveTurnID = activeTurnID.String
	thread.PreferredModel = preferredModel.String
	thread.PermissionsMode = permissionsMode.String
	thread.Archived = archived == 1
	thread.Raw = json.RawMessage(raw)
	return &thread, nil
}

func nullable(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rollback(tx *sql.Tx) {
	_ = tx.Rollback()
}

func MustJSON(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Errorf("marshal payload: %w", err))
	}
	return string(payload)
}
