package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

const externalTelegramVisibilityPending = "__visibility_required__"

func externalSourceCursorKey(source string) string {
	return "external_source." + strings.TrimSpace(source) + ".cursor"
}

func validateExternalLaunchRequest(request model.ExternalLaunchRequest, source string) error {
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.Source) != source || strings.TrimSpace(request.ExternalID) == "" {
		return errors.New("external launch request requires id, matching source, and external id")
	}
	if strings.TrimSpace(request.Sender) == "" || strings.TrimSpace(request.Title) == "" {
		return errors.New("external launch request requires sender and title")
	}
	if request.Status != model.ExternalLaunchPendingApproval && request.Status != model.ExternalLaunchRejectedSender {
		return fmt.Errorf("invalid initial external launch request status %q", request.Status)
	}
	if request.Status == model.ExternalLaunchPendingApproval && strings.TrimSpace(request.Prompt) == "" {
		return errors.New("accepted external launch request requires prompt")
	}
	if request.Status == model.ExternalLaunchPendingApproval && !request.AutoStart && request.TelegramTopicID == 0 {
		return errors.New("accepted external launch request requires approval topic unless auto-starting")
	}
	if request.Status == model.ExternalLaunchRejectedSender && (request.AutoStart || request.TelegramTopicID != 0 || strings.TrimSpace(request.Prompt) != "") {
		return errors.New("rejected sender request must not contain a Codex launch route or prompt")
	}
	if strings.TrimSpace(string(request.CreatedAt)) == "" || strings.TrimSpace(string(request.UpdatedAt)) == "" {
		return errors.New("external launch request requires approval topic unless auto-starting, and timestamps")
	}
	if (strings.TrimSpace(request.SourceChatID) == "") != (request.SourceMessageID == 0) {
		return errors.New("external launch request reply target requires both chat and message")
	}
	if request.Status == model.ExternalLaunchRejectedSender && (strings.TrimSpace(request.AckStatus) != model.ExternalReplyPending || strings.TrimSpace(request.AckText) == "" || strings.TrimSpace(request.SourceChatID) == "") {
		return errors.New("rejected sender request requires a pending acknowledgement and reply target")
	}
	if strings.TrimSpace(request.AckStatus) != "" && (request.AckStatus != model.ExternalReplyPending || strings.TrimSpace(request.AckText) == "" || strings.TrimSpace(request.SourceChatID) == "") {
		return errors.New("external request acknowledgement requires pending status, text, and reply target")
	}
	return nil
}

func (s *Store) IngestExternalLaunchRequests(ctx context.Context, source string, cursor int64, requests []model.ExternalLaunchRequest) (int, error) {
	return s.writeExternalLaunchRequests(ctx, source, requests, &cursor)
}

func (s *Store) EnqueueExternalLaunchRequests(ctx context.Context, source string, requests []model.ExternalLaunchRequest) (int, error) {
	return s.writeExternalLaunchRequests(ctx, source, requests, nil)
}

func (s *Store) writeExternalLaunchRequests(ctx context.Context, source string, requests []model.ExternalLaunchRequest, cursor *int64) (int, error) {
	source = strings.TrimSpace(source)
	if source == "" || (cursor != nil && *cursor < 0) {
		return 0, errors.New("external source and non-negative cursor are required when advancing a cursor")
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
		telegramRenderedStatus := request.TelegramRenderedStatus
		if request.AutoStart && request.TelegramTopicID != 0 && strings.TrimSpace(telegramRenderedStatus) == "" {
			telegramRenderedStatus = externalTelegramVisibilityPending
		}
		result, err := tx.ExecContext(ctx, `
		INSERT INTO external_launch_requests(
			id, source, external_id, sender, title, safe_preview, source_url, prompt, cwd, model, reasoning_effort, status,
			telegram_topic_id, telegram_message_id, telegram_rendered_status, thread_id, turn_id,
			auto_start, source_chat_id, source_message_id, source_thread_id,
			ack_status, ack_text, ack_message_id, ack_attempts, ack_available_at, ack_error,
			reply_status, reply_text,
			reply_message_id, reply_attempts, reply_available_at, reply_error,
			error_type, error_summary, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, external_id) DO NOTHING`,
			request.ID, request.Source, request.ExternalID, request.Sender, request.Title, nullable(request.SafePreview), nullable(request.SourceURL),
			request.Prompt, nullable(request.CWD), nullable(request.Model), nullable(request.ReasoningEffort), request.Status, request.TelegramTopicID, request.TelegramMessageID,
			nullable(telegramRenderedStatus), nullable(request.ThreadID), nullable(request.TurnID), boolToInt(request.AutoStart),
			nullable(request.SourceChatID), request.SourceMessageID, request.SourceThreadID,
			nullable(request.AckStatus), nullable(request.AckText), request.AckMessageID, request.AckAttempts, nullable(string(request.AckAvailableAt)), nullable(request.AckError),
			nullable(request.ReplyStatus), nullable(request.ReplyText),
			request.ReplyMessageID, request.ReplyAttempts, nullable(string(request.ReplyAvailableAt)), nullable(request.ReplyError), nullable(request.ErrorType),
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
	if cursor != nil {
		now := model.NowString()
		if _, err := tx.ExecContext(ctx, `
	INSERT INTO daemon_state(key, value, updated_at) VALUES (?, ?, ?)
	ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at`,
			externalSourceCursorKey(source), strconv.FormatInt(*cursor, 10), now); err != nil {
			return 0, err
		}
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
	row := s.db.QueryRowContext(ctx, `SELECT `+externalLaunchRequestColumns+` FROM external_launch_requests WHERE id=?`, id)
	var request model.ExternalLaunchRequest
	if err := scanExternalLaunchRequest(row, &request); err != nil {
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
	rows, err := s.db.QueryContext(ctx, `SELECT `+externalLaunchRequestColumns+`
	FROM external_launch_requests
	WHERE telegram_topic_id != 0 AND (
		(auto_start=0 AND (telegram_message_id=0 OR coalesce(telegram_rendered_status,'') != status))
		OR
		(auto_start=1 AND (
			telegram_rendered_status=?
			OR (telegram_message_id != 0 AND coalesce(telegram_rendered_status,'') != status)
		))
	)
	ORDER BY created_at, id
	LIMIT ?`, externalTelegramVisibilityPending, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]model.ExternalLaunchRequest, 0)
	for rows.Next() {
		var request model.ExternalLaunchRequest
		if err := scanExternalLaunchRequest(rows, &request); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

func (s *Store) ListExternalLaunchRequestsForAutoStart(ctx context.Context, limit int) ([]model.ExternalLaunchRequest, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+externalLaunchRequestColumns+`
	FROM external_launch_requests
	WHERE auto_start=1 AND status=?
	ORDER BY created_at, id LIMIT ?`, model.ExternalLaunchPendingApproval, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	requests := make([]model.ExternalLaunchRequest, 0)
	for rows.Next() {
		var request model.ExternalLaunchRequest
		if err := scanExternalLaunchRequest(rows, &request); err != nil {
			return nil, err
		}
		requests = append(requests, request)
	}
	return requests, rows.Err()
}

const externalLaunchRequestColumns = `
	id, source, external_id, sender, title, coalesce(safe_preview,''), coalesce(source_url,''), prompt, coalesce(cwd,''),
	coalesce(model,''), coalesce(reasoning_effort,''), status,
	telegram_topic_id, telegram_message_id, coalesce(telegram_rendered_status,''), coalesce(thread_id,''), coalesce(turn_id,''),
	auto_start, coalesce(source_chat_id,''), source_message_id, source_thread_id,
	coalesce(ack_status,''), coalesce(ack_text,''), ack_message_id, ack_attempts, coalesce(ack_available_at,''), coalesce(ack_error,''),
	coalesce(reply_status,''), coalesce(reply_text,''), reply_message_id, reply_attempts, coalesce(reply_available_at,''), coalesce(reply_error,''),
	coalesce(error_type,''), coalesce(error_summary,''), created_at, updated_at`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanExternalLaunchRequest(scanner rowScanner, request *model.ExternalLaunchRequest) error {
	var autoStart int
	err := scanner.Scan(&request.ID, &request.Source, &request.ExternalID, &request.Sender, &request.Title, &request.SafePreview,
		&request.SourceURL, &request.Prompt, &request.CWD, &request.Model, &request.ReasoningEffort, &request.Status, &request.TelegramTopicID, &request.TelegramMessageID,
		&request.TelegramRenderedStatus, &request.ThreadID, &request.TurnID, &autoStart, &request.SourceChatID,
		&request.SourceMessageID, &request.SourceThreadID,
		&request.AckStatus, &request.AckText, &request.AckMessageID, &request.AckAttempts, &request.AckAvailableAt, &request.AckError,
		&request.ReplyStatus, &request.ReplyText, &request.ReplyMessageID,
		&request.ReplyAttempts, &request.ReplyAvailableAt, &request.ReplyError, &request.ErrorType, &request.ErrorSummary,
		&request.CreatedAt, &request.UpdatedAt)
	request.AutoStart = autoStart != 0
	return err
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
	now := model.NowString()
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET status=?,
		reply_status=CASE WHEN coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_status END,
		reply_text=CASE WHEN coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_text END,
		reply_available_at=CASE WHEN coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_available_at END,
		updated_at=?
	WHERE id=? AND status=?`, model.ExternalLaunchDismissed, model.ExternalReplyPending,
		"The owner dismissed this Codex request. No Codex session was started.", now, now, id, model.ExternalLaunchPendingApproval)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
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
	replyText := ""
	if status == model.ExternalLaunchFailed {
		replyText = "Codex could not start this request."
		if strings.TrimSpace(errorSummary) != "" {
			replyText += " " + strings.TrimSpace(errorSummary)
		}
	} else if status == model.ExternalLaunchOutcomeUnknown {
		replyText = "The Codex dispatch outcome is unknown. The request was not replayed automatically."
	}
	now := model.NowString()
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET status=?, thread_id=?, turn_id=?, error_type=?, error_summary=?,
		reply_status=CASE WHEN ?<>'' AND coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_status END,
		reply_text=CASE WHEN ?<>'' AND coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_text END,
		reply_available_at=CASE WHEN ?<>'' AND coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_available_at END,
		updated_at=?
	WHERE id=? AND status=?`, status, nullable(threadID), nullable(turnID), nullable(errorType), nullable(errorSummary),
		replyText, model.ExternalReplyPending, replyText, nullable(replyText), replyText, now, now, id, model.ExternalLaunchStarting)
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
	now := model.NowString()
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET status=?, error_type='daemon_restart', error_summary='Daemon restarted after dispatch claim; outcome is unknown and was not replayed.',
		reply_status=CASE WHEN coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_status END,
		reply_text=CASE WHEN coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN 'The Codex dispatch outcome is unknown after a daemon restart. The request was not replayed automatically.' ELSE reply_text END,
		reply_available_at=CASE WHEN coalesce(source_chat_id,'')<>'' AND source_message_id<>0 AND coalesce(reply_status,'')='' THEN ? ELSE reply_available_at END,
		updated_at=?
	WHERE status=?`, model.ExternalLaunchOutcomeUnknown, model.ExternalReplyPending, now, now, model.ExternalLaunchStarting)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) QueueExternalReply(ctx context.Context, threadID, turnID, text string) (bool, error) {
	threadID = strings.TrimSpace(threadID)
	turnID = strings.TrimSpace(turnID)
	text = strings.TrimSpace(text)
	if threadID == "" || turnID == "" || text == "" {
		return false, errors.New("external reply requires thread, turn, and text")
	}
	now := model.NowString()
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET reply_status=?, reply_text=?, reply_available_at=?, reply_error='', updated_at=?
	WHERE thread_id=? AND turn_id=? AND status=? AND coalesce(source_chat_id,'')<>'' AND source_message_id<>0
	AND coalesce(reply_status,'')=''`, model.ExternalReplyPending, text, now, now, threadID, turnID, model.ExternalLaunchSessionStarted)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (s *Store) ClaimExternalAckBatch(ctx context.Context, limit int) ([]model.ExternalLaunchRequest, error) {
	if limit <= 0 {
		limit = 10
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := model.NowString()
	rows, err := tx.QueryContext(ctx, `SELECT `+externalLaunchRequestColumns+`
	FROM external_launch_requests
	WHERE ack_status=? AND (coalesce(ack_available_at,'')='' OR ack_available_at<=?)
	ORDER BY ack_available_at, updated_at, id LIMIT ?`, model.ExternalReplyPending, now, limit)
	if err != nil {
		return nil, err
	}
	requests := make([]model.ExternalLaunchRequest, 0)
	for rows.Next() {
		var request model.ExternalLaunchRequest
		if err := scanExternalLaunchRequest(rows, &request); err != nil {
			rows.Close()
			return nil, err
		}
		requests = append(requests, request)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range requests {
		result, err := tx.ExecContext(ctx, `UPDATE external_launch_requests SET ack_status=?, updated_at=? WHERE id=? AND ack_status=?`,
			model.ExternalReplySending, now, requests[i].ID, model.ExternalReplyPending)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return nil, errors.New("external acknowledgement changed while claiming")
		}
		requests[i].AckStatus = model.ExternalReplySending
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return requests, nil
}

func (s *Store) CompleteExternalAck(ctx context.Context, requestID string, messageID int64) error {
	if strings.TrimSpace(requestID) == "" || messageID == 0 {
		return errors.New("external acknowledgement completion requires request and message ids")
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests SET ack_status=?, ack_message_id=?, ack_error='', updated_at=?
	WHERE id=? AND ack_status=?`, model.ExternalReplySent, messageID, model.NowString(), requestID, model.ExternalReplySending)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("external acknowledgement changed before completion")
	}
	return nil
}

func (s *Store) FailExternalAck(ctx context.Context, requestID string, attempts int, availableAt time.Time, errorText string, dead bool) error {
	status := model.ExternalReplyPending
	if dead {
		status = model.ExternalReplyDead
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET ack_status=?, ack_attempts=?, ack_available_at=?, ack_error=?, updated_at=?
	WHERE id=? AND ack_status=?`, status, attempts, model.TimeString(availableAt.UTC().Format(time.RFC3339Nano)), nullable(errorText), model.NowString(), requestID, model.ExternalReplySending)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("external acknowledgement changed before failure was recorded")
	}
	return nil
}

func (s *Store) RecoverSendingExternalAcks(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET ack_status=?, ack_available_at=?, ack_error='Daemon restarted during delivery; retrying.', updated_at=?
	WHERE ack_status=?`, model.ExternalReplyPending, model.NowString(), model.NowString(), model.ExternalReplySending)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ClaimExternalReplyBatch(ctx context.Context, limit int) ([]model.ExternalLaunchRequest, error) {
	if limit <= 0 {
		limit = 10
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	now := model.NowString()
	rows, err := tx.QueryContext(ctx, `SELECT `+externalLaunchRequestColumns+`
	FROM external_launch_requests
	WHERE reply_status=? AND (coalesce(reply_available_at,'')='' OR reply_available_at<=?)
	ORDER BY reply_available_at, updated_at, id LIMIT ?`, model.ExternalReplyPending, now, limit)
	if err != nil {
		return nil, err
	}
	requests := make([]model.ExternalLaunchRequest, 0)
	for rows.Next() {
		var request model.ExternalLaunchRequest
		if err := scanExternalLaunchRequest(rows, &request); err != nil {
			rows.Close()
			return nil, err
		}
		requests = append(requests, request)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range requests {
		result, err := tx.ExecContext(ctx, `UPDATE external_launch_requests SET reply_status=?, updated_at=? WHERE id=? AND reply_status=?`,
			model.ExternalReplySending, now, requests[i].ID, model.ExternalReplyPending)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return nil, errors.New("external reply changed while claiming")
		}
		requests[i].ReplyStatus = model.ExternalReplySending
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return requests, nil
}

func (s *Store) CompleteExternalReply(ctx context.Context, requestID string, messageID int64) error {
	if strings.TrimSpace(requestID) == "" || messageID == 0 {
		return errors.New("external reply completion requires request and message ids")
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests SET reply_status=?, reply_message_id=?, reply_error='', updated_at=?
	WHERE id=? AND reply_status=?`, model.ExternalReplySent, messageID, model.NowString(), requestID, model.ExternalReplySending)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("external reply changed before completion")
	}
	return nil
}

func (s *Store) FailExternalReply(ctx context.Context, requestID string, attempts int, availableAt time.Time, errorText string, dead bool) error {
	status := model.ExternalReplyPending
	if dead {
		status = model.ExternalReplyDead
	}
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET reply_status=?, reply_attempts=?, reply_available_at=?, reply_error=?, updated_at=?
	WHERE id=? AND reply_status=?`, status, attempts, model.TimeString(availableAt.UTC().Format(time.RFC3339Nano)), nullable(errorText), model.NowString(), requestID, model.ExternalReplySending)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("external reply changed before failure was recorded")
	}
	return nil
}

func (s *Store) RecoverSendingExternalReplies(ctx context.Context) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
	UPDATE external_launch_requests
	SET reply_status=?, reply_available_at=?, reply_error='Daemon restarted during delivery; retrying.', updated_at=?
	WHERE reply_status=?`, model.ExternalReplyPending, model.NowString(), model.NowString(), model.ExternalReplySending)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) ExternalReplyBacklog(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT coalesce(reply_status,''), count(*) FROM external_launch_requests WHERE coalesce(reply_status,'')<>'' GROUP BY reply_status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result[status] = count
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	ackRows, err := s.db.QueryContext(ctx, `SELECT coalesce(ack_status,''), count(*) FROM external_launch_requests WHERE coalesce(ack_status,'')<>'' GROUP BY ack_status`)
	if err != nil {
		return nil, err
	}
	defer ackRows.Close()
	for ackRows.Next() {
		var status string
		var count int
		if err := ackRows.Scan(&status, &count); err != nil {
			return nil, err
		}
		result["ack_"+status] = count
	}
	return result, ackRows.Err()
}
