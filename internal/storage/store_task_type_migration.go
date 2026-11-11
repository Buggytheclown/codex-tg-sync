package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/mideco-tech/codex-tg/internal/model"
)

var ErrCardMigrationRequired = errors.New("card type migration required; stop the daemon and run ctr-go board migrate-card-types --dry-run, then --apply <report>")

type CardTypeMigration struct {
	FormatVersion   int                    `json:"format_version"`
	Policy          string                 `json:"policy"`
	Digest          string                 `json:"digest"`
	AlreadyMigrated bool                   `json:"already_migrated"`
	Cards           []CardTypeMigrationRow `json:"cards"`
}
type CardTypeMigrationRow struct {
	ID          string `json:"id"`
	Version     int64  `json:"version"`
	CardType    string `json:"card_type"`
	PRID        string `json:"arcanum_pr_id,omitempty"`
	ArchivePR   bool   `json:"archive_pr_reference"`
	ClearTicket bool   `json:"clear_tracker"`
}
type migrationQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func tableData(ctx context.Context, q migrationQuerier, table string) ([]map[string]any, error) {
	rows, err := q.QueryContext(ctx, `SELECT * FROM `+table+` ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	data := []map[string]any{}
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err = rows.Scan(pointers...); err != nil {
			return nil, err
		}
		row := map[string]any{}
		for i, key := range columns {
			if raw, ok := values[i].([]byte); ok {
				values[i] = string(raw)
			}
			row[key] = values[i]
		}
		data = append(data, row)
	}
	return data, rows.Err()
}
func migrationText(row map[string]any, key string) string {
	if value, ok := row[key].(string); ok {
		return value
	}
	return ""
}

func cardMigrationSnapshot(ctx context.Context, q migrationQuerier) (CardTypeMigration, []map[string]any, error) {
	report := CardTypeMigration{FormatVersion: 1, Policy: "archive_task_pr_reference", Cards: []CardTypeMigrationRow{}}
	tasks, err := tableData(ctx, q, "tasks")
	if err != nil {
		return report, nil, err
	}
	polling, err := tableData(ctx, q, "task_polling")
	if err != nil {
		return report, nil, err
	}
	events, err := tableData(ctx, q, "task_events")
	if err != nil {
		return report, nil, err
	}
	var templates []map[string]any
	for _, row := range polling {
		var legacy struct {
			TemplateID string `json:"template_id"`
		}
		if err := json.Unmarshal([]byte(migrationText(row, "action_json")), &legacy); err != nil {
			return report, nil, err
		}
		if legacy.TemplateID != "" {
			templates, err = tableData(ctx, q, "task_templates")
			if err != nil {
				return report, nil, err
			}
			break
		}
	}
	raw, err := json.Marshal([]any{tasks, polling, events, templates})
	if err != nil {
		return report, nil, err
	}
	sum := sha256.Sum256(raw)
	report.Digest = hex.EncodeToString(sum[:])
	kinds := map[string]string{}
	sources := map[string]bool{}
	for _, row := range polling {
		id := migrationText(row, "task_id")
		kinds[id] = migrationText(row, "type")
		var action model.TaskPollingAction
		if err = json.Unmarshal([]byte(migrationText(row, "action_json")), &action); err != nil {
			return report, nil, fmt.Errorf("invalid stored polling action")
		}
		sources[id] = action.Type == model.PollingCreateTask
	}
	schemaRows, err := q.QueryContext(ctx, `SELECT name FROM pragma_table_info('tasks') WHERE name IN ('card_type','arcanum_pr_id','parent_task_id')`)
	if err != nil {
		return report, nil, err
	}
	typedColumns := 0
	for schemaRows.Next() {
		typedColumns++
	}
	err = schemaRows.Err()
	schemaRows.Close()
	if err != nil {
		return report, nil, err
	}
	if typedColumns != 0 && typedColumns != 3 {
		return report, nil, errors.New("incomplete card type schema")
	}
	report.AlreadyMigrated = typedColumns == 3
	legacyParents := map[string]string{}
	for _, event := range events {
		source := migrationText(event, "task_id")
		child := migrationText(event, "result_task_id")
		if sources[source] && child != "" && child != source && legacyParents[child] == "" {
			legacyParents[child] = source
		}
	}
	seen := map[string]string{}
	for _, row := range tasks {
		id := migrationText(row, "id")
		key := migrationText(row, "source_key")
		sourceKind := kinds[migrationText(row, "source_task_id")]
		if sourceKind == "" {
			sourceKind = kinds[legacyParents[id]]
		}
		entry := CardTypeMigrationRow{ID: id, CardType: model.CardTask}
		if v, ok := row["version"].(int64); ok {
			entry.Version = v
		}
		if report.AlreadyMigrated {
			entry.CardType = migrationText(row, "card_type")
			entry.PRID = migrationText(row, "arcanum_pr_id")
			report.Cards = append(report.Cards, entry)
			continue
		}
		switch {
		case sources[id] || strings.HasPrefix(key, "source:"):
			entry.CardType = ""
		case strings.HasPrefix(key, "arcanum_review:") || strings.HasPrefix(key, "arcanum_changes:") || model.PRSourceType(sourceKind):
			entry.CardType = model.CardPR
		case strings.HasPrefix(key, "cron:") || sourceKind == "cron":
			entry.CardType = model.CardSchedule
		}
		url := migrationText(row, "source_url")
		prID, parseErr := model.ParseArcanumPR(url)
		if entry.CardType == model.CardPR {
			if parseErr != nil {
				return report, nil, fmt.Errorf("%w: PR source card has no valid PR URL", ErrTaskInvalid)
			}
			if seen[prID] != "" {
				return report, nil, fmt.Errorf("%w: multiple PR-source cards have the same PR; resolve before migration", ErrTaskConflict)
			}
			seen[prID] = id
			entry.PRID = prID
			entry.ClearTicket = migrationText(row, "ticket_url") != ""
		} else if entry.CardType == model.CardTask && parseErr == nil {
			entry.ArchivePR = true
			entry.PRID = prID
		}
		report.Cards = append(report.Cards, entry)
	}
	return report, tasks, nil
}

func PreviewCardTypeMigration(ctx context.Context, path string) (CardTypeMigration, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return CardTypeMigration{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CardTypeMigration{}, err
	}
	defer tx.Rollback()
	report, _, err := cardMigrationSnapshot(ctx, tx)
	return report, err
}

func needsCardMigration(ctx context.Context, db *sql.DB) (bool, error) {
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='tasks'`).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, nil
	}
	var typed int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('tasks') WHERE name='card_type'`).Scan(&typed); err != nil {
		return false, err
	}
	if typed != 0 {
		return false, nil
	}
	return true, nil
}

// ApplyCardTypeMigration never initializes the Store before checking the report.
// The caller must stop the daemon and preserve an offline SQLite backup first.
func ApplyCardTypeMigration(ctx context.Context, path string, expected CardTypeMigration) (err error) {
	if expected.FormatVersion != 1 || expected.Policy != "archive_task_pr_reference" || len(expected.Digest) != 64 {
		return ErrTaskInvalid
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, `PRAGMA busy_timeout=5000; PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	defer func() {
		_, restoreErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`)
		if err == nil {
			err = restoreErr
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, originals, err := cardMigrationSnapshot(ctx, tx)
	if err != nil {
		return err
	}
	if current.AlreadyMigrated {
		return nil
	}
	if current.Digest != expected.Digest {
		return fmt.Errorf("%w: migration report is stale; run dry-run again", ErrTaskConflict)
	}
	// Commands are derived from the verified database, never executed from input.
	wanted, _ := json.Marshal(expected.Cards)
	actual, _ := json.Marshal(current.Cards)
	if string(wanted) != string(actual) {
		return fmt.Errorf("%w: migration report decisions were altered", ErrTaskInvalid)
	}
	ddl := strings.Replace(taskSchemaSQL, "IF NOT EXISTS tasks (", "tasks_new (", 1)
	// Keep the self reference pointed at the final table name during the swap.
	if _, err = tx.ExecContext(ctx, ddl); err != nil {
		return err
	}
	columnRows, err := tx.QueryContext(ctx, `PRAGMA table_info(tasks)`)
	if err != nil {
		return err
	}
	var columns []string
	hadDoneAt := false
	type extraColumn struct{ name, kind string }
	var extras []extraColumn
	known := map[string]bool{}
	freshRows, err := tx.QueryContext(ctx, `PRAGMA table_info(tasks_new)`)
	if err != nil {
		columnRows.Close()
		return err
	}
	for freshRows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = freshRows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			freshRows.Close()
			columnRows.Close()
			return err
		}
		known[name] = true
	}
	if err = freshRows.Err(); err != nil {
		freshRows.Close()
		columnRows.Close()
		return err
	}
	freshRows.Close()
	for columnRows.Next() {
		var cid, notnull, pk int
		var name, kind string
		var defaultValue any
		if err = columnRows.Scan(&cid, &name, &kind, &notnull, &defaultValue, &pk); err != nil {
			columnRows.Close()
			return err
		}
		if name == "done_at" {
			hadDoneAt = true
		}
		columns = append(columns, `"`+strings.ReplaceAll(name, `"`, `""`)+`"`)
		if !known[name] {
			extras = append(extras, extraColumn{name, kind})
		}
	}
	if err = columnRows.Err(); err != nil {
		columnRows.Close()
		return err
	}
	columnRows.Close()
	for _, extra := range extras {
		switch strings.ToUpper(extra.kind) {
		case "TEXT", "INTEGER", "REAL", "BLOB", "NUMERIC", "":
		default:
			return errors.New("unsupported legacy task column affinity")
		}
		if _, err = tx.ExecContext(ctx, `ALTER TABLE tasks_new ADD COLUMN "`+strings.ReplaceAll(extra.name, `"`, `""`)+`" `+extra.kind); err != nil {
			return err
		}
	}
	list := strings.Join(columns, ",")
	originalsByID := map[string]map[string]any{}
	for _, row := range originals {
		originalsByID[migrationText(row, "id")] = row
	}
	now := model.NowString()
	for _, card := range current.Cards {
		prID := card.PRID
		if card.CardType != model.CardPR {
			prID = ""
		}
		selectList := list
		if card.ClearTicket {
			selectList = strings.Replace(selectList, `"ticket_url"`, `''`, 1)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO tasks_new (`+list+`,card_type,arcanum_pr_id) SELECT `+selectList+`,?,? FROM tasks WHERE id=?`, card.CardType, prID, card.ID); err != nil {
			return err
		}
		original := originalsByID[card.ID]
		if card.ArchivePR {
			if _, err = tx.ExecContext(ctx, `UPDATE tasks_new SET source_url=NULL WHERE id=?`, card.ID); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"pr_id": card.PRID, "source_url": migrationText(original, "source_url"), "reason": "archive_task_pr_reference"})
			if _, err = tx.ExecContext(ctx, `INSERT INTO task_events(id,task_id,kind,payload_json,created_at) VALUES(?,?,'legacy_pr_unlinked',?,?)`, uuid.NewString(), card.ID, string(payload), now); err != nil {
				return err
			}
		}
		if card.ClearTicket {
			if _, err = tx.ExecContext(ctx, `UPDATE tasks_new SET ticket_url='' WHERE id=?`, card.ID); err != nil {
				return err
			}
			payload, _ := json.Marshal(map[string]string{"ticket_url": migrationText(original, "ticket_url")})
			if _, err = tx.ExecContext(ctx, `INSERT INTO task_events(id,task_id,kind,payload_json,created_at) VALUES(?,?,'legacy_tracker_unlinked',?,?)`, uuid.NewString(), card.ID, string(payload), now); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `DROP TABLE tasks; ALTER TABLE tasks_new RENAME TO tasks;`+taskTypeIndexesSQL); err != nil {
		return err
	}
	if err = migrateOwnedSourceDefinitionsTx(ctx, tx); err != nil {
		return err
	}
	if !hadDoneAt {
		if err = backfillTaskDoneDatesTx(ctx, tx); err != nil {
			return err
		}
	}
	checks, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	invalid := checks.Next()
	checkErr := checks.Err()
	checks.Close()
	if checkErr != nil {
		return checkErr
	}
	if invalid {
		return errors.New("migration failed foreign key check")
	}
	return tx.Commit()
}
