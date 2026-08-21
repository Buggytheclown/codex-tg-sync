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
