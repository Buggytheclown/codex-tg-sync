package storage

import (
	"context"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/model"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskCardsExcludeDetailsAndKeepStatus(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "cards.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	cards, err := s.ListTaskCards(ctx)
	if err != nil || cards == nil || len(cards) != 0 {
		t.Fatalf("empty: %#v %v", cards, err)
	}
	huge := strings.Repeat("private-prompt", 100000)
	task, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "chat", ThreadID: "thread", Config: model.TaskConfig{InitialPrompt: huge, Execution: model.TaskExecutionSettings{CWD: "private-workspace"}}, Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingOnce, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingReturnTask}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.UpsertSnapshot(ctx, "thread", model.ThreadSnapshotState{LastSeenTurnStatus: "inProgress", CompactJSON: []byte(`{"private":"` + huge + `"}`)}); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO external_launch_requests(id,source,external_id,sender,title,prompt,status,telegram_topic_id,created_at,updated_at,task_id) VALUES('request','test','external','author','title',?,'pending_approval',1,'now','now',?)`, huge, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.CreateTask(ctx, model.CreateTaskInput{Title: "source", Polling: &model.TaskPolling{Type: "pr", Mode: model.PollingRepeat, Config: []byte(`{}`), Action: model.TaskPollingAction{Type: model.PollingCreateTask, ChildDefinition: &model.TaskDefinition{Title: "child", Config: model.TaskConfig{InitialPrompt: huge, Execution: model.TaskExecutionSettings{CWD: "private-child-workspace"}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	_ = source
	cards, err = s.ListTaskCards(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cards)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 4000 || strings.Contains(string(raw), "private") || strings.Contains(string(raw), "config") || strings.Contains(string(raw), "snapshot") {
		t.Fatalf("oversized/detail card: %d bytes", len(raw))
	}
	chatCard, err := s.GetTaskCard(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if chatCard.Execution.LastSeenTurnStatus != "inProgress" || chatCard.Polling.Problem != "" {
		t.Fatalf("status: %#v", cards[0])
	}
	full, err := s.GetTask(ctx, task.ID)
	if err != nil || full.Config.InitialPrompt != huge || len(full.Requests) != 1 || full.Requests[0].Prompt != huge || len(full.Execution.CompactJSON) < len(huge) {
		t.Fatalf("details missing: %v", err)
	}
	s.SetTaskPollingValidator(func(model.TaskPolling) error { return ErrTaskInvalid })
	card, err := s.GetTaskCard(ctx, task.ID)
	if err != nil || card.Polling.Problem == "" {
		t.Fatalf("invalid diagnostics: %#v %v", card, err)
	}
}
