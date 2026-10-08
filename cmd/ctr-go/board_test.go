package main

import (
	"context"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/polling"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/model"
)

type boardImportSink struct {
	legacy   []model.ExternalLaunchRequest
	imports  []model.TaskSourceImport
	migrated bool
}

func (s *boardImportSink) ListLegacyLaunchRequests(context.Context) ([]model.ExternalLaunchRequest, error) {
	return s.legacy, nil
}
func (s *boardImportSink) BootstrapTaskSource(_ context.Context, in model.TaskSourceImport) (*model.Task, error) {
	s.imports = append(s.imports, in)
	return &model.Task{}, nil
}
func TestBootstrapTaskBoardPreservesLegacyBarriersAndPausedSchedule(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":1,"timezone":"UTC","tasks":[{"id":"daily","cron":"0 10 * * *","cwd":"/work","prompt":"frozen prompt","enabled":false}]}`
	if err := os.WriteFile(filepath.Join(dir, "cron.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{Paths: config.Paths{Home: dir}, ArcanumReviewEnabled: true, ArcanumReviewLogin: "reviewer", ArcanumReviewCWD: "/work", ArcanumReviewPollInterval: time.Minute, ExternalRequestsTopicID: 77, ExternalApprovalPolicy: "untrusted", ExternalApprovalsReviewer: "user", ExternalSandboxMode: "read-only", ArcanumAutoStartAuthors: []string{"alice"}}
	sink := &boardImportSink{legacy: []model.ExternalLaunchRequest{{ID: "arcanum_review:12", Source: "arcanum_review", ExternalID: "12", Title: "legacy PR"}, {ID: "cron:daily:2026-10-05", Source: "cron", ExternalID: "daily:2026-10-05"}}}
	if err := bootstrapTaskBoard(context.Background(), cfg, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.imports) != 3 {
		t.Fatalf("imports=%#v", sink.imports)
	}
	review, cron := sink.imports[0], sink.imports[2]
	if review.Title != "PR review" || review.Polling.Action.InitialLaunch.Mode != "trusted_authors" || review.Polling.Action.InitialLaunch.Authors[0] != "alice" || sink.imports[1].Polling.Action.InitialLaunch.Mode != "manual" || cron.Polling.Action.InitialLaunch.Mode != "manual" {
		t.Fatalf("policies=%#v", sink.imports)
	}
	var reviewWait, changesWait polling.Config
	if err := json.Unmarshal(review.Definition.Polling.Config, &reviewWait); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sink.imports[1].Definition.Polling.Config, &changesWait); err != nil {
		t.Fatal(err)
	}
	if len(reviewWait.Conditions) != 2 || reviewWait.Conditions[0] != polling.ConditionCommit || reviewWait.Conditions[1] != polling.ConditionAnswered {
		t.Fatalf("review conditions=%#v", reviewWait.Conditions)
	}
	if len(changesWait.Conditions) != 2 || changesWait.Conditions[0] != polling.ConditionResolved || changesWait.Conditions[1] != polling.ConditionChanges {
		t.Fatalf("changes conditions=%#v", changesWait.Conditions)
	}

	if review.Legacy[0].SourceKey != "arcanum_review:12" || review.Legacy[0].EventKey != "arcanum_review:12" || review.Legacy[0].RequestIDs[0] != "arcanum_review:12" {
		t.Fatalf("review=%#v", review)
	}
	if cron.Column != model.TaskTODO || cron.Legacy[0].EventKey != "cron:daily:2026-10-05" || cron.Definition.Config.InitialPrompt != "frozen prompt" {
		t.Fatalf("cron=%#v", cron)
	}
	if cron.Definition.Config.Execution.ApprovalPolicy != "untrusted" || cron.Definition.Config.Execution.SandboxMode != "read-only" || cron.Definition.Config.Execution.ApprovalsReviewer != "user" {
		t.Fatalf("permissions=%#v", cron.Definition.Config.Execution)
	}
}

func (s *boardImportSink) TaskBoardMigrationDone(context.Context, string) (bool, error) {
	return s.migrated, nil
}
func (s *boardImportSink) CompleteTaskBoardMigration(context.Context, string) error {
	s.migrated = true
	return nil
}
func TestTaskBoardCronImportSkipsHistoricalFileAfterMigration(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{Paths: config.Paths{Home: dir}, ExternalRequestsTopicID: 77}
	sink := &boardImportSink{}
	if err := bootstrapTaskBoard(context.Background(), cfg, sink); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cron.json"), []byte("invalid historical config"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapTaskBoard(context.Background(), cfg, sink); err != nil {
		t.Fatalf("reread old config: %v", err)
	}
}

func TestFreshCronImportPreservesAutomaticLaunchPolicy(t *testing.T) {
	dir := t.TempDir()
	data := `{"version":1,"timezone":"UTC","tasks":[{"id":"daily","cron":"0 10 * * *","cwd":"/work","prompt":"run","launch_policy":"auto","enabled":true}]}`
	if err := os.WriteFile(filepath.Join(dir, "cron.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	sink := &boardImportSink{}
	if err := bootstrapTaskBoard(context.Background(), config.Config{Paths: config.Paths{Home: dir}, ExternalRequestsTopicID: 77}, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.imports) != 1 || sink.imports[0].Polling.Action.InitialLaunch.Mode != "auto" {
		t.Fatalf("imports=%#v", sink.imports)
	}
}
