package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"strconv"
	"strings"
)

// ListPollingTasks reads configuration and lightweight identity only. It never
// loads archived tasks, snapshots, or launch request histories for health.
func (s *Store) ListPollingTasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+strings.ReplaceAll(pollingColumns, "task_id,", "p.task_id,")+`,t.title FROM task_polling p JOIN tasks t ON t.id=p.task_id WHERE t.column='polling' ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Task{}
	for rows.Next() {
		var title string
		p, err := scanPolling(pollingTitleScanner{row: rows, title: &title})
		if err != nil {
			return nil, err
		}
		out = append(out, model.Task{ID: p.TaskID, Title: title, Column: model.TaskPollingColumn, Polling: p})
	}
	return out, rows.Err()
}

type pollingTitleScanner struct {
	row   rowScanner
	title *string
}

func (s pollingTitleScanner) Scan(dest ...any) error { return s.row.Scan(append(dest, s.title)...) }

func (s *Store) ListPRFooterDemand(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.arcanum_pr_id,coalesce(t.source_author,'') FROM tasks t LEFT JOIN arcanum_pr_metadata m ON m.pr_id=t.arcanum_pr_id WHERE t.card_type='pr' AND t.column<>'done' AND coalesce(m.lifecycle,'')<>'merged'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id, author string
		if err := rows.Scan(&id, &author); err != nil {
			return nil, err
		}
		n, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return nil, err
		}
		out[n] = author
	}
	return out, rows.Err()
}

func (s *Store) ListRegisteredTaskPollers(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT substr(key,length('poller.board.task.')+1) FROM daemon_state WHERE key LIKE 'poller.board.task.%' AND json_extract(value,'$.enabled')=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
