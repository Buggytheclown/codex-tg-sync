package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestLegacyMergeProjectionIsReadOnlyAndUsesStableProvenance(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	parent, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", SourceKey: "source:arcanum_changes"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "child", Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"42"}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE tasks SET source_task_id=? WHERE id=?`, parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		got, err := s.GetTask(ctx, child.ID)
		if err != nil {
			t.Fatal(err)
		}
		var cfg struct {
			Complete bool `json:"complete_on_merge"`
		}
		if err := json.Unmarshal(got.Polling.Config, &cfg); err != nil {
			t.Fatal(err)
		}
		if !cfg.Complete || got.Version != child.Version || got.Polling.Generation != child.Polling.Generation {
			t.Fatalf("projection %#v", got)
		}
		// Changing the source's editable settings cannot change legacy provenance.
		if _, err := s.db.Exec(`INSERT INTO task_polling(task_id,type,mode,config_json,action_json,cursor_json,generation,next_check_at,last_attempt_at,last_success_at,last_error) VALUES(?,'cron','repeat','{}','{}','{}',1,'','','','') ON CONFLICT(task_id) DO UPDATE SET type='cron',config_json='{}'`, parent.ID); err != nil {
			t.Fatal(err)
		}
	}
	var raw string
	if err := s.db.QueryRow(`SELECT config_json FROM task_polling WHERE task_id=?`, child.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `{"pr_id":"42"}` {
		t.Fatalf("read persisted projection %s", raw)
	}
}
