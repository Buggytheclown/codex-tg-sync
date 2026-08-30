package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/daemon"
	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestBotEditMessageRejectsMultiChunkPayload(t *testing.T) {
	t.Parallel()

	bot := &Bot{client: NewClient("token")}
	err := bot.EditMessage(context.Background(), 42, 0, 77, strings.Repeat("x", telegramMessageLimit+10), nil)
	if err == nil {
		t.Fatal("EditMessage must reject multi-chunk payloads")
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

func TestBotStartScopesCommandsToConfiguredAFCGroup(t *testing.T) {
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
		cfg:    config.Config{AFCGroupID: -10042},
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
		t.Fatalf("setMyCommands scope = %#v, want exact AFC chat", scope)
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

func TestBotAFCMessagePreservesRenderedEntitiesOnSendAndEdit(t *testing.T) {
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
	bot := &Bot{cfg: config.Config{AFCGroupID: -1001}, client: client}
	rendered := model.RenderedMessage{
		Text:     "status\nbody",
		Entities: []model.MessageEntity{{Type: "expandable_blockquote", Offset: 7, Length: 4}},
	}
	messageID, err := bot.SendAFCMessage(context.Background(), 11, rendered, true)
	if err != nil || messageID != 81 {
		t.Fatalf("SendAFCMessage id=%d err=%v", messageID, err)
	}
	if err := bot.EditAFCMessage(context.Background(), 11, messageID, rendered); err != nil {
		t.Fatalf("EditAFCMessage failed: %v", err)
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

func TestBotIgnoresUnsupportedMediaOutsideAFCGroup(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":777,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()
	root := t.TempDir()
	service, err := daemon.New(config.Config{AllowedUserIDs: []int64{7}, AFCGroupID: -1001, Paths: config.Paths{
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
