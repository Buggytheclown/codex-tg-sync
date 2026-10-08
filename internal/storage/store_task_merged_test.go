package storage

import (
	"context"
	"github.com/mideco-tech/codex-tg/internal/model"
	"testing"
)

func TestMergedReviewTaskMovesDoneWithoutLaunch(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "review", SourceKey: "arcanum_review:42", Column: model.TaskPollingColumn, ThreadID: "thread", Config: model.TaskConfig{ContinuationPrompt: "continue"}, Polling: &model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{"pr_id":"42"}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}})
	if err != nil {
		t.Fatal(err)
	}
	out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, PRStatus: "merged"}
	accepted, err := s.AcceptTaskPollingOutcome(ctx, out, 0)
	if err != nil || !accepted {
		t.Fatalf("merge: %v %v", accepted, err)
	}
	got, _ := s.GetTask(ctx, task.ID)
	if got.Column != model.TaskDone || got.Version != task.Version+1 || got.Polling.Generation != task.Polling.Generation+1 || len(got.Requests) != 0 {
		t.Fatalf("task: %#v", got)
	}
	events, _ := s.ListTaskEvents(ctx, task.ID)
	if events[len(events)-1].Kind != "pr_merged" {
		t.Fatal(events)
	}
	if accepted, err := s.AcceptTaskPollingOutcome(ctx, out, 0); err != nil || accepted {
		t.Fatalf("repeat: %v %v", accepted, err)
	}
}

func TestMergedReviewScopeFencesAndRequestCancellation(t *testing.T) {
	for _, kind := range []string{"review", "parent", "manual", "manual_enabled", "feedback", "review_disabled", "cron", "closed", "stale", "disabled", "error"} {
		t.Run(kind, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			sourceKey := ""
			if kind == "review" || kind == "review_disabled" || kind == "closed" || kind == "stale" || kind == "disabled" || kind == "error" {
				sourceKey = "arcanum_review:42"
			}
			if kind == "feedback" {
				sourceKey = "arcanum_changes:42"
			}
			column := model.TaskPollingColumn
			if kind == "disabled" {
				column = model.TaskTODO
			}
			pollType := "pr_wait"
			if kind == "cron" {
				pollType = "cron"
			}
			raw := []byte(`{}`)
			if kind == "review_disabled" {
				raw = []byte(`{"complete_on_merge":false}`)
			}
			if kind == "manual_enabled" {
				raw = []byte(`{"complete_on_merge":true}`)
			}
			task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "task", SourceKey: sourceKey, Column: column, Polling: &model.TaskPolling{Type: pollType, Mode: model.PollingOnce, Config: raw, Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "parent" {
				parent, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", SourceKey: "source:arcanum_review", Polling: &model.TaskPolling{Type: "pr_review_source", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: "work"}}}}})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := s.db.Exec(`UPDATE tasks SET source_task_id=? WHERE id=?`, parent.ID, task.ID); err != nil {
					t.Fatal(err)
				}
			}
			statuses := []string{model.ExternalLaunchPendingApproval, model.ExternalLaunchStarting, model.ExternalLaunchFailed, model.ExternalLaunchSessionStarted, model.ExternalLaunchOutcomeUnknown}
			for _, status := range statuses {
				request := testExternalLaunchRequest("test:" + status)
				request.TaskID, request.TriggerEventID = task.ID, status
				policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
				request.ApprovalPolicy, request.ApprovalsReviewer, request.SandboxMode = &policy, &reviewer, &sandbox
				if _, err := s.EnqueueExternalLaunchRequests(ctx, "test", []model.ExternalLaunchRequest{request}); err != nil {
					t.Fatal(err)
				}
				thread := ""
				if status == model.ExternalLaunchSessionStarted {
					thread = "thread"
				}
				if _, err := s.db.Exec(`UPDATE external_launch_requests SET status=?,thread_id=? WHERE id=?`, status, thread, request.ID); err != nil {
					t.Fatal(err)
				}
			}
			out := model.TaskPollingOutcome{TaskID: task.ID, Generation: task.Polling.Generation, PRStatus: "merged"}
			if kind == "closed" {
				out.PRStatus = "closed"
			}
			if kind == "stale" {
				out.Generation++
			}
			if kind == "error" {
				out.Error = "incomplete observation"
			}
			if _, err := s.AcceptTaskPollingOutcome(ctx, out, 0); err != nil {
				t.Fatal(err)
			}
			got, _ := s.GetTask(ctx, task.ID)
			done := kind == "review" || kind == "parent" || kind == "feedback" || kind == "manual_enabled"
			if (got.Column == model.TaskDone) != done {
				t.Fatalf("scope task: %#v", got)
			}
			for _, status := range statuses {
				request, _ := s.GetExternalLaunchRequest(ctx, "test:"+status)
				want := status
				if done && (status == model.ExternalLaunchPendingApproval || status == model.ExternalLaunchStarting || status == model.ExternalLaunchFailed) {
					want = model.ExternalLaunchDismissed
				}
				if request.Status != want {
					t.Fatalf("%s -> %s want %s", status, request.Status, want)
				}
			}
		})
	}
}
