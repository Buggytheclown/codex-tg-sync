package storage

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestSourceLaunchPolicyTrimsSourceNameUsesActualAuthorAndContinuationStaysManual(t *testing.T) {
	for _, tc := range []struct {
		mode, author string
		auto         bool
	}{{"manual", "alice", false}, {"trusted_authors", "alice", true}, {"trusted_authors", "ALICE", true}, {"trusted_authors", "@Alice", true}, {"trusted_authors", "bob", false}, {"trusted_authors", "", false}, {"auto", "bob", true}} {
		t.Run(tc.mode+tc.author, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			def := model.TaskDefinition{Title: "Custom {{pr_title}}", Config: model.TaskConfig{InitialPrompt: "Review {{pr_url}}", ContinuationPrompt: "Continue {{reasons}}"}}
			p := model.TaskPolling{Type: "pr_review_source", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &def, InitialLaunch: &model.TaskLaunchPolicy{Mode: tc.mode, Authors: []string{"alice"}}}}
			source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: " PR review ", Column: model.TaskPollingColumn, Polling: &p})
			if err != nil {
				t.Fatal(err)
			}
			out := model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Events: []model.PollingEvent{{Key: "pr:42", SourceKey: "pr:42", Variables: map[string]string{"author": tc.author, "pr_title": "Fix", "pr_url": "url"}, Payload: []byte(`{"author":"` + tc.author + `"}`)}}}
			if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
				t.Fatal(err)
			}
			items, _ := s.ListTasks(ctx)
			var child model.Task
			for _, item := range items {
				if item.ID != source.ID {
					child = item
				}
			}
			req, _ := s.GetExternalLaunchRequest(ctx, child.Requests[0].ID)
			if req.AutoStart != tc.auto || req.Source != "PR review" || child.Title != "PR review: Custom Fix" || req.Prompt != "Review url" || child.SourceAuthor != tc.author {
				t.Fatalf("child=%#v request=%#v", child, req)
			}
			if tc.author != "" && req.Sender != tc.author {
				t.Fatalf("sender=%q", req.Sender)
			}
			if tc.auto {
				auto, _ := s.ListExternalLaunchRequestsForAutoStart(ctx, 100)
				if len(auto) != 0 {
					t.Fatal("auto start available before visible card")
				}
				if err := s.MarkExternalLaunchRequestTelegramSent(ctx, req.ID, 501, model.ExternalLaunchPendingApproval); err != nil {
					t.Fatal(err)
				}
			}
			if ok, err := s.ClaimExternalLaunchRequest(ctx, req.ID); err != nil || !ok {
				t.Fatalf("claim %v %v", ok, err)
			}
			if _, err := s.CompleteExternalLaunchRequest(ctx, req.ID, model.ExternalLaunchSessionStarted, "thread", "turn", "", ""); err != nil {
				t.Fatal(err)
			}
			childPtr, _p := s.GetTask(ctx, child.ID)
			if _p != nil {
				t.Fatal(_p)
			}
			childPtr, err = s.SetTaskPolling(ctx, child.ID, model.SetTaskPollingInput{ExpectedVersion: childPtr.Version, Polling: model.TaskPolling{Type: "pr_wait", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingResumeTask}}})
			if err != nil {
				t.Fatal(err)
			}
			childPtr, err = s.MoveTask(ctx, child.ID, model.MoveTaskInput{ExpectedVersion: childPtr.Version, Column: model.TaskPollingColumn})
			if err != nil {
				t.Fatal(err)
			}
			out = model.TaskPollingOutcome{TaskID: child.ID, Generation: childPtr.Polling.Generation, Events: []model.PollingEvent{{Key: "wait:1", Payload: []byte(`{}`), Variables: map[string]string{"reasons": "new commit"}}}}
			if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
				t.Fatal(err)
			}
			requests, _ := s.ListExternalLaunchRequestsByStatus(ctx, []string{model.ExternalLaunchPendingApproval}, 100)
			if len(requests) != 1 || requests[0].TargetThreadID != "thread" {
				t.Fatalf("missing continuation: %#v", requests)
			}
			for _, r := range requests {
				if r.TargetThreadID == "thread" && (r.AutoStart || r.Prompt != "Continue new commit" || (tc.author != "" && r.Sender != tc.author) || r.Source != "PR review") {
					t.Fatalf("continuation=%#v", r)
				}
			}
		})
	}
}

func TestCronChildTitleIncludesScheduledLocalTimeBeforeCustomTitle(t *testing.T) {
	for _, title := range []string{"Custom work", "Scheduled task: daily"} {
		source := model.Task{ID: "source", Title: " Schedule: daily ", Polling: &model.TaskPolling{Type: "cron", Config: []byte(`{"timezone":"Europe/Minsk","legacy_id":"daily"}`)}}
		event := model.PollingEvent{Payload: json.RawMessage(`{"scheduled_at":"2026-10-12T06:00:00Z"}`)}
		got, err := sourceChildTitle(source, title, event)
		if err != nil {
			t.Fatal(err)
		}
		want := "Schedule: daily: 2026-10-12 09:00 [Europe/Minsk]"
		if title == "Custom work" {
			want += ": Custom work"
		}
		if got != want {
			t.Fatalf("title=%q want=%q", got, want)
		}
	}
}

func TestImportedReviewDefaultsUpgradeOncePreservesCustomPolicyAndFrozenRequests(t *testing.T) {
	for _, custom := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "custom"}[custom], func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			in := model.TaskSourceImport{Key: "arcanum_review", Title: "Assigned PRs", Polling: model.TaskPolling{Type: "pr_review_source", Mode: model.PollingRepeat, Config: []byte(`{}`)}, Definition: model.TaskDefinition{Title: "Child", Config: model.TaskConfig{InitialPrompt: "configured"}}}
			source, err := s.BootstrapTaskSource(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM daemon_state WHERE key='task_board.source_launch_v1.arcanum_review'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET title='Assigned PRs' WHERE id=?`, source.ID); err != nil {
				t.Fatal(err)
			}
			if custom {
				source, err = s.UpdateTask(ctx, source.ID, model.UpdateTaskInput{ExpectedVersion: source.Version, Title: "My reviews", Config: source.Config})
				if err != nil {
					t.Fatal(err)
				}
				source.Polling.Action.InitialLaunch = &model.TaskLaunchPolicy{Mode: "manual"}
				source, err = s.SetTaskPolling(ctx, source.ID, model.SetTaskPollingInput{ExpectedVersion: source.Version, Polling: *source.Polling})
				if err != nil {
					t.Fatal(err)
				}
			}
			out := model.TaskPollingOutcome{TaskID: source.ID, Generation: source.Polling.Generation, Events: []model.PollingEvent{{Key: "old", SourceKey: "old", Payload: []byte(`{}`)}}}
			if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
				t.Fatal(err)
			}
			requests, _ := s.ListExternalLaunchRequestsByStatus(ctx, nil, 100)
			before, _ := json.Marshal(requests)
			in.Title = "PR review"
			in.Polling.Action.InitialLaunch = &model.TaskLaunchPolicy{Mode: "trusted_authors", Authors: []string{"alice"}}
			upgraded, err := s.BootstrapTaskSource(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if custom {
				if upgraded.Title != "My reviews" || upgraded.Polling.Action.InitialLaunch.Mode != "manual" {
					t.Fatalf("custom settings overwritten: %#v", upgraded)
				}
			} else if upgraded.Title != "PR review" || upgraded.Polling.Action.InitialLaunch.Mode != "trusted_authors" {
				t.Fatalf("legacy settings not upgraded: %#v", upgraded)
			}
			requests, _ = s.ListExternalLaunchRequestsByStatus(ctx, nil, 100)
			after, _ := json.Marshal(requests)
			if string(before) != string(after) {
				t.Fatal("frozen requests changed")
			}
			upgraded.Polling.Action.InitialLaunch = &model.TaskLaunchPolicy{Mode: "manual"}
			upgraded, err = s.SetTaskPolling(ctx, upgraded.ID, model.SetTaskPollingInput{ExpectedVersion: upgraded.Version, Polling: *upgraded.Polling})
			if err != nil {
				t.Fatal(err)
			}
			again, err := s.BootstrapTaskSource(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if again.Polling.Action.InitialLaunch.Mode != "manual" || again.Version != upgraded.Version {
				t.Fatal("restart overwrote source edits")
			}
			// A user can turn an imported source into an ordinary task. Upgrade is safe.
			if _, err := s.db.ExecContext(ctx, `DELETE FROM task_polling WHERE task_id=?`, source.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.ExecContext(ctx, `DELETE FROM daemon_state WHERE key='task_board.source_launch_v1.arcanum_review'`); err != nil {
				t.Fatal(err)
			}
			if _, err := s.BootstrapTaskSource(ctx, in); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBoardAutoContinuationRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	_, out := makeTaskBoardSource(t, s, "")
	if _, err := s.AcceptTaskPollingOutcome(ctx, out, 123); err != nil {
		t.Fatal(err)
	}
	requests, _ := s.ListExternalLaunchRequestsByStatus(ctx, nil, 100)
	request := requests[0]
	request.ID = "invalid"
	request.ExternalID = "invalid"
	request.TriggerEventID = "invalid"
	request.AutoStart = true
	request.TargetThreadID = "thread"
	if _, err := s.EnqueueExternalLaunchRequests(ctx, request.Source, []model.ExternalLaunchRequest{request}); err == nil {
		t.Fatal("automatic continuation accepted")
	}
}
