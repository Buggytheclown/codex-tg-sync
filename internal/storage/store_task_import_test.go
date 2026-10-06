package storage

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestTaskSchemaUpgradePreservesLegacyRequestsAndNullableSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	legacy := testExternalLaunchRequest("legacy")
	legacy.AutoStart = true
	if _, err := s.EnqueueExternalLaunchRequests(ctx, legacy.Source, []model.ExternalLaunchRequest{legacy}); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP INDEX idx_external_launch_task`, `DROP INDEX idx_external_launch_task_event`, `DROP TABLE task_events`, `DROP TABLE task_polling`, `DROP TABLE tasks`, `DROP TABLE task_templates`, `ALTER TABLE external_launch_requests DROP COLUMN task_id`, `ALTER TABLE external_launch_requests DROP COLUMN trigger_event_id`, `ALTER TABLE external_launch_requests DROP COLUMN target_thread_id`, `ALTER TABLE external_launch_requests DROP COLUMN approval_policy`, `ALTER TABLE external_launch_requests DROP COLUMN approvals_reviewer`, `ALTER TABLE external_launch_requests DROP COLUMN sandbox_mode`, `ALTER TABLE sync_message_receipts DROP COLUMN turn_id`} {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	request, err := s.GetExternalLaunchRequest(ctx, legacy.ID)
	if err != nil || request == nil || request.Prompt != legacy.Prompt || !request.AutoStart || request.TaskID != "" || request.ApprovalPolicy != nil || request.ApprovalsReviewer != nil || request.SandboxMode != nil {
		t.Fatalf("legacy request changed: %#v %v", request, err)
	}
	for _, table := range []string{"tasks", "task_polling", "task_templates", "task_events"} {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("table %s: %d %v", table, n, err)
		}
	}
	for _, table := range []string{"thread_bindings", "observer_targets", "thread_panels"} {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("retired table %s: %d %v", table, n, err)
		}
	}
}

func TestTaskLegacyImportIsAtomicIdempotentAndPreservesPolicy(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	legacy := testExternalLaunchRequest("legacy")
	legacy.AutoStart = true
	if _, err := s.EnqueueExternalLaunchRequests(ctx, legacy.Source, []model.ExternalLaunchRequest{legacy}); err != nil {
		t.Fatal(err)
	}
	in := model.TaskSourceImport{Key: "legacy-pr-source", Title: "Reviews", TemplateName: "Reviews", Polling: model.TaskPolling{Type: "pr_source", Mode: model.PollingRepeat, Config: []byte(`{}`)}, Definition: model.TaskTemplateDefinition{Title: "Review {{pr_id}}", Config: model.TaskConfig{InitialPrompt: "review {{pr_id}}", ContinuationPrompt: "continue {{pr_id}}"}, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"{{pr_id}}"}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}}, Legacy: []model.LegacyTaskImport{{SourceKey: "review:1", EventKey: "assigned:1", RequestIDs: []string{legacy.ID, "missing"}, Variables: map[string]string{"pr_id": "1"}, Cursor: []byte(`{"revision":"old"}`)}}}
	if _, err := s.BootstrapTaskSource(ctx, in); err == nil {
		t.Fatal("missing legacy request accepted")
	}
	tasks, _ := s.ListTasks(ctx)
	templates, _ := s.ListTaskTemplates(ctx)
	if len(tasks) != 0 || len(templates) != 0 {
		t.Fatalf("partial import: %d tasks %d templates", len(tasks), len(templates))
	}
	in.Legacy[0].RequestIDs = []string{legacy.ID}
	source, err := s.BootstrapTaskSource(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	request, _ := s.GetExternalLaunchRequest(ctx, legacy.ID)
	if request.TaskID == "" || !request.AutoStart || request.ApprovalPolicy != nil || request.Prompt != legacy.Prompt {
		t.Fatalf("legacy policy changed: %#v", request)
	}
	child, _ := s.GetTask(ctx, request.TaskID)
	if child.SourceKey != "review:1" || child.Polling == nil || string(child.Polling.Config) != `{"pr_id":"1"}` || string(child.Polling.Cursor) != `{"revision":"old"}` {
		t.Fatalf("legacy child: %#v", child)
	}
	if _, err := s.AcceptTaskPollingOutcome(ctx, model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Cursor: []byte(`{}`), Events: []model.PollingEvent{{Key: "assigned:1", SourceKey: "review:1", Payload: []byte(`{}`)}}}, 123); err != nil {
		t.Fatal(err)
	}
	child, _ = s.GetTask(ctx, request.TaskID)
	if len(child.Requests) != 1 {
		t.Fatalf("legacy barrier lost: %#v", child.Requests)
	}
	source, err = s.MoveTask(ctx, source.ID, model.MoveTaskInput{ExpectedVersion: source.Version, Column: model.TaskTODO})
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.BootstrapTaskSource(ctx, in)
	if err != nil || again.ID != source.ID || again.Column != model.TaskTODO {
		t.Fatalf("second import replaced edits: %#v %v", again, err)
	}
	tasks, _ = s.ListTasks(ctx)
	templates, _ = s.ListTaskTemplates(ctx)
	if len(tasks) != 2 || len(templates) != 1 {
		t.Fatalf("duplicate import: %d tasks %d templates", len(tasks), len(templates))
	}
}

func TestTaskTemplateCannotDeleteReferencedSource(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	source, _ := makeTaskBoardSource(t, s, "")
	tmpl, _ := s.GetTaskTemplate(ctx, source.Polling.Action.TemplateID)
	if err := s.DeleteTaskTemplate(ctx, tmpl.ID, tmpl.Version); err != ErrTaskTemplateInUse {
		t.Fatalf("delete linked template: %v", err)
	}
	// A copied instance does not need its source template to execute later.
	plain, err := s.CreateTaskTemplate(ctx, model.SaveTaskTemplateInput{Name: "plain", Definition: model.TaskTemplateDefinition{Title: "plain", Config: model.TaskConfig{InitialPrompt: "fixed"}}})
	if err != nil {
		t.Fatal(err)
	}
	instance, err := s.CreateTask(ctx, model.CreateTaskInput{TemplateID: plain.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteTaskTemplate(ctx, plain.ID, plain.Version); err != nil {
		t.Fatal(err)
	}
	instance, err = s.GetTask(ctx, instance.ID)
	if err != nil || instance.Config.InitialPrompt != "fixed" {
		t.Fatalf("deleted template broke snapshot: %#v %v", instance, err)
	}
}
