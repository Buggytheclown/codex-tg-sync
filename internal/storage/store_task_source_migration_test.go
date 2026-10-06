package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestSourceDefinitionMigrationPreservesHistoryAndOwnedEdits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "board.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = s.db.Exec(`ALTER TABLE tasks ADD COLUMN template_id TEXT; ALTER TABLE tasks ADD COLUMN template_version INTEGER NOT NULL DEFAULT 0; ALTER TABLE tasks DROP COLUMN source_task_id`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS task_templates(id TEXT PRIMARY KEY,name TEXT,version INTEGER,definition_json TEXT,created_at TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		definition := fmt.Sprintf(`{"title":"Child %d","config":{"initial_prompt":"owned prompt %d","execution":{"cwd":"/project/%d"}}}`, i, i, i)
		if _, err = s.db.ExecContext(ctx, `INSERT INTO task_templates VALUES(?,?,1,?,'old','old')`, fmt.Sprintf("tpl%d", i), "old", definition); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO tasks(id,title,column,task_config_json,version,created_at,updated_at) VALUES(?,?,'polling',?,1,'old','old')`, fmt.Sprintf("source%d", i), "source", `{"initial_prompt":"unused remote debugger","execution":{"cwd":"/wrong"}}`); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO task_polling(task_id,type,mode,config_json,action_json,cursor_json,generation,last_error) VALUES(?,'pr','repeat','{}',?,'{"seen":true}',7,'prior diagnostic')`, fmt.Sprintf("source%d", i), fmt.Sprintf(`{"type":"create_task","template_id":"tpl%d"}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 82; i++ {
		id := fmt.Sprintf("history%d", i)
		if _, err = s.db.ExecContext(ctx, `INSERT INTO tasks(id,title,column,template_id,template_version,task_config_json,version,created_at,updated_at) VALUES(?,?,'inbox','tpl0',1,?,3,'old','old')`, id, id, fmt.Sprintf(`{"initial_prompt":"historical %d","execution":{"cwd":"/history/%d"}}`, i, i)); err != nil {
			t.Fatal(err)
		}
		if _, err = s.db.ExecContext(ctx, `INSERT INTO task_events(id,task_id,kind,event_key,result_task_id,payload_json,created_at) VALUES(?,'source0','polling_event',?,?, '{}','old')`, "event"+id, id, id); err != nil {
			t.Fatal(err)
		}
	}
	request := testExternalLaunchRequest("frozen")
	if _, err = s.EnqueueExternalLaunchRequests(ctx, request.Source, []model.ExternalLaunchRequest{request}); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetExternalLaunchRequest(ctx, request.ID)
	var historyBefore string
	if err = s.db.QueryRow(`SELECT group_concat(task_config_json,'|') FROM tasks WHERE id LIKE 'history%'`).Scan(&historyBefore); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		var raw, cursor, lastError string
		var generation int
		if err = s.db.QueryRow(`SELECT action_json,cursor_json,generation,last_error FROM task_polling WHERE task_id=?`, fmt.Sprintf("source%d", i)).Scan(&raw, &cursor, &generation, &lastError); err != nil {
			t.Fatal(err)
		}
		var action map[string]json.RawMessage
		json.Unmarshal([]byte(raw), &action)
		if len(action["child_definition"]) == 0 || len(action["template_id"]) != 0 || !strings.Contains(string(action["child_definition"]), fmt.Sprintf("owned prompt %d", i)) || strings.Contains(raw, "remote debugger") {
			t.Fatalf("source not migrated from owned definition: %s", raw)
		}
		if cursor != `{"seen":true}` || generation != 7 || lastError != "prior diagnostic" {
			t.Fatalf("polling runtime changed: %s %d %s", cursor, generation, lastError)
		}
	}
	var historyAfter string
	if err = s.db.QueryRow(`SELECT group_concat(task_config_json,'|') FROM tasks WHERE id LIKE 'history%'`).Scan(&historyAfter); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetExternalLaunchRequest(ctx, request.ID)
	if historyAfter != historyBefore || !reflect.DeepEqual(before, after) {
		t.Fatal("migration changed historical task config or frozen request")
	}
	var provenance int
	if err = s.db.QueryRow(`SELECT count(*) FROM tasks WHERE id LIKE 'history%' AND source_task_id='source0'`).Scan(&provenance); err != nil || provenance != 82 {
		t.Fatalf("provenance %d %v", provenance, err)
	}
	if _, err = s.db.Exec(`UPDATE task_polling SET action_json='{"type":"create_task","child_definition":{"title":"edited","config":{"initial_prompt":"local edit"}}}' WHERE task_id='source0'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var raw string
	if err = s.db.QueryRow(`SELECT action_json FROM task_polling WHERE task_id='source0'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "local edit") {
		t.Fatalf("repeat migration overwrote owned source: %s", raw)
	}
}

func TestSourceDefinitionMigrationRollsBackOnMissingTemplate(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS task_templates(id TEXT PRIMARY KEY,name TEXT,version INTEGER,definition_json TEXT,created_at TEXT,updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO task_templates VALUES('a-valid','valid',1,'{"title":"child","config":{"initial_prompt":"prompt"}}','old','old')`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-valid", "z-missing"} {
		if _, err := s.db.Exec(`INSERT INTO tasks(id,title,column,task_config_json,version,created_at,updated_at) VALUES(?,?,'polling','{}',1,'old','old')`, id, id); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO task_polling(task_id,type,mode,config_json,action_json,generation) VALUES(?,'pr','repeat','{}',?,1)`, id, fmt.Sprintf(`{"type":"create_task","template_id":"%s"}`, id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.initializeTasks(ctx); err == nil {
		t.Fatal("missing template silently lost source definition")
	}
	var raw string
	s.db.QueryRow(`SELECT action_json FROM task_polling WHERE task_id='a-valid'`).Scan(&raw)
	if !strings.Contains(raw, "template_id") {
		t.Fatalf("partial migration committed: %s", raw)
	}
}

func TestFreshBoardDoesNotCreateTemplateStorage(t *testing.T) {
	s := openTestStore(t)
	var tables int
	if err := s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='task_templates'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatal("fresh database creates obsolete template entity")
	}
	var cols int
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('tasks') WHERE name IN ('template_id','template_version')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 0 {
		t.Fatal("fresh tasks retain shared template relation")
	}
}
