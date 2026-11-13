package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func (s *Store) TaskLinks(ctx context.Context, id, kind string) ([]model.TaskReference, error) {
	query := `SELECT r.id,r.title,r.card_type,r.arcanum_pr_id,coalesce(r.parent_task_id,''),r.column,r.version,coalesce(r.thread_id,''),r.ticket_url FROM tasks r`
	args := []any{}
	if id != "" {
		query += ` JOIN tasks owner ON owner.id=? WHERE r.parent_task_id=owner.id OR r.id=owner.parent_task_id`
		args = append(args, id)
	} else if kind == model.CardPR || kind == model.CardTask {
		query += ` WHERE r.card_type=? AND NOT EXISTS(SELECT 1 FROM task_polling p WHERE p.task_id=r.id AND json_extract(p.action_json,'$.type')='create_task')`
		args = append(args, kind)
	} else {
		return nil, ErrTaskInvalid
	}
	query += ` ORDER BY r.title,r.id`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.TaskReference{}
	for rows.Next() {
		var r model.TaskReference
		if err := rows.Scan(&r.ID, &r.Title, &r.CardType, &r.ArcanumPRID, &r.ParentTaskID, &r.Column, &r.Version, &r.ThreadID, &r.TicketURL); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
