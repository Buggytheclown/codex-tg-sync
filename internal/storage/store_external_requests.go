package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func externalSourceCursorKey(source string) string {
	return "external_source." + strings.TrimSpace(source) + ".cursor"
}

func validateExternalLaunchRequest(request model.ExternalLaunchRequest, source string) error {
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.Source) != source || strings.TrimSpace(request.ExternalID) == "" {
		return errors.New("external launch request requires id, matching source, and external id")
	}
	if strings.TrimSpace(request.Sender) == "" || strings.TrimSpace(request.Title) == "" || strings.TrimSpace(request.Prompt) == "" {
		return errors.New("external launch request requires sender, title, and prompt")
	}
	if request.Status != model.ExternalLaunchPendingApproval {
		return fmt.Errorf("external launch request initial status must be %s", model.ExternalLaunchPendingApproval)
	}
	if request.TelegramTopicID == 0 || strings.TrimSpace(string(request.CreatedAt)) == "" || strings.TrimSpace(string(request.UpdatedAt)) == "" {
		return errors.New("external launch request requires topic and timestamps")
	}
	return nil
}

func (s *Store) IngestExternalLaunchRequests(ctx context.Context, source string, cursor int64, requests []model.ExternalLaunchRequest) (int, error) {
	source = strings.TrimSpace(source)
	if source == "" || cursor < 0 {
		return 0, errors.New("external source and non-negative cursor are required")
	}
	for _, request := range requests {
		if err := validateExternalLaunchRequest(request, source); err != nil {
			return 0, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	created := 0
	for _, request := range requests {
		result, err := tx.ExecContext(ctx, `
		INSERT INTO external_launch_requests(
			id, source, external_id, sender, title, safe_preview, source_url, prompt, cwd, status,
			telegram_topic_id, telegram_message_id, telegram_rendered_status, thread_id, turn_id,
			error_type, error_summary, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, external_id) DO NOTHING`,
			request.ID, request.Source, request.ExternalID, request.Sender, request.Title, nullable(request.SafePreview), nullable(request.SourceURL),
			request.Prompt, nullable(request.CWD), request.Status, request.TelegramTopicID, request.TelegramMessageID,
			nullable(request.TelegramRenderedStatus), nullable(request.ThreadID), nullable(request.TurnID), nullable(request.ErrorType),
			nullable(request.ErrorSummary), request.CreatedAt, request.UpdatedAt)
		if err != nil {
			return 0, err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		created += int(rows)
	}
	now := model.NowString()
	if _, err := tx.ExecContext(ctx, `
	INSERT INTO daemon_state(key, value, updated_at) VALUES (?, ?, ?)
	ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
		externalSourceCursorKey(source), strconv.FormatInt(cursor, 10), now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return created, nil
}

func (s *Store) IngestExternalRequests(ctx context.Context, source string, cursor int64, requests []model.ExternalLaunchRequest) (int, error) {
	return s.IngestExternalLaunchRequests(ctx, source, cursor, requests)
}

func (s *Store) GetExternalSourceCursor(ctx context.Context, source string) (int64, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value FROM daemon_state WHERE key=?`, externalSourceCursorKey(source))
	var raw string
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	cursor, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse external source cursor: %w", err)
	}
	return cursor, nil
}

func (s *Store) ExternalSourceCursor(ctx context.Context, source string) (int64, error) {
	return s.GetExternalSourceCursor(ctx, source)
}

func (s *Store) GetExternalLaunchRequest(ctx context.Context, id string) (*model.ExternalLaunchRequest, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT id, source, external_id, sender, title, coalesce(safe_preview,''), coalesce(source_url,''), prompt, coalesce(cwd,''), status,
		telegram_topic_id, telegram_message_id, coalesce(telegram_rendered_status,''), coalesce(thread_id,''), coalesce(turn_id,''),
		coalesce(error_type,''), coalesce(error_summary,''), created_at, updated_at
	FROM external_launch_requests WHERE id=?`, id)
	var request model.ExternalLaunchRequest
	if err := row.Scan(&request.ID, &request.Source, &request.ExternalID, &request.Sender, &request.Title, &request.SafePreview,
		&request.SourceURL, &request.Prompt, &request.CWD, &request.Status, &request.TelegramTopicID, &request.TelegramMessageID,
		&request.TelegramRenderedStatus, &request.ThreadID, &request.TurnID, &request.ErrorType, &request.ErrorSummary,
		&request.CreatedAt, &request.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &request, nil
}

func (s *Store) ListExternalLaunchRequestsForTelegram(ctx context.Context, limit int) ([]model.ExternalLaunchRequest, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
	SELECT id, source, external_id, sender, title, coalesce(safe_preview,''), coalesce(source_url,''), prompt, coalesce(cwd,''), status,
		telegram_topic_id, telegram_message_id, coalesce(telegram_rendered_status,''), coalesce(thread_id,''), coalesce(turn_id,''),
		coalesce(error_type,''), coalesce(error_summary,''), created_at, updated_at
	FROM external_launch_requests
	WHERE telegram_message_id=0 OR coalesce(telegram_rendered_status,'') != status
	ORDER BY created_at, id
	LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]model.ExternalLaunchRequest, 0)
	for rows.Next() {
		var request model.ExternalLaunchRequest
		if err := rows.Scan(&request.ID, &request.Source, &request.ExternalID, &request.Sender, &request.Title, &request.SafePreview,
			&request.SourceURL, &request.Prompt, &request.CWD, &request.Status, &request.TelegramTopicID, &request.TelegramMessageID,
			&request.TelegramRenderedStatus, &request.ThreadID, &request.TurnID, &request.ErrorType, &request.ErrorSummary,
			&request.CreatedAt, &request.UpdatedAt); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (s *Store) MarkExternalLaunchRequestTelegramSent(ctx context.Context, id string, messageID int64, renderedStatus string) error {
	if messageID == 0 || strings.TrimSpace(renderedStatus) == "" {
		return errors.New("telegram message id and rendered status are required")
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET telegram_message_id=?, telegram_rendered_status=?, updated_at=?
	WHERE id=? AND telegram_message_id=0 AND status=?`, messageID, renderedStatus, model.NowString(), id, renderedStatus)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("external launch request changed before Telegram send was recorded")
	}
	return nil
}

func (s *Store) MarkExternalLaunchRequestTelegramRendered(ctx context.Context, id string, messageID int64, status string) error {
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET telegram_rendered_status=?, updated_at=?
	WHERE id=? AND telegram_message_id=? AND status=?`, status, model.NowString(), id, messageID, status)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("external launch request changed before Telegram edit was recorded")
	}
	return nil
}

func (s *Store) ClaimExternalLaunchRequest(ctx context.Context, id string) (bool, error) {
	return s.transitionExternalLaunchRequest(ctx, id, model.ExternalLaunchPendingApproval, model.ExternalLaunchStarting)
}

func (s *Store) DismissExternalLaunchRequest(ctx context.Context, id string) (bool, error) {
	return s.transitionExternalLaunchRequest(ctx, id, model.ExternalLaunchPendingApproval, model.ExternalLaunchDismissed)
}

func (s *Store) transitionExternalLaunchRequest(ctx context.Context, id, from, to string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE external_launch_requests SET status=?, updated_at=? WHERE id=? AND status=?`,
		to, model.NowString(), id, from)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) ExpireExternalLaunchCallbackRoutes(ctx context.Context, requestID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE callback_routes SET status=? WHERE action LIKE 'external_launch_%' AND request_id=?`,
		model.CallbackStatusExpired, requestID)
	return err
}

func (s *Store) CompleteExternalLaunchRequest(ctx context.Context, id, status, threadID, turnID, errorType, errorSummary string) (bool, error) {
	switch status {
	case model.ExternalLaunchSessionStarted:
		if strings.TrimSpace(threadID) == "" || strings.TrimSpace(turnID) == "" {
			return false, errors.New("started external launch request requires thread and turn ids")
		}
	case model.ExternalLaunchFailed, model.ExternalLaunchOutcomeUnknown:
	default:
		return false, errors.New("invalid terminal external launch request status")
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET status=?, thread_id=?, turn_id=?, error_type=?, error_summary=?, updated_at=?
	WHERE id=? AND status=?`, status, nullable(threadID), nullable(turnID), nullable(errorType), nullable(errorSummary),
		model.NowString(), id, model.ExternalLaunchStarting)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) ResetExternalLaunchRequestPending(ctx context.Context, id, errorType, errorSummary string) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET status=?, error_type=?, error_summary=?, updated_at=?
	WHERE id=? AND status=?`, model.ExternalLaunchPendingApproval, nullable(errorType), nullable(errorSummary), model.NowString(),
		id, model.ExternalLaunchStarting)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) NoteExternalLaunchRequestPendingError(ctx context.Context, id, errorType, errorSummary string) error {
	_, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET error_type=?, error_summary=?, telegram_rendered_status='', updated_at=?
	WHERE id=? AND status=?`, nullable(errorType), nullable(errorSummary), model.NowString(), id, model.ExternalLaunchPendingApproval)
	return err
}

func (s *Store) RecoverStartingExternalLaunchRequests(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET status=?, error_type='daemon_restart', error_summary='Daemon restarted after dispatch claim; outcome is unknown and was not replayed.', updated_at=?
	WHERE status=?`, model.ExternalLaunchOutcomeUnknown, model.NowString(), model.ExternalLaunchStarting)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
