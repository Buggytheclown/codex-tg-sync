package storage

import (
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
	if out.Waiting && out.DiagnosticNew {
		_, err = tx.ExecContext(ctx, `UPDATE task_polling SET next_check_at=?,last_attempt_at=?,last_error=?,cursor_json=? WHERE task_id=?`, out.NextCheckAt, out.ObservationAt, out.DiagnosticError, string(out.Cursor), t.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if out.Waiting && len(out.Events) == 0 && out.PRStatus == "" {
		// Waiting is a scheduling update, not a fabricated attempt or successful IO.
		_, err = tx.ExecContext(ctx, `UPDATE task_polling SET next_check_at=? WHERE task_id=?`, out.NextCheckAt, t.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if out.Error != "" {
		_, err = tx.ExecContext(ctx, `UPDATE task_polling SET last_attempt_at=?,last_error=?,next_check_at=? WHERE task_id=?`, now, out.Error, out.NextCheckAt, t.ID)
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}

	if p.Type == model.PollingRules && len(out.Events) > 0 {
		var cfg model.PollingRulesConfig
		if err := json.Unmarshal(p.Config, &cfg); err != nil {
			return false, err
		}
		found := false
		for _, rule := range cfg.Rules {
			if rule.ID == out.RuleID {
				leaf := rule.Polling(*p)
				p = &leaf
				found = true
				break
			}
		}
		if !found {
			return false, fmt.Errorf("%w: outcome rule no longer exists", ErrTaskInvalid)
		}
	}
	if out.PRStatus == "merged" && p.Type == "pr_wait" && p.Action.Type != model.PollingCreateTask {
		complete, err := taskCompleteOnMerge(ctx, tx, t)
		if err != nil {
			return false, err
		}
		if complete {
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET column='done',version=version+1,updated_at=?,done_at=? WHERE id=?`, now, model.NowString(), t.ID); err != nil {
				return false, err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE task_polling SET generation=generation+1,last_attempt_at=?,last_success_at=?,last_error='',next_check_at='' WHERE task_id=?`, now, now, t.ID); err != nil {
				return false, err
			}
			if err := cancelUnstartedTaskRequestsTx(ctx, tx, t.ID, now); err != nil {
				return false, err
			}
			if err := addTaskEventTx(ctx, tx, t.ID, "pr_merged", map[string]string{"status": "merged"}); err != nil {
				return false, err
			}
			return true, tx.Commit()
		}
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
		if p.Type == "cron" {
			accepted, err := legacyCronEventAcceptedTx(ctx, tx, t.ID, event)
			if err != nil {
				return false, err
			}
			if accepted {
				continue
			}
		}

		if !acceptedEvent && p.Mode == model.PollingOnce && (p.Action.Column == model.TaskTODO || p.Action.Column == model.TaskDone) {
			if err := cancelUnstartedTaskRequestsTx(ctx, tx, t.ID, now); err != nil {
				return false, err
			}
		}
		eventID := uuid.NewString()
		resultID := ""
		var target *model.Task
		switch p.Action.Type {
		case model.PollingCreateTask:
			if strings.TrimSpace(event.SourceKey) == "" {
				return false, fmt.Errorf("%w: create event source_key required", ErrTaskInvalid)
			}
			def := *p.Action.ChildDefinition
			if def.CardType == "" {
				def.CardType = model.SourceChildCardType(p.Type)
			}
			child, err := taskFromDefinition(def, t.ID, event)
			if err != nil {
				return false, err
			}
			existingPR, err := findPRCardTx(ctx, tx, child.ArcanumPRID)
			if err != nil {
				return false, err
			}
			var id string
			err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE source_key=?`, event.SourceKey).Scan(&id)
			if err == nil && child.CardType == model.CardPR {
				old, readErr := getTaskTx(ctx, tx, id)
				if readErr != nil {
					return false, readErr
				}
				if old.CardType != model.CardPR || old.ArcanumPRID != child.ArcanumPRID {
					return false, ErrTaskConflict
				}
			}
			if err == nil && existingPR != nil && id != existingPR.ID {
				return false, ErrTaskConflict
			}
			if errors.Is(err, sql.ErrNoRows) && existingPR != nil {
				id = existingPR.ID
				err = nil
			}
			if err == nil {
				resultID = id
			} else if errors.Is(err, sql.ErrNoRows) {
				child.Title, err = sourceChildTitle(*t, child.Title, event)
				if err != nil {
					return false, err
				}
				if p.Action.Column != "" {
					child.Column = p.Action.Column
				}
				if err := insertTaskTx(ctx, tx, child); err != nil {
					return false, err
				}
				if child.Polling != nil {
					if err := freezeMergeChoice(child.Polling, child.SourceKey, p.Type); err != nil {
						return false, err
					}
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
				if p.Action.InitialLaunch == nil || p.Action.InitialLaunch.Mode != "none" {
					target = &child
				}
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
				prompt = target.Config.ContinuationPrompt
				if p.Action.Prompt != "" {
					prompt = p.Action.Prompt
				}
				prompt = renderTaskText(prompt, event.Variables)
				threadID = target.ThreadID
			}
			if strings.TrimSpace(prompt) == "" || approvalTopicID == 0 {
				return false, fmt.Errorf("%w: launch needs prompt and Telegram approval topic", ErrTaskInvalid)
			}
			sourceLabel := strings.TrimSpace(t.Title)
			if p.Action.Type == model.PollingResumeTask && target.SourceTaskID != "" {
				source, err := getTaskTx(ctx, tx, target.SourceTaskID)
				if err != nil {
					return false, err
				}
				sourceLabel = strings.TrimSpace(source.Title)
			}
			author := strings.TrimSpace(event.Variables["author"])
			if author == "" {
				author = target.SourceAuthor
			}
			sender := author
			if sender == "" {
				sender = sourceLabel
			}
			autoStart := (p.Action.Type == model.PollingCreateTask && initialLaunchAllowed(p.Action.InitialLaunch, author)) || (p.Action.Type == model.PollingResumeTask && p.Action.AutoStart)
			cfg := normalizeConfig(target.Config).Execution
			if reserved, err := boardChatReservedTx(ctx, tx, target.ID); err != nil {
				return false, err
			} else if reserved {
				return false, ErrTaskConflict
			}
			request := model.ExternalLaunchRequest{ID: uuid.NewString(), Source: sourceLabel, ExternalID: eventID, Sender: sender, AutoStart: autoStart, Title: target.Title, SafePreview: prompt, SourceURL: target.SourceURL, Prompt: prompt, CWD: cfg.CWD, Model: cfg.Model, ReasoningEffort: cfg.ReasoningEffort, ApprovalPolicy: &cfg.ApprovalPolicy, ApprovalsReviewer: &cfg.ApprovalsReviewer, SandboxMode: &cfg.SandboxMode, Status: model.ExternalLaunchPendingApproval, TelegramTopicID: approvalTopicID, TaskID: target.ID, TriggerEventID: eventID, TargetThreadID: threadID, CreatedAt: now, UpdatedAt: now}
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
	if out.ObservationAt != "" {
		now = out.ObservationAt
	}
	cursor := p.Cursor
	if len(out.Cursor) > 0 {
		cursor = out.Cursor
	}
	_, err = tx.ExecContext(ctx, `UPDATE task_polling SET cursor_json=?,next_check_at=?,last_attempt_at=?,last_success_at=?,last_error=? WHERE task_id=?`, string(cursor), out.NextCheckAt, now, now, out.DiagnosticError, t.ID)
	if err != nil {
		return false, err
	}
	if acceptedEvent && p.Mode == model.PollingOnce {
		doneAt := model.TimeString("")
		if pollingDestination(p.Action) == model.TaskDone {
			doneAt = model.NowString()
		}
		_, err = tx.ExecContext(ctx, `UPDATE tasks SET column=?,version=version+1,updated_at=?,done_at=? WHERE id=?`, pollingDestination(p.Action), now, doneAt, t.ID)
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

func pollingDestination(a model.TaskPollingAction) string {
	if a.Column != "" {
		return a.Column
	}
	return model.TaskInbox
}
