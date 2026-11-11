package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Add and backfill together, once. A crash cannot leave a partially backfilled
// schema, and an unknown date must not be guessed again on subsequent opens.
func (s *Store) migrateTaskDoneDates(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('tasks') WHERE name='done_at')`).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `ALTER TABLE tasks ADD COLUMN done_at TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	if err := backfillTaskDoneDatesTx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillTaskDoneDatesTx(ctx context.Context, tx *sql.Tx) error {
	// Only the most recent column-affecting event can describe the current stint.
	// A legacy polling_event lacks its destination: it invalidates earlier evidence.
	rows, err := tx.QueryContext(ctx, `SELECT e.task_id,e.kind,e.payload_json,e.created_at,t.created_at
 FROM task_events e JOIN tasks t ON t.id=e.task_id WHERE t.column='done'
 AND (e.kind IN ('created','pr_merged','moved')
 OR (e.kind='polling_event' AND coalesce(e.result_task_id,'')=''))`)
	if err != nil {
		return err
	}
	type evidence struct {
		at, entry      time.Time
		known, invalid bool
	}
	dates := map[string]evidence{}
	for rows.Next() {
		var id, kind, raw, at, created string
		if err = rows.Scan(&id, &kind, &raw, &at, &created); err != nil {
			rows.Close()
			return err
		}
		previous := dates[id]
		var payload struct {
			From   string `json:"from"`
			To     string `json:"to"`
			Column string `json:"column"`
		}
		payloadErr := json.Unmarshal([]byte(raw), &payload)
		if payloadErr == nil && kind == "moved" && validTaskColumn(payload.From) && payload.From == payload.To {
			continue
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, at)
		invalidMove := kind == "moved" && (!validTaskColumn(payload.From) || !validTaskColumn(payload.To))
		if parseErr != nil || payloadErr != nil || invalidMove {
			// With an unorderable event, no earlier entry can safely stand for the last.
			previous.invalid = true
			dates[id] = previous
			continue
		}
		known := kind == "pr_merged" || (kind == "moved" && payload.To == "done") || (kind == "created" && payload.Column == "done")
		entry := parsed
		if kind == "created" {
			var entryErr error
			entry, entryErr = time.Parse(time.RFC3339Nano, created)
			known = known && entryErr == nil
		}
		if parsed.After(previous.at) {
			previous.at = parsed
			previous.entry = entry
			previous.known = known
		} else if parsed.Equal(previous.at) {
			previous.known = previous.known && known && previous.entry.Equal(entry)
		}
		dates[id] = previous
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, date := range dates {
		if !date.known || date.invalid {
			continue
		}
		if _, err = tx.ExecContext(ctx, `UPDATE tasks SET done_at=? WHERE id=?`, date.entry.UTC().Format(time.RFC3339Nano), id); err != nil {
			return err
		}
	}
	return nil
}
