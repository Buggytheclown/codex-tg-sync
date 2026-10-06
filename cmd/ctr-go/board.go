package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/cronpoller"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/polling"
)

type boardBootstrapSink interface {
	ListLegacyLaunchRequests(context.Context) ([]model.ExternalLaunchRequest, error)
	BootstrapTaskSource(context.Context, model.TaskSourceImport) (*model.Task, error)
	TaskBoardMigrationDone(context.Context, string) (bool, error)
	CompleteTaskBoardMigration(context.Context, string) error
}

func boardJSON(value any) json.RawMessage { b, _ := json.Marshal(value); return b }

// Bootstrap performs only local reads and atomic SQLite imports. The runner is
// started afterward, and the old PR/cron owners are never started alongside it.
func bootstrapTaskBoard(ctx context.Context, cfg config.Config, sink boardBootstrapSink) error {
	legacy, err := sink.ListLegacyLaunchRequests(ctx)
	if err != nil {
		return err
	}
	settings := model.TaskExecutionSettings{CWD: cfg.ExternalRequestDefaultCWD, ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"}
	if cfg.ArcanumReviewEnabled {
		settings.CWD = cfg.ArcanumReviewCWD
		for _, source := range []string{arcanumreview.Source, arcanumreview.ChangesSource} {
			kind, title, prompt := polling.TypeReview, "Assigned PRs", "$arc-pr-review-stefania {{pr_url}}"
			conditions := []string{polling.ConditionCommit, polling.ConditionAnswered}
			if source == arcanumreview.ChangesSource {
				kind, title, prompt = polling.TypeChanges, "PR feedback", "{{pr_url}} прочитай замечания и исправь код по ним"
				conditions = []string{polling.ConditionResolved, polling.ConditionChanges}
			}
			wait := model.TaskPolling{Type: polling.TypePRWait, Mode: model.PollingOnce, Action: model.TaskPollingAction{Type: model.PollingResumeTask}, Config: boardJSON(polling.Config{PRID: "{{pr_id}}", Login: cfg.ArcanumReviewLogin, Author: "{{author}}", Conditions: conditions, IntervalSeconds: max(10, int(cfg.ArcanumReviewPollInterval.Seconds()))})}
			in := model.TaskSourceImport{Key: source, Title: title, Polling: model.TaskPolling{Type: kind, Mode: model.PollingRepeat, Config: boardJSON(polling.Config{Login: cfg.ArcanumReviewLogin, IntervalSeconds: max(10, int(cfg.ArcanumReviewPollInterval.Seconds()))})}, Definition: model.TaskDefinition{Title: "{{pr_title}}", Config: model.TaskConfig{InitialPrompt: prompt, ContinuationPrompt: "Продолжи работу с {{pr_url}}: прочитай текущее состояние PR и замечаний, выполни следующий проход проверки или исправлений.", Execution: settings}, Polling: &wait}}
			groups := map[string]*model.LegacyTaskImport{}
			for _, request := range legacy {
				if request.Source != source || request.TaskID != "" {
					continue
				}
				key := source + ":" + request.ExternalID
				entry := groups[key]
				if entry == nil {
					copy := wait
					copy.Config = boardJSON(polling.Config{PRID: request.ExternalID, Login: cfg.ArcanumReviewLogin, Conditions: conditions, IntervalSeconds: max(10, int(cfg.ArcanumReviewPollInterval.Seconds()))})
					entry = &model.LegacyTaskImport{SourceKey: key, EventKey: key, Title: request.Title, SourceURL: request.SourceURL, Polling: &copy}
					groups[key] = entry
				}
				entry.RequestIDs = append(entry.RequestIDs, request.ID)
			}
			keys := make([]string, 0, len(groups))
			for key := range groups {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				in.Legacy = append(in.Legacy, *groups[key])
			}
			if _, err := sink.BootstrapTaskSource(ctx, in); err != nil {
				return fmt.Errorf("import %s: %w", source, err)
			}
		}
	}
	done, err := sink.TaskBoardMigrationDone(ctx, "cron")
	if err != nil {
		return err
	}
	if done {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(cfg.Paths.Home, "cron.json"))
	if os.IsNotExist(err) {
		return sink.CompleteTaskBoardMigration(ctx, "cron")
	}
	if err != nil {
		return err
	}
	file, err := cronpoller.ParseConfig(data, cfg.ExternalRequestsTopicID)
	if err != nil {
		return err
	}
	for _, task := range file.Tasks {
		if task.Enabled && cfg.ExternalRequestsTopicID == 0 {
			return fmt.Errorf("CTR_GO_EXTERNAL_REQUESTS_TOPIC_ID is required for board schedule %q", task.ID)
		}
		column := model.TaskPollingColumn
		if !task.Enabled {
			column = model.TaskTODO
		}
		execution := model.TaskExecutionSettings{CWD: task.CWD, Model: task.Model, ReasoningEffort: task.ReasoningEffort, ApprovalPolicy: "on-request", ApprovalsReviewer: "auto_review", SandboxMode: "workspace-write"}
		in := model.TaskSourceImport{Key: "cron:" + task.ID, Title: "Schedule: " + task.ID, Column: column, Polling: model.TaskPolling{Type: polling.TypeCron, Mode: model.PollingRepeat, Config: boardJSON(polling.Config{Cron: task.Cron, Timezone: file.Timezone, MaxLateness: task.MaxLateness, LegacyID: task.ID, IntervalSeconds: 30})}, Definition: model.TaskDefinition{Title: "Scheduled task: " + task.ID, Config: model.TaskConfig{InitialPrompt: task.Prompt, ContinuationPrompt: task.Prompt, Execution: execution}}}
		for _, request := range legacy {
			if request.Source != cronpoller.Source || request.TaskID != "" || !strings.HasPrefix(request.ExternalID, task.ID+":") {
				continue
			}
			key := "cron:" + request.ExternalID
			in.Legacy = append(in.Legacy, model.LegacyTaskImport{SourceKey: key, EventKey: key, RequestIDs: []string{request.ID}, Title: request.Title})
		}
		if _, err := sink.BootstrapTaskSource(ctx, in); err != nil {
			return fmt.Errorf("import schedule %s: %w", task.ID, err)
		}
	}
	return sink.CompleteTaskBoardMigration(ctx, "cron")
}
