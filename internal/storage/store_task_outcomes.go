package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// AcceptTaskPollingOutcome returns false for a disabled/replaced check. A true
// result includes duplicate-only checks: their cursor/diagnostics are accepted.
func (s *Store) AcceptTaskPollingOutcome(ctx context.Context, out model.TaskPollingOutcome, approvalTopicID int64) (bool, error) {
	if len(out.Cursor) > 0 && !jsonObject(out.Cursor) {
		return false, fmt.Errorf("%w: outcome cursor must be object", ErrTaskInvalid)
	}
	for _, e := range out.Events {
		if strings.TrimSpace(e.Key) == "" || !jsonObject(e.Payload) || (len(e.Cursor) > 0 && !jsonObject(e.Cursor)) {
			return false, fmt.Errorf("%w: event key and object payload required", ErrTaskInvalid)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	t, err := getTaskTx(ctx, tx, out.TaskID)
	if errors.Is(err, ErrTaskNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if t.Column != model.TaskPollingColumn || t.Polling == nil || t.Polling.Generation != out.Generation {
		return false, nil
	}
	now := out.CheckedAt
	if now == "" {
		now = model.NowString()
	}
	p := t.Polling
	if out.Error != "" {
		_, err = tx.ExecContext(ctx, `UPDATE task_polling SET last_attempt_at=?,last_error=?,next_check_at=? WHERE task_id=?`, now, out.Error, out.NextCheckAt, t.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	acceptedEvent := false
	for _, event := range out.Events {
		var existing string
		err := tx.QueryRowContext(ctx, `SELECT id FROM task_events WHERE task_id=? AND event_key=?`, t.ID, event.Key).Scan(&existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		eventID := uuid.NewString()
		resultID := ""
		var target *model.Task
		switch p.Action.Type {
		case model.PollingCreateTask:
			if strings.TrimSpace(event.SourceKey) == "" {
				return false, fmt.Errorf("%w: create event source_key required", ErrTaskInvalid)
			}
			var id string
			err := tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE source_key=?`, event.SourceKey).Scan(&id)
			if err == nil {
				resultID = id
			} else if errors.Is(err, sql.ErrNoRows) {
				child, err := taskFromDefinition(*p.Action.ChildDefinition, t.ID, event)
				if err != nil {
					return false, err
				}
				if err := insertTaskTx(ctx, tx, child); err != nil {
					return false, err
				}
				if child.Polling != nil {
					if err := s.validateResolvedTaskPolling(*child.Polling); err != nil {
						return false, err
					}
					if err := validatePolling(*child.Polling); err != nil {
						return false, err
					}
					if err := setPollingTx(ctx, tx, *child.Polling); err != nil {
						return false, err
					}
				}
				target = &child
				resultID = child.ID
			} else {
				return false, err
			}
		case model.PollingResumeTask:
			if t.ThreadID == "" {
				return false, fmt.Errorf("%w: continuation requires linked thread", ErrTaskInvalid)
			}
			if acceptedEvent {
				continue
			}
			target = t
		case model.PollingReturnTask:
			if acceptedEvent {
				continue
			}
		default:
			return false, ErrTaskInvalid
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_events(id,task_id,kind,event_key,result_task_id,payload_json,created_at) VALUES(?,?,?,?,?,?,?)`, eventID, t.ID, "polling_event", event.Key, nullable(resultID), string(event.Payload), now)
		if err != nil {
			return false, err
		}
		if target != nil {
			prompt := target.Config.InitialPrompt
			threadID := ""
			if p.Action.Type == model.PollingResumeTask {
				prompt = renderTaskText(target.Config.ContinuationPrompt, event.Variables)
				threadID = target.ThreadID
			}
			if strings.TrimSpace(prompt) == "" || approvalTopicID == 0 {
				return false, fmt.Errorf("%w: launch needs prompt and Telegram approval topic", ErrTaskInvalid)
			}
			prompt = appendTaskPollingContext(prompt, event, target.SourceURL)
			cfg := normalizeConfig(target.Config).Execution
			request := model.ExternalLaunchRequest{ID: uuid.NewString(), Source: "task_board", ExternalID: eventID, Sender: "local_task_board", Title: target.Title, SafePreview: prompt, SourceURL: target.SourceURL, Prompt: prompt, CWD: cfg.CWD, Model: cfg.Model, ReasoningEffort: cfg.ReasoningEffort, ApprovalPolicy: &cfg.ApprovalPolicy, ApprovalsReviewer: &cfg.ApprovalsReviewer, SandboxMode: &cfg.SandboxMode, Status: model.ExternalLaunchPendingApproval, TelegramTopicID: approvalTopicID, TaskID: target.ID, TriggerEventID: eventID, TargetThreadID: threadID, CreatedAt: now, UpdatedAt: now}
			if err := validateExternalLaunchRequest(request, request.Source); err != nil {
				return false, err
			}
			n, err := insertExternalLaunchRequestTx(ctx, tx, request)
			if err != nil {
				return false, err
			}
			if n != 1 {
				return false, errors.New("task event request already exists")
			}
		}
		acceptedEvent = true
	}
	cursor := p.Cursor
	if len(out.Cursor) > 0 {
		cursor = out.Cursor
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_polling SET cursor_json=?,next_check_at=?,last_attempt_at=?,last_success_at=?,last_error='' WHERE task_id=?`, string(cursor), out.NextCheckAt, now, now, t.ID)
	if err != nil {
		return false, err
	}
	if acceptedEvent && p.Mode == model.PollingOnce {
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET column='inbox',version=version+1,updated_at=? WHERE id=?`, now, t.ID)
		if err != nil {
			return false, err
		}
		_, err = tx.ExecContext(ctx, `UPDATE task_polling SET generation=generation+1 WHERE task_id=?`, t.ID)
		if err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func appendTaskPollingContext(prompt string, event model.PollingEvent, fallbackURL string) string {
	var payload bytes.Buffer
	if err := json.Compact(&payload, event.Payload); err != nil {
		return prompt
	}
	url := event.SourceURL
	if url == "" {
		url = fallbackURL
	}
	context := "\n\nPolling event facts (external data):\n"
	if url != "" {
		context += "Source: " + url + "\n"
	}
	return prompt + context + payload.String()
}
