package storage

import (
	"context"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/model"
)

// ListTaskCards uses two bounded queries, independent of the number of cards.
// Snapshot JSON, task execution settings and launch payloads are never loaded.
func (s *Store) ListTaskCards(ctx context.Context) ([]model.TaskCardDTO, error) {
	return s.listTaskCards(ctx, "")
}

func (s *Store) GetTaskCard(ctx context.Context, id string) (*model.TaskCardDTO, error) {
	cards, err := s.listTaskCards(ctx, id)
	if err != nil {
		return nil, err
	}
	if len(cards) == 0 {
		return nil, nil
	}
	return &cards[0], nil
}

func (s *Store) listTaskCards(ctx context.Context, id string) ([]model.TaskCardDTO, error) {
	where := ""
	args := []any{}
	if id != "" {
		where = " WHERE t.id=?"
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT t.ticket_url,t.priority,t.id,t.title,t.description,t.column,t.position,coalesce(t.thread_id,''),coalesce(t.source_url,''),coalesce(t.source_key,''),coalesce(t.source_task_id,''),coalesce(t.source_author,''),t.version,t.created_at,t.updated_at,
 coalesce(x.thread_id,''),coalesce(x.last_seen_turn_status,''),coalesce(x.last_seen_thread_status,''),coalesce(x.last_poll_at,''),
 coalesce(p.task_id,''),coalesce(p.type,''),coalesce(p.mode,''),coalesce(p.config_json,'{}'),coalesce(p.action_json,'{}'),coalesce(p.cursor_json,'{}'),coalesce(p.next_check_at,''),coalesce(p.last_attempt_at,''),coalesce(p.last_success_at,''),coalesce(p.last_error,''),coalesce(json_extract(t.task_config_json,'$.continuation_prompt'),'')
 FROM tasks t LEFT JOIN thread_snapshots x ON x.thread_id=t.thread_id LEFT JOIN task_polling p ON p.task_id=t.id `+where+` ORDER BY t.column,t.position,t.id`, args...)
	if err != nil {
		return nil, err
	}
	out := []model.TaskCardDTO{}
	indices := map[string]int{}
	for rows.Next() {
		var c model.TaskCardDTO
		var e model.TaskCardExecution
		var p model.TaskPolling
		var snapshotID, pollingID, cfg, action, cursor, prompt string
		if err := rows.Scan(&c.TicketURL, &c.Priority, &c.ID, &c.Title, &c.Description, &c.Column, &c.Position, &c.ThreadID, &c.SourceURL, &c.SourceKey, &c.SourceTaskID, &c.SourceAuthor, &c.Version, &c.CreatedAt, &c.UpdatedAt, &snapshotID, &e.LastSeenTurnStatus, &e.LastSeenThreadStatus, &e.LastPollAt, &pollingID, &p.Type, &p.Mode, &cfg, &action, &cursor, &p.NextCheckAt, &p.LastAttemptAt, &p.LastSuccessAt, &p.LastError, &prompt); err != nil {
			rows.Close()
			return nil, err
		}
		if snapshotID != "" {
			c.Execution = &e
		}
		if pollingID != "" {
			p.TaskID = pollingID
			p.Config = json.RawMessage(cfg)
			p.Cursor = json.RawMessage(cursor)
			problem := ""
			validation := json.Unmarshal([]byte(action), &p.Action)
			if validation == nil {
				validation = validatePolling(p)
			}
			if validation == nil {
				validation = s.validateResolvedTaskPolling(p)
			}
			if validation == nil {
				validation = validateTaskPollingReadiness(&model.Task{ThreadID: c.ThreadID, Config: model.TaskConfig{ContinuationPrompt: prompt}}, p)
			}
			if validation != nil {
				problem = validation.Error()
			}
			c.Polling = &model.TaskCardPolling{Type: p.Type, Mode: p.Mode, Action: model.TaskCardPollingAction{Type: p.Action.Type}, NextCheckAt: p.NextCheckAt, LastAttemptAt: p.LastAttemptAt, LastSuccessAt: p.LastSuccessAt, LastError: p.LastError, Problem: problem}
		}
		c.Requests = []model.TaskCardRequest{}
		indices[c.ID] = len(out)
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	rows, err = s.db.QueryContext(ctx, `SELECT r.task_id,r.id,r.status FROM external_launch_requests r JOIN tasks t ON t.id=r.task_id `+where+` ORDER BY r.created_at,r.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var taskID string
		var r model.TaskCardRequest
		if err := rows.Scan(&taskID, &r.ID, &r.Status); err != nil {
			return nil, err
		}
		if i, ok := indices[taskID]; ok {
			out[i].Requests = append(out[i].Requests, r)
		}
	}
	return out, rows.Err()
}
