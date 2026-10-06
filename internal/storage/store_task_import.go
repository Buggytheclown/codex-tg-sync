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

// BootstrapTaskSource atomically imports a retired owner's source and barriers.
// It never modifies a source/template after the durable import marker exists.
// Adapter code owns mapping legacy IDs to cycle/occurrence event keys.
func (s *Store) BootstrapTaskSource(ctx context.Context, in model.TaskSourceImport) (*model.Task, error) {
	if strings.TrimSpace(in.Key) == "" || strings.TrimSpace(in.Title) == "" {
		return nil, fmt.Errorf("%w: import key and source title required", ErrTaskInvalid)
	}
	if in.Column == "" {
		in.Column = model.TaskPollingColumn
	}
	if !validTaskColumn(in.Column) {
		return nil, ErrTaskInvalid
	}
	marker := "task_board.import." + in.Key
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sourceID string
	err = tx.QueryRowContext(ctx, `SELECT value FROM daemon_state WHERE key=?`, marker).Scan(&sourceID)
	if err == nil {
		tx.Rollback()
		return s.GetTask(ctx, sourceID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	sourceKey := "source:" + in.Key
	err = tx.QueryRowContext(ctx, `SELECT id FROM tasks WHERE source_key=?`, sourceKey).Scan(&sourceID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := model.NowString()
	if errors.Is(err, sql.ErrNoRows) {
		tmplID := uuid.NewSHA1(uuid.NameSpaceOID, []byte("task_board.template."+in.Key)).String()
		name := in.TemplateName
		if name == "" {
			name = in.Title
		}
		input := model.SaveTaskTemplateInput{Name: name, Definition: in.Definition}
		if err := validateTaskTemplate(input); err != nil {
			return nil, err
		}
		input.Definition.Config = s.normalizeTaskConfig(input.Definition.Config)
		input.Definition.Polling = cleanTemplatePolling(input.Definition.Polling)
		raw, err := json.Marshal(input.Definition)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_templates(id,name,version,definition_json,created_at,updated_at) VALUES(?,?,1,?,?,?) ON CONFLICT(id) DO NOTHING`, tmplID, name, string(raw), now, now)
		if err != nil {
			return nil, err
		}
		sourceID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("task_board.source."+in.Key)).String()
		source := model.Task{ID: sourceID, Title: in.Title, Column: in.Column, SourceKey: sourceKey, Config: s.normalizeTaskConfig(model.TaskConfig{}), Version: 1, CreatedAt: now, UpdatedAt: now}
		if err := insertTaskTx(ctx, tx, source); err != nil {
			return nil, err
		}
		p := in.Polling
		p.TaskID = sourceID
		p.Action = model.TaskPollingAction{Type: model.PollingCreateTask, TemplateID: tmplID}
		p.Generation = 1
		if len(p.Cursor) == 0 {
			p.Cursor = json.RawMessage(`{}`)
		}
		if err := validatePolling(p); err != nil {
			return nil, err
		}
		if err := s.validateResolvedTaskPolling(p); err != nil {
			return nil, err
		}
		if err := setPollingTx(ctx, tx, p); err != nil {
			return nil, err
		}
	}
	source, err := getTaskTx(ctx, tx, sourceID)
	if err != nil {
		return nil, err
	}
	if source.Polling == nil || source.Polling.Action.Type != model.PollingCreateTask {
		return nil, fmt.Errorf("%w: import source is not a create_task source", ErrTaskInvalid)
	}
	for _, legacy := range in.Legacy {
		if legacy.SourceKey == "" || legacy.EventKey == "" || len(legacy.RequestIDs) == 0 {
			return nil, fmt.Errorf("%w: legacy import needs cycle/event keys and request IDs", ErrTaskInvalid)
		}
		requests := make([]model.ExternalLaunchRequest, 0, len(legacy.RequestIDs))
		threadID := ""
		for _, id := range legacy.RequestIDs {
			var request model.ExternalLaunchRequest
			if err := scanExternalLaunchRequest(tx.QueryRowContext(ctx, `SELECT `+externalLaunchRequestColumns+` FROM external_launch_requests WHERE id=?`, id), &request); err != nil {
				return nil, err
			}
			requests = append(requests, request)
			if request.ThreadID != "" {
				if threadID != "" && threadID != request.ThreadID {
					return nil, errors.New("legacy cycle spans multiple threads")
				}
				threadID = request.ThreadID
			}
		}
		var taskID string
		err = findTaskIdentityTx(ctx, tx, legacy.SourceKey, threadID, &taskID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if errors.Is(err, sql.ErrNoRows) {
			child, err := taskFromTemplateTx(ctx, tx, source.Polling.Action.TemplateID, model.PollingEvent{SourceKey: legacy.SourceKey, SourceURL: legacy.SourceURL, Title: legacy.Title, Cursor: legacy.Cursor, Variables: legacy.Variables})
			if err != nil {
				return nil, err
			}
			first := requests[0]
			if legacy.Title != "" {
				child.Title = legacy.Title
			}
			if child.Title == "" || strings.Contains(child.Title, "{{") {
				child.Title = first.Title
			}
			child.ThreadID = threadID
			if child.SourceURL == "" {
				child.SourceURL = first.SourceURL
			}
			child.Config.InitialPrompt = first.Prompt
			child.Config.Execution.CWD = first.CWD
			child.Config.Execution.Model = first.Model
			child.Config.Execution.ReasoningEffort = first.ReasoningEffort
			if first.ApprovalPolicy != nil {
				child.Config.Execution.ApprovalPolicy = *first.ApprovalPolicy
			}
			if first.ApprovalsReviewer != nil {
				child.Config.Execution.ApprovalsReviewer = *first.ApprovalsReviewer
			}
			if first.SandboxMode != nil {
				child.Config.Execution.SandboxMode = *first.SandboxMode
			}
			if err := insertTaskTx(ctx, tx, child); err != nil {
				return nil, err
			}
			taskID = child.ID
			p := child.Polling
			if legacy.Polling != nil {
				copy := *legacy.Polling
				p = &copy
			}
			if p != nil {
				p.TaskID = taskID
				p.Generation = 1
				p.Cursor = json.RawMessage(`{}`)
				if len(legacy.Cursor) > 0 {
					p.Cursor = legacy.Cursor
				}
				if err := validatePolling(*p); err != nil {
					return nil, err
				}
				if err := setPollingTx(ctx, tx, *p); err != nil {
					return nil, err
				}
			}
		}
		linked, err := getTaskTx(ctx, tx, taskID)
		if err != nil {
			return nil, err
		}
		if linked.SourceKey != "" && linked.SourceKey != legacy.SourceKey {
			return nil, fmt.Errorf("%w: legacy chat belongs to another source cycle", ErrTaskConflict)
		}
		if linked.ThreadID != "" && threadID != "" && linked.ThreadID != threadID {
			return nil, fmt.Errorf("%w: legacy cycle belongs to another chat", ErrTaskConflict)
		}
		if linked.SourceKey == "" {
			if _, err := tx.ExecContext(ctx, `UPDATE tasks SET source_key=?,version=version+1,updated_at=? WHERE id=?`, legacy.SourceKey, now, taskID); err != nil {
				return nil, err
			}
		}
		eventID := uuid.NewString()
		payload, _ := json.Marshal(map[string]any{"legacy_request_ids": legacy.RequestIDs})
		_, err = tx.ExecContext(ctx, `INSERT INTO task_events(id,task_id,kind,event_key,result_task_id,payload_json,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(task_id,event_key) WHERE event_key IS NOT NULL AND event_key<>'' DO NOTHING`, eventID, sourceID, "legacy_import", legacy.EventKey, taskID, string(payload), now)
		if err != nil {
			return nil, err
		}
		for _, request := range requests {
			if request.TaskID != "" && request.TaskID != taskID {
				return nil, errors.New("legacy request already linked to another task")
			}
			_, err = tx.ExecContext(ctx, `UPDATE external_launch_requests SET task_id=? WHERE id=?`, taskID, request.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	if err := addTaskEventTx(ctx, tx, sourceID, "source_imported", map[string]string{"key": in.Key}); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO daemon_state(key,value,updated_at) VALUES(?,?,?)`, marker, sourceID, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetTask(ctx, sourceID)
}
