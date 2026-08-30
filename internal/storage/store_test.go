package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestLegacyTelegramTablesAreNotCreatedOrDropped(t *testing.T) {
	legacyTables := []string{"thread_bindings", "observer_targets", "telegram_message_routes", "pending_approvals", "thread_panels", "chat_steer_state"}
	t.Run("fresh database", func(t *testing.T) {
		store, err := Open(filepath.Join(t.TempDir(), "fresh.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		for _, table := range legacyTables {
			var count int
			if err := store.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("fresh database created legacy table %s", table)
			}
		}
	})

	t.Run("existing database remains non-destructive", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "existing.sqlite")
		db, err := sql.Open("sqlite", path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE thread_bindings (marker TEXT); INSERT INTO thread_bindings(marker) VALUES ('keep')`); err != nil {
			t.Fatal(err)
		}
		_ = db.Close()
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer store.Close()
		var marker string
		if err := store.db.QueryRow(`SELECT marker FROM thread_bindings`).Scan(&marker); err != nil || marker != "keep" {
			t.Fatalf("existing legacy data marker=%q err=%v", marker, err)
		}
	})
}

func TestRetireUnsupportedTelegramDeliveries(t *testing.T) {
	t.Parallel()
	store, err := Open(filepath.Join(t.TempDir(), "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	tests := []struct {
		eventID string
		chatID  int64
		kind    string
		status  string
		want    string
	}{
		{"dm-pending", 42, "observer", model.DeliveryStatusPending, model.DeliveryStatusSuperseded},
		{"dm-retry", 42, "health", model.DeliveryStatusRetry, model.DeliveryStatusSuperseded},
		{"group-processing-observer", -1001, "observer", model.DeliveryStatusProcessing, model.DeliveryStatusSuperseded},
		{"group-health", -1001, "health", model.DeliveryStatusPending, model.DeliveryStatusPending},
		{"group-terminal", -1001, "external_terminal", model.DeliveryStatusRetry, model.DeliveryStatusRetry},
		{"historical-delivered", 42, "observer", model.DeliveryStatusDelivered, model.DeliveryStatusDelivered},
		{"historical-dead", 42, "observer", model.DeliveryStatusDead, model.DeliveryStatusDead},
	}
	for _, tt := range tests {
		if err := store.EnqueueDelivery(ctx, model.DeliveryQueueItem{
			EventID: tt.eventID, ChatKey: tt.eventID, ChatID: tt.chatID, Kind: tt.kind,
			Status: tt.status, PayloadJSON: `{}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.RetireUnsupportedTelegramDeliveries(ctx, -1001); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		got, err := store.DeliveryStatusForEvent(ctx, tt.eventID, tt.eventID)
		if err != nil {
			t.Fatal(err)
		}
		if got != tt.want {
			t.Errorf("%s status = %q, want %q", tt.eventID, got, tt.want)
		}
	}
}

func TestListThreadsFiltersInternalAppServerThreads(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	threads := []model.Thread{
		{
			ID:            "visible-thread",
			Title:         "Visible work",
			ProjectName:   "codex-tg",
			DirectoryName: "codex-tg",
			UpdatedAt:     10,
			LastPreview:   "normal user request",
			Raw:           json.RawMessage(`{"id":"visible-thread","preview":"normal user request"}`),
		},
		{
			ID:            "ephemeral-thread",
			Title:         "01900000-0000-7000-8000-000000000014",
			ProjectName:   "memories",
			DirectoryName: "memories",
			UpdatedAt:     30,
			Raw:           json.RawMessage(`{"thread":{"id":"ephemeral-thread","ephemeral":true,"source":{"subAgent":"memory_consolidation"}}}`),
		},
		{
			ID:            "sub-agent-thread",
			Title:         "01900000-0000-7000-8000-000000000015",
			ProjectName:   "memories",
			DirectoryName: "memories",
			UpdatedAt:     20,
			Raw:           json.RawMessage(`{"id":"sub-agent-thread","source":{"subAgent":"memory_consolidation"}}`),
		},
	}
	for _, thread := range threads {
		if err := store.UpsertThread(ctx, thread); err != nil {
			t.Fatalf("UpsertThread(%s) failed: %v", thread.ID, err)
		}
	}

	listed, err := store.ListThreads(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListThreads failed: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "visible-thread" {
		t.Fatalf("listed threads = %#v, want only visible-thread", listed)
	}

	searched, err := store.ListThreads(ctx, 10, "memories")
	if err != nil {
		t.Fatalf("ListThreads(search) failed: %v", err)
	}
	if len(searched) != 0 {
		t.Fatalf("searched internal threads = %#v, want none", searched)
	}

	grouped, err := store.ListProjectGroups(ctx)
	if err != nil {
		t.Fatalf("ListProjectGroups failed: %v", err)
	}
	if _, ok := grouped["memories"]; ok {
		t.Fatalf("project groups include internal memories project: %#v", grouped)
	}
}

func TestThreadAndSnapshotPersistenceRedactsTelegramBotCredentials(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	credential := "bot123456789:" + "example_secret-token"
	raw := json.RawMessage(fmt.Sprintf(`{"id":"thread-secret","url":"https://api.telegram.org/%s/getUpdates"}`, credential))
	thread := model.Thread{
		ID:          "thread-secret",
		Title:       "Secret fixture",
		ProjectName: "codex-tg",
		UpdatedAt:   1,
		Raw:         raw,
	}
	if err := store.UpsertThread(ctx, thread); err != nil {
		t.Fatalf("UpsertThread failed: %v", err)
	}
	loadedThread, err := store.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread failed: %v", err)
	}
	if strings.Contains(string(loadedThread.Raw), credential) || !strings.Contains(string(loadedThread.Raw), "bot<redacted>") {
		t.Fatalf("thread raw JSON was not redacted: %s", loadedThread.Raw)
	}
	var decodedThread map[string]any
	if err := json.Unmarshal(loadedThread.Raw, &decodedThread); err != nil {
		t.Fatalf("redacted thread raw JSON is invalid: %v", err)
	}

	snapshot := model.ThreadSnapshotState{
		CompactJSON: json.RawMessage(fmt.Sprintf(`{"tool_output":"request failed at https://api.telegram.org/%s/sendMessage"}`, credential)),
	}
	if err := store.UpsertSnapshot(ctx, thread.ID, snapshot); err != nil {
		t.Fatalf("UpsertSnapshot failed: %v", err)
	}
	loadedSnapshot, err := store.GetSnapshot(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetSnapshot failed: %v", err)
	}
	if strings.Contains(string(loadedSnapshot.CompactJSON), credential) || !strings.Contains(string(loadedSnapshot.CompactJSON), "bot<redacted>") {
		t.Fatalf("snapshot compact JSON was not redacted: %s", loadedSnapshot.CompactJSON)
	}
	var decodedSnapshot map[string]any
	if err := json.Unmarshal(loadedSnapshot.CompactJSON, &decodedSnapshot); err != nil {
		t.Fatalf("redacted snapshot compact JSON is invalid: %v", err)
	}
}

func TestDeliveryQueueClaimRetryAndComplete(t *testing.T) {
	t.Parallel()

	store := openTestStore(t)
	ctx := context.Background()
	item := model.DeliveryQueueItem{
		EventID:     "event-1",
		ChatKey:     model.ChatKey(123456789, 0),
		ChatID:      123456789,
		TopicID:     0,
		ThreadID:    "thread-1",
		Kind:        "observer",
		Status:      model.DeliveryStatusPending,
		AvailableAt: model.NowString(),
		PayloadJSON: `{"text":"hello"}`,
		CreatedAt:   model.NowString(),
		UpdatedAt:   model.NowString(),
	}
	if err := store.EnqueueDelivery(ctx, item); err != nil {
		t.Fatalf("EnqueueDelivery failed: %v", err)
	}

	batch, err := store.ClaimDeliveryBatch(ctx, 10)
	if err != nil {
		t.Fatalf("ClaimDeliveryBatch failed: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("ClaimDeliveryBatch len = %d, want 1", len(batch))
	}
	if batch[0].Status != model.DeliveryStatusPending {
		t.Fatalf("Claimed status = %q, want %q", batch[0].Status, model.DeliveryStatusPending)
	}

	retryAt := time.Now().UTC().Add(5 * time.Second)
	if err := store.FailDelivery(ctx, batch[0].ID, 1, retryAt, "temporary failure", false); err != nil {
		t.Fatalf("FailDelivery failed: %v", err)
	}
	if err := store.RecordDeliveryAttempt(ctx, batch[0].ID, 1, "send_error", "temporary failure"); err != nil {
		t.Fatalf("RecordDeliveryAttempt failed: %v", err)
	}

	backlog, err := store.DeliveryQueueBacklog(ctx)
	if err != nil {
		t.Fatalf("DeliveryQueueBacklog failed: %v", err)
	}
	if backlog != 1 {
		t.Fatalf("DeliveryQueueBacklog = %d, want 1", backlog)
	}
}

func TestDeliveryQueueDeadCount(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()
	for index := 0; index < 2; index++ {
		if err := store.EnqueueDelivery(ctx, model.DeliveryQueueItem{
			EventID:     fmt.Sprintf("dead-%d", index),
			ChatKey:     model.ChatKey(123456789, int64(index)),
			ChatID:      123456789,
			TopicID:     int64(index),
			Kind:        "health",
			Status:      model.DeliveryStatusPending,
			AvailableAt: model.NowString(),
			PayloadJSON: `{"text":"warning"}`,
			CreatedAt:   model.NowString(),
			UpdatedAt:   model.NowString(),
		}); err != nil {
			t.Fatalf("EnqueueDelivery[%d] failed: %v", index, err)
		}
	}
	batch, err := store.ClaimDeliveryBatch(ctx, 10)
	if err != nil || len(batch) != 2 {
		t.Fatalf("ClaimDeliveryBatch = %#v, err=%v", batch, err)
	}
	if err := store.FailDelivery(ctx, batch[0].ID, 5, time.Now().UTC(), "failed", true); err != nil {
		t.Fatalf("FailDelivery(dead) failed: %v", err)
	}
	if err := store.CompleteDelivery(ctx, batch[1].ID); err != nil {
		t.Fatalf("CompleteDelivery failed: %v", err)
	}

	count, err := store.DeliveryQueueDeadCount(ctx)
	if err != nil || count != 1 {
		t.Fatalf("DeliveryQueueDeadCount = %d, err=%v, want 1", count, err)
	}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s) failed: %v", path, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
