package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const templateColumns = `id,name,version,definition_json,created_at,updated_at`

func scanTaskTemplate(row rowScanner) (*model.TaskTemplate, error) {
	var t model.TaskTemplate
	var raw string
	err := row.Scan(&t.ID, &t.Name, &t.Version, &raw, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(raw), &t.Definition)
	return &t, err
}
func validateTaskTemplate(in model.SaveTaskTemplateInput) error {
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Definition.Title) == "" || strings.TrimSpace(in.Definition.Config.InitialPrompt) == "" {
		return fmt.Errorf("%w: template name, title and initial prompt required", ErrTaskInvalid)
	}
	if in.Definition.Polling != nil {
		if err := validatePolling(*in.Definition.Polling); err != nil {
			return err
		}
		if in.Definition.Polling.Action.Type == model.PollingResumeTask && strings.TrimSpace(in.Definition.Config.ContinuationPrompt) == "" {
			return fmt.Errorf("%w: resume template requires continuation prompt", ErrTaskInvalid)
		}
		if in.Definition.Polling.Action.Type == model.PollingCreateTask {
			return fmt.Errorf("%w: template cannot create another source", ErrTaskInvalid)
		}
	}
	return nil
}
func (s *Store) ListTaskTemplates(ctx context.Context) ([]model.TaskTemplate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+templateColumns+` FROM task_templates ORDER BY name,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TaskTemplate{}
	for rows.Next() {
		t, err := scanTaskTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}
func (s *Store) GetTaskTemplate(ctx context.Context, id string) (*model.TaskTemplate, error) {
	t, err := scanTaskTemplate(s.db.QueryRowContext(ctx, `SELECT `+templateColumns+` FROM task_templates WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return t, err
}
func (s *Store) CreateTaskTemplate(ctx context.Context, in model.SaveTaskTemplateInput) (*model.TaskTemplate, error) {
	if err := validateTaskTemplate(in); err != nil {
		return nil, err
	}
	in.Definition.Config = s.normalizeTaskConfig(in.Definition.Config)
	in.Definition.Polling = cleanTemplatePolling(in.Definition.Polling)
	raw, err := json.Marshal(in.Definition)
	if err != nil {
		return nil, err
	}
	now := model.NowString()
	id := uuid.NewString()
	_, err = s.db.ExecContext(ctx, `INSERT INTO task_templates(id,name,version,definition_json,created_at,updated_at) VALUES(?,?,1,?,?,?)`, id, in.Name, string(raw), now, now)
	if err != nil {
		return nil, err
	}
	return s.GetTaskTemplate(ctx, id)
}
func cleanTemplatePolling(p *model.TaskPolling) *model.TaskPolling {
	if p == nil {
		return nil
	}
	return &model.TaskPolling{Type: p.Type, Mode: p.Mode, Config: p.Config, Action: p.Action}
}
func (s *Store) UpdateTaskTemplate(ctx context.Context, id string, in model.SaveTaskTemplateInput) (*model.TaskTemplate, error) {
	if err := validateTaskTemplate(in); err != nil {
		return nil, err
	}
	in.Definition.Config = s.normalizeTaskConfig(in.Definition.Config)
	in.Definition.Polling = cleanTemplatePolling(in.Definition.Polling)
	raw, err := json.Marshal(in.Definition)
	if err != nil {
		return nil, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE task_templates SET name=?,version=version+1,definition_json=?,updated_at=? WHERE id=? AND version=?`, in.Name, string(raw), model.NowString(), id, in.ExpectedVersion)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		t, err := s.GetTaskTemplate(ctx, id)
		if err != nil {
			return nil, err
		}
		if t == nil {
			return nil, ErrTaskNotFound
		}
		return nil, ErrTaskConflict
	}
	return s.GetTaskTemplate(ctx, id)
}
func (s *Store) DeleteTaskTemplate(ctx context.Context, id string, version int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	t, err := scanTaskTemplate(tx.QueryRowContext(ctx, `SELECT `+templateColumns+` FROM task_templates WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrTaskNotFound
	}
	if err != nil {
		return err
	}
	if t.Version != version {
		return ErrTaskConflict
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM task_polling WHERE json_extract(action_json,'$.type')='create_task' AND json_extract(action_json,'$.template_id')=?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrTaskTemplateInUse
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM task_templates WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// Template variables are data. In JSON, substitution only affects string values.
var taskVariablePattern = regexp.MustCompile(`\{\{([A-Za-z0-9_]+)\}\}`)

func renderTaskText(value string, vars map[string]string) string {
	return taskVariablePattern.ReplaceAllStringFunc(value, func(token string) string {
		key := token[2 : len(token)-2]
		if v, ok := vars[key]; ok {
			return v
		}
		return token
	})
}
func renderTaskJSON(raw json.RawMessage, vars map[string]string) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var render func(any) any
	render = func(v any) any {
		switch x := v.(type) {
		case string:
			return renderTaskText(x, vars)
		case []any:
			for i := range x {
				x[i] = render(x[i])
			}
			return x
		case map[string]any:
			for k, v := range x {
				x[k] = render(v)
			}
			return x
		default:
			return x
		}
	}
	return json.Marshal(render(value))
}
func taskFromTemplateTx(ctx context.Context, tx *sql.Tx, templateID string, event model.PollingEvent) (model.Task, error) {
	tmpl, err := scanTaskTemplate(tx.QueryRowContext(ctx, `SELECT `+templateColumns+` FROM task_templates WHERE id=?`, templateID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, fmt.Errorf("%w: missing template", ErrTaskInvalid)
	}
	if err != nil {
		return model.Task{}, err
	}
	now := model.NowString()
	vars := make(map[string]string, len(event.Variables)+1)
	for k, v := range event.Variables {
		vars[k] = v
	}
	if _, ok := vars["title"]; !ok && event.Title != "" {
		vars["title"] = event.Title
	}
	event.Variables = vars
	config := tmpl.Definition.Config
	config.InitialPrompt = renderTaskText(config.InitialPrompt, event.Variables)
	config.ContinuationPrompt = renderTaskText(config.ContinuationPrompt, event.Variables)
	t := model.Task{ID: uuid.NewString(), Title: renderTaskText(tmpl.Definition.Title, event.Variables), Description: renderTaskText(tmpl.Definition.Description, event.Variables), Column: model.TaskInbox, SourceURL: event.SourceURL, SourceKey: event.SourceKey, TemplateID: tmpl.ID, TemplateVersion: tmpl.Version, Config: config, Version: 1, CreatedAt: now, UpdatedAt: now}
	if t.Title == "" {
		t.Title = event.Title
	}
	if t.Description == "" && event.Description != "" {
		t.Description = event.Description
	}
	if tmpl.Definition.Polling != nil {
		p := *tmpl.Definition.Polling
		p.TaskID = t.ID
		p.Generation = 1
		p.Cursor = json.RawMessage(`{}`)
		if len(event.Cursor) > 0 {
			p.Cursor = event.Cursor
		}
		p.Config, err = renderTaskJSON(p.Config, event.Variables)
		if err != nil {
			return model.Task{}, err
		}
		t.Polling = &p
	}
	return t, nil
}
