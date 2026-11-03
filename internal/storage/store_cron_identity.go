package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

// Old event keys used a local date. Retain their protection without rewriting
// history, child identities or frozen requests. Unknown old times protect the date.
func legacyCronEventAcceptedTx(ctx context.Context, tx *sql.Tx, taskID string, event model.PollingEvent) (bool, error) {
	var facts struct {
		ScheduledAt string `json:"scheduled_at"`
	}
	if json.Unmarshal(event.Payload, &facts) != nil {
		return false, nil
	}
	slot, err := time.Parse(time.RFC3339, facts.ScheduledAt)
	if err != nil {
		return false, nil
	}
	suffix := slot.UTC().Format(time.RFC3339)
	if !strings.HasSuffix(event.Key, ":"+suffix) {
		return false, nil
	}
	prefix := strings.TrimSuffix(event.Key, suffix)
	// The existing flat-to-rules conversion names the original rule legacy-wait.
	// Its pre-conversion events retain their unprefixed keys.
	flatPrefix := strings.TrimPrefix(prefix, "rule:legacy-wait:")
	// Prefix includes the owner and, for rules, the stable rule ID. Scan only
	// that scope, including known instants whose old local date differs by zone.
	rows, err := tx.QueryContext(ctx, `SELECT event_key,payload_json FROM task_events
		WHERE task_id=? AND event_key IS NOT NULL AND event_key<>''
		AND (substr(event_key,1,length(?))=? OR substr(event_key,1,length(?))=?)`,
		taskID, prefix, prefix, flatPrefix, flatPrefix)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, payload string
		if err := rows.Scan(&key, &payload); err != nil {
			return false, err
		}
		date := strings.TrimPrefix(key, prefix)
		if date == key {
			date = strings.TrimPrefix(key, flatPrefix)
			if date == suffix {
				return true, nil // Same timestamp recorded before flat-to-rules conversion.
			}
		}
		if _, err := time.Parse(time.DateOnly, date); err != nil {
			continue // Timestamp keys are handled by exact event-key deduplication.
		}
		var old struct {
			ScheduledAt string `json:"scheduled_at"`
		}
		if json.Unmarshal([]byte(payload), &old) != nil {
			old.ScheduledAt = ""
		}
		oldSlot, err := time.Parse(time.RFC3339, old.ScheduledAt)
		known := err == nil && oldSlot.Format(time.DateOnly) == date
		if (known && oldSlot.Equal(slot)) || (!known && date == slot.Format(time.DateOnly)) {
			return true, nil
		}
	}
	return false, rows.Err()
}
