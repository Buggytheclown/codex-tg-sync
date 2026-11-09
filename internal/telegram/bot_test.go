package telegram

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/daemon"
	"github.com/mideco-tech/codex-tg/internal/model"
	"github.com/mideco-tech/codex-tg/internal/storage"
)

func TestBotEditMessageRejectsMultiChunkPayload(t *testing.T) {
	t.Parallel()

	bot := &Bot{client: NewClient("token")}
	err := bot.EditMessage(context.Background(), 42, 0, 77, strings.Repeat("x", telegramMessageLimit+10), nil, model.SendOptions{})
	if err == nil {
		t.Fatal("EditMessage must reject multi-chunk payloads")
	}
}

func TestBotUnchangedMessageEditIsDelivered(t *testing.T) {
	for _, rendered := range []bool{false, true} {
		for _, unchanged := range []bool{false, true} {
			t.Run(fmt.Sprintf("rendered=%t/unchanged=%t", rendered, unchanged), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					description := "Bad Request: message to edit not found"
					if unchanged {
						description = "Bad Request: message is not modified: specified new message content and reply markup are exactly the same"
					}
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": description})
				}))
				defer server.Close()
				client := NewClient("token")
				client.baseURL = server.URL
				bot := &Bot{client: client}
				var err error
				if rendered {
					err = bot.EditRenderedMessage(context.Background(), 42, 0, 77, model.RenderedMessage{Text: "existing card"}, nil)
				} else {
					err = bot.EditMessage(context.Background(), 42, 0, 77, "existing card", nil, model.SendOptions{})
				}
				if (err == nil) != unchanged {
					t.Fatalf("edit error=%v, unchanged=%t", err, unchanged)
				}
				if unchanged && calls != 1 {
					t.Fatalf("unchanged edit made %d API calls", calls)
				}
			})
		}
	}
}

func TestSanitizeTelegramLogErrorRedactsBotTokenURL(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf(`Post "https://api.telegram.org/bot123456789:AAF_secret-token/getUpdates": context deadline exceeded`)
	got := sanitizeTelegramLogError(err)
	if strings.Contains(got, "123456789:AAF_secret-token") {
		t.Fatalf("sanitizeTelegramLogError leaked token: %q", got)
	}
	if !strings.Contains(got, "bot<redacted>") {
		t.Fatalf("sanitizeTelegramLogError = %q, want redacted marker", got)
	}
}

func TestBotLogsEgressQueueWaitByBoundedOperation(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	bot := &Bot{logger: log.New(&output, "", 0)}
	bot.logEgressObservation(egressObservation{
		Operation: egressOperationDeleteForumTopic, Priority: "background",
		QueueWait: 3 * time.Second, APIDuration: 25 * time.Millisecond, Outcome: "success",
	})
	got := output.String()
	for _, want := range []string{
		"telegram_egress", "operation=delete_forum_topic", "priority=background",
		"queue_wait_ms=3000", "api_duration_ms=25", "outcome=success",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("egress diagnostic %q does not contain %q", got, want)
		}
	}
}

func TestDefaultCommandsExposeSyncAndOperatorStatusCommands(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for _, command := range defaultCommands() {
		if seen[command.Command] {
			t.Fatalf("defaultCommands contains duplicate command %q", command.Command)
		}
		seen[command.Command] = true
	}
	for _, command := range []string{"sync", "status", "pollers", "requests", "refresh", "projects", "newchat", "stop"} {
		if !seen[command] {
			t.Fatalf("defaultCommands must expose /%s in the Telegram command menu", command)
		}
	}
	if len(seen) != 8 {
		t.Fatalf("defaultCommands = %#v, want only the public Sync and operator commands", defaultCommands())
	}
}

func TestBotStartScopesCommandsToConfiguredSyncGroup(t *testing.T) {
	t.Parallel()

	var paths []string
	var commandPayload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":7,"is_bot":true,"first_name":"bot","username":"codex_bot"}}`))
		case "/deleteMyCommands":
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		case "/setMyCommands":
			if err := json.NewDecoder(r.Body).Decode(&commandPayload); err != nil {
				t.Fatalf("decode setMyCommands: %v", err)
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			t.Fatalf("unexpected Telegram API path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{
		cfg:    config.Config{SyncGroupID: -10042},
		client: client,
		logger: log.New(io.Discard, "", 0),
	}
	if err := bot.Start(context.Background()); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if got, want := strings.Join(paths, ","), "/getMe,/deleteMyCommands,/setMyCommands"; got != want {
		t.Fatalf("Telegram API calls = %q, want %q", got, want)
	}
	scope, _ := commandPayload["scope"].(map[string]any)
	if scope["type"] != "chat" || scope["chat_id"] != float64(-10042) {
		t.Fatalf("setMyCommands scope = %#v, want exact Sync chat", scope)
	}
	commands, _ := commandPayload["commands"].([]any)
	if len(commands) != 8 {
		t.Fatalf("setMyCommands commands = %#v, want 8", commands)
	}
}

func TestBotSendMessageChunksAndReturnsLastMessageID(t *testing.T) {
	t.Parallel()

	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":42,"type":"private"}}}`, 100+calls)
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client}

	messageID, err := bot.SendMessage(context.Background(), 42, 0, strings.Repeat("line\n", telegramMessageLimit/4), nil, model.SendOptions{})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if calls < 2 {
		t.Fatalf("calls = %d, want at least 2 chunked requests", calls)
	}
	if got, want := messageID, int64(100+calls); got != want {
		t.Fatalf("messageID = %d, want %d", got, want)
	}
}

func TestBotSendRenderedMessagesFallsBackToPlainEntities(t *testing.T) {
	t.Parallel()

	var calls int
	var second map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if calls == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: entities are invalid"}`))
			return
		}
		if err := json.Unmarshal(body, &second); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":202,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client}
	ids, err := bot.SendRenderedMessages(context.Background(), 42, 0, []model.RenderedMessage{{
		Text:     "formatted",
		Entities: []model.MessageEntity{{Type: "code", Offset: 0, Length: 9}},
	}}, nil, model.SendOptions{})
	if err != nil {
		t.Fatalf("SendRenderedMessages failed: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
	if len(ids) != 1 || ids[0] != 202 {
		t.Fatalf("ids = %#v, want [202]", ids)
	}
	if _, ok := second["entities"]; ok {
		t.Fatalf("fallback entities = %#v, want omitted", second["entities"])
	}
	if _, ok := second["parse_mode"]; ok {
		t.Fatalf("fallback parse_mode = %#v, want omitted", second["parse_mode"])
	}
}

func TestBotSyncMessagePreservesRenderedEntitiesOnSendAndEdit(t *testing.T) {
	t.Parallel()

	payloads := make([]map[string]any, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		payload["path"] = r.URL.Path
		payloads = append(payloads, payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":81,"chat":{"id":-1001,"type":"supergroup"},"text":"status"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{cfg: config.Config{SyncGroupID: -1001}, client: client}
	rendered := model.RenderedMessage{
		Text:     "status\nbody",
		Entities: []model.MessageEntity{{Type: "expandable_blockquote", Offset: 7, Length: 4}},
	}
	messageID, err := bot.SendSyncMessage(context.Background(), 11, rendered, model.SendOptions{Silent: true})
	if err != nil || messageID != 81 {
		t.Fatalf("SendSyncMessage id=%d err=%v", messageID, err)
	}
	if err := bot.EditSyncMessage(context.Background(), 11, messageID, rendered, model.SendOptions{}); err != nil {
		t.Fatalf("EditSyncMessage failed: %v", err)
	}

	if len(payloads) != 2 || payloads[0]["path"] != "/sendMessage" || payloads[1]["path"] != "/editMessageText" {
		t.Fatalf("payloads=%#v", payloads)
	}
	if payloads[0]["message_thread_id"] != float64(11) || payloads[0]["disable_notification"] != true {
		t.Fatalf("send payload=%#v", payloads[0])
	}
	for index, payload := range payloads {
		if _, exists := payload["parse_mode"]; exists {
			t.Fatalf("payload[%d] unexpectedly used parse_mode: %#v", index, payload)
		}
		entities, ok := payload["entities"].([]any)
		if !ok || len(entities) != 1 || entities[0].(map[string]any)["type"] != "expandable_blockquote" {
			t.Fatalf("payload[%d] entities=%#v", index, payload["entities"])
		}
	}
}

func TestCreateLaunchTopicReturnsRetryable429WithoutInternalReplay(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	governor := newEgressGovernor(0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	governor.Start(ctx)
	bot := &Bot{cfg: config.Config{SyncGroupID: -1001}, client: client, egress: governor}

	_, err := bot.CreateLaunchTopic(ctx, "Launch request")
	var failure *daemon.SyncForumFailure
	if !errors.As(err, &failure) || failure.Kind != daemon.SyncForumFailureRetryable {
		t.Fatalf("CreateLaunchTopic error = %#v, want retryable forum failure", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("createForumTopic attempts = %d, want one launch-owned attempt", attempts.Load())
	}
}

func TestBotGovernorPacesEveryRawTextChunk(t *testing.T) {
	requests := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":81,"chat":{"id":-1001,"type":"supergroup"},"text":"ok"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(defaultTelegramGroupWriteInterval, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	governor.Start(ctx)
	bot := &Bot{client: client, egress: governor}
	done := make(chan error, 1)
	go func() {
		_, err := bot.SendMessage(ctx, -1001, 77, strings.Repeat("x", telegramMessageLimit+1), nil, model.SendOptions{})
		done <- err
	}()

	receiveSignal(t, requests)
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })
	assertNoSignal(t, requests)
	clock.Advance(defaultTelegramGroupWriteInterval)
	receiveSignal(t, requests)
	if err := receiveError(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestBotPreAcknowledgesWholeCallbackBatchDuringGroupCooldown(t *testing.T) {
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	root := t.TempDir()
	service, err := daemon.New(config.Config{
		SyncGroupID:    -1001,
		AllowedUserIDs: []int64{123},
		Paths: config.Paths{
			Home: root, DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs"), DBPath: filepath.Join(root, "data", "state.sqlite"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(defaultTelegramGroupWriteInterval, clock)
	governor.noteCooldown(clock.Now().Add(time.Hour))
	bot := &Bot{client: client, egress: governor, logger: log.New(io.Discard, "", 0), service: service}
	allowedMessage := &Message{Chat: Chat{ID: -1001}}
	bot.acknowledgeCallbacks(context.Background(), []Update{
		{CallbackQuery: &CallbackQuery{ID: "callback-1", From: &User{ID: 123}, Message: allowedMessage}},
		{Message: &Message{MessageID: 1}},
		{CallbackQuery: &CallbackQuery{ID: "callback-2", From: &User{ID: 123}, Message: allowedMessage}},
		{CallbackQuery: &CallbackQuery{ID: "wrong-user", From: &User{ID: 999}, Message: allowedMessage}},
		{CallbackQuery: &CallbackQuery{ID: "wrong-group", From: &User{ID: 123}, Message: &Message{Chat: Chat{ID: -1002}}}},
	})

	for index := 0; index < 2; index++ {
		select {
		case path := <-requests:
			if path != "/answerCallbackQuery" {
				t.Fatalf("callback request path=%q", path)
			}
		case <-time.After(time.Second):
			t.Fatal("callback acknowledgement waited for group cooldown")
		}
	}
	select {
	case path := <-requests:
		t.Fatalf("unauthorized callback was acknowledged via %q", path)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestBotSendDocumentReturnsTelegramMessageID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":555,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client}

	dir := t.TempDir()
	path := filepath.Join(dir, "trace.log")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFile(trace.log) failed: %v", err)
	}

	messageID, err := bot.SendDocument(context.Background(), 42, 0, "trace.log", path, "trace", model.SendOptions{})
	if err != nil {
		t.Fatalf("SendDocument failed: %v", err)
	}
	if got, want := messageID, int64(555); got != want {
		t.Fatalf("messageID = %d, want %d", got, want)
	}
}

func TestBotDeliverDirectResponseSendsSilentMessage(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":777,"chat":{"id":42,"type":"private"},"text":"menu"}}`))
	}))
	defer server.Close()

	root := t.TempDir()
	service, err := daemon.New(config.Config{Paths: config.Paths{
		Home:    root,
		DataDir: filepath.Join(root, "data"),
		LogDir:  filepath.Join(root, "logs"),
		DBPath:  filepath.Join(root, "data", "state.sqlite"),
	}})
	if err != nil {
		t.Fatalf("daemon.New failed: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })

	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client, service: service}
	if err := bot.deliverDirectResponse(context.Background(), 42, 0, &daemon.DirectResponse{Text: "menu"}); err != nil {
		t.Fatalf("deliverDirectResponse failed: %v", err)
	}
	if got, ok := captured["disable_notification"].(bool); !ok || !got {
		t.Fatalf("disable_notification = %#v, want true", captured["disable_notification"])
	}
}

func TestTelegramInboundTextUsesCaptionAndDetectsUnsupportedMedia(t *testing.T) {
	message := Message{Text: " ", Caption: " screenshot context ", Photo: []json.RawMessage{json.RawMessage(`{"file_id":"photo"}`)}}
	if got := telegramInboundText(message); got != "screenshot context" {
		t.Fatalf("telegramInboundText=%q", got)
	}
	if !telegramMessageHasUnsupportedMedia(message) {
		t.Fatal("photo was not detected as media")
	}
}

func TestBotIgnoresUnsupportedMediaOutsideSyncGroup(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":777,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()
	root := t.TempDir()
	service, err := daemon.New(config.Config{AllowedUserIDs: []int64{7}, SyncGroupID: -1001, Paths: config.Paths{
		Home: root, DataDir: filepath.Join(root, "data"), LogDir: filepath.Join(root, "logs"), DBPath: filepath.Join(root, "data", "state.sqlite"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client, service: service}
	if err := bot.handleMessage(context.Background(), Message{
		MessageID: 5, From: &User{ID: 7}, Chat: Chat{ID: 42, Type: "private"}, Document: json.RawMessage(`{"file_id":"doc"}`),
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("Bot API calls = %d, want silent ignore", calls)
	}
}

func TestBotDoesNotAcknowledgeUnsavedTopicEvent(t *testing.T) {
	root := t.TempDir()
	service, err := daemon.New(config.Config{SyncGroupID: -1001, Paths: config.Paths{Home: root, DataDir: root, LogDir: root, DBPath: filepath.Join(root, "db")}})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	db, err := sql.Open("sqlite", filepath.Join(root, "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TRIGGER fail_topic_event BEFORE INSERT ON sync_topic_created_events BEGIN SELECT RAISE(ABORT,'injected event persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var offsets []int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request getUpdatesRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		offsets = append(offsets, request.Offset)
		if len(offsets) == 2 {
			cancel()
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":10,"message":{"message_id":20,"message_thread_id":20,"chat":{"id":-1001},"from":{"id":9},"forum_topic_created":{"name":"topic"}}}]}`)
	}))
	defer server.Close()
	client := NewClient("test-token")
	client.baseURL = server.URL
	bot := &Bot{cfg: config.Config{SyncGroupID: -1001}, client: client, service: service, me: &User{ID: 9}, logger: log.New(io.Discard, "", 0)}
	if err = bot.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(offsets) < 2 || offsets[0] != 0 || offsets[1] != 0 {
		t.Fatalf("acknowledged unsaved event: %v", offsets)
	}
}

func TestBotOffIsHandledWhileTopicCreationWaits(t *testing.T) {
	root := t.TempDir()
	cfg := config.Config{SyncGroupID: -1001, AllowedUserIDs: []int64{7}, DefaultCWD: root, Paths: config.Paths{Home: root, DataDir: root, LogDir: root, DBPath: filepath.Join(root, "db")}}
	service, err := daemon.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	store, err := storage.Open(cfg.Paths.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err = store.BeginSyncActivation(ctx, "s", -1001); err != nil {
		t.Fatal(err)
	}
	if err = store.FinishSyncActivation(ctx, "s", "{}", true); err != nil {
		t.Fatal(err)
	}
	menu, err := service.HandleMessageWithID(ctx, -1001, 1, 10, 7, "/newchat", 0)
	if err != nil || menu == nil || len(menu.Buttons) == 0 {
		t.Fatal(menu, err)
	}
	token := menu.Buttons[0][0].CallbackData
	entered := make(chan struct{})
	release := make(chan struct{})
	offSent := make(chan struct{})
	allSent := make(chan struct{})
	var polls, sends atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/getUpdates":
			switch polls.Add(1) {
			case 1:
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": []any{map[string]any{"update_id": 1, "callback_query": map[string]any{"id": "cb", "from": map[string]any{"id": 7}, "data": token, "message": map[string]any{"message_id": 50, "message_thread_id": 1, "chat": map[string]any{"id": -1001}}}}}})
			case 2:
				select {
				case <-entered:
				case <-ctx.Done():
					return
				}
				_, _ = io.WriteString(w, `{"ok":true,"result":[{"update_id":2,"message":{"message_id":51,"message_thread_id":1,"chat":{"id":-1001},"from":{"id":7},"text":"/sync off"}}]}`)
			default:
				<-ctx.Done()
			}
		case "/answerCallbackQuery":
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		case "/createForumTopic":
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return
			}
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_thread_id":20,"name":"New task"}}`)
		case "/sendMessage":
			n := sends.Add(1)
			_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":100}}`)
			if n == 1 {
				close(offSent)
			}
			if n == 2 {
				close(allSent)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
		}
	}))
	defer server.Close()
	client := NewClient("test-token")
	client.baseURL = server.URL
	bot := &Bot{cfg: cfg, client: client, service: service, me: &User{ID: 9}, logger: log.New(io.Discard, "", 0), egress: newEgressGovernor(time.Nanosecond)}
	service.SetSyncForum(bot)
	bot.egress.Start(ctx)
	done := make(chan error, 1)
	go func() { done <- bot.Run(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("create was not attempted")
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	offObserved := false
	for !offObserved {
		state, err := store.GetSyncState(ctx)
		if err != nil {
			t.Fatal(err)
		}
		offObserved = state.State == model.SyncStateOff
		if !offObserved {
			select {
			case <-ctx.Done():
				t.Fatal("off waited for create response")
			case <-ticker.C:
			}
		}
	}
	close(release)
	select {
	case <-allSent:
	case <-ctx.Done():
		t.Fatal("creation did not complete")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	targets, err := store.ListAllSyncCleanupTargets(context.Background(), -1001)
	if err != nil || len(targets) != 1 || targets[0].TopicID != 20 {
		t.Fatal("late topic was not cleanup-only", targets, err)
	}
}

func TestHistoricalCardEditsLeaveForegroundCapacity(t *testing.T) {
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(defaultTelegramGroupWriteInterval, clock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	governor.Start(ctx)
	observed := make(chan int64, 32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			MessageID int64 `json:"message_id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.MessageID == 0 {
			body.MessageID = 999
		}
		observed <- body.MessageID
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, body.MessageID)
	}))
	defer server.Close()
	client := NewClient("token")
	client.baseURL = server.URL
	bot := &Bot{client: client, egress: governor}
	for i := int64(1); i <= defaultTelegramBackgroundWriteLimit; i++ {
		done := make(chan error, 1)
		go func(id int64) {
			done <- bot.EditMessage(ctx, 42, 0, id, "historical card", nil, model.SendOptions{Background: true})
		}(i)
		if i > 1 {
			waitForCondition(t, func() bool { return clock.timerCount() > 0 })
			clock.Advance(defaultTelegramGroupWriteInterval)
		}
		select {
		case id := <-observed:
			if id != i {
				t.Fatal(id, i)
			}
		case <-time.After(time.Second):
			t.Fatal("history edit not sent")
		}
		if err := receiveError(t, done); err != nil {
			t.Fatal(err)
		}
	}
	parked := make(chan error, 1)
	go func() {
		parked <- bot.EditMessage(ctx, 42, 0, 13, "historical card", nil, model.SendOptions{Background: true})
	}()
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })
	manual := make(chan error, 1)
	go func() {
		_, err := bot.SendMessage(ctx, 42, 0, "manual response", nil, model.SendOptions{})
		manual <- err
	}()
	clock.Advance(defaultTelegramGroupWriteInterval)
	select {
	case id := <-observed:
		if id != 999 {
			t.Fatalf("historical edit consumed manual capacity: id=%d", id)
		}
	case <-time.After(time.Second):
		t.Fatal("manual response waited for background window")
	}
	if err := receiveError(t, manual); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = receiveError(t, parked)
}
