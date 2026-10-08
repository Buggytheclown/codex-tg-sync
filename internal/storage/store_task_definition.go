package storage

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func validateChildDefinition(d model.TaskDefinition) error {
	if strings.TrimSpace(d.Title) == "" || strings.TrimSpace(d.Config.InitialPrompt) == "" {
		return fmt.Errorf("%w: child title and initial prompt required", ErrTaskInvalid)
	}
	if d.Polling != nil {
		if d.Polling.Action.Type == model.PollingCreateTask {
			return fmt.Errorf("%w: child waiting cannot create another source", ErrTaskInvalid)
		}
		if err := validatePolling(*d.Polling); err != nil {
			return err
		}
		if d.Polling.Action.Type == model.PollingResumeTask && strings.TrimSpace(d.Config.ContinuationPrompt) == "" {
			return fmt.Errorf("%w: child resume waiting requires continuation prompt", ErrTaskInvalid)
		}
	}
	return nil
}
func cleanChildPolling(p *model.TaskPolling) *model.TaskPolling {
	if p == nil {
		return nil
	}
	return &model.TaskPolling{Type: p.Type, Mode: p.Mode, Config: p.Config, Action: p.Action}
}
func (s *Store) normalizeSourcePolling(p model.TaskPolling) model.TaskPolling {
	if p.Action.ChildDefinition != nil {
		d := *p.Action.ChildDefinition
		d.Config = s.normalizeTaskConfig(d.Config)
		d.Polling = cleanChildPolling(d.Polling)
		p.Action.ChildDefinition = &d
	}
	return p
}

// Source placeholders are data. In JSON, substitution only affects string values.
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
func taskFromDefinition(def model.TaskDefinition, sourceID string, event model.PollingEvent) (model.Task, error) {
	now := model.NowString()
	vars := make(map[string]string, len(event.Variables)+1)
	for k, v := range event.Variables {
		vars[k] = v
	}
	if _, ok := vars["title"]; !ok && event.Title != "" {
		vars["title"] = event.Title
	}
	event.Variables = vars
	config := def.Config
	config.InitialPrompt = renderTaskText(config.InitialPrompt, event.Variables)
	config.ContinuationPrompt = renderTaskText(config.ContinuationPrompt, event.Variables)
	t := model.Task{ID: uuid.NewString(), Title: renderTaskText(def.Title, event.Variables), Description: renderTaskText(def.Description, event.Variables), Column: model.TaskInbox, SourceURL: event.SourceURL, SourceKey: event.SourceKey, SourceTaskID: sourceID, SourceAuthor: event.Variables["author"], Config: config, Version: 1, CreatedAt: now, UpdatedAt: now}
	if t.Title == "" {
		t.Title = event.Title
	}
	if t.Description == "" && event.Description != "" {
		t.Description = event.Description
	}
	if def.Polling != nil {
		p := *def.Polling
		p.TaskID = t.ID
		p.Generation = 1
		p.Cursor = json.RawMessage(`{}`)
		if len(event.Cursor) > 0 {
			p.Cursor = event.Cursor
		}
		var err error
		p.Config, err = renderTaskJSON(p.Config, event.Variables)
		if err != nil {
			return model.Task{}, err
		}
		t.Polling = &p
	}
	return t, nil
}
