package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mideco-tech/codex-tg/internal/model"
)

func TestClientForumTopicOperations(t *testing.T) {
	t.Parallel()

	type requestRecord struct {
		path string
		body map[string]any
	}
	var requests []requestRecord
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("Decode(%s): %v", r.URL.Path, err)
		}
		requests = append(requests, requestRecord{path: r.URL.Path, body: body})
		switch r.URL.Path {
		case "/createForumTopic":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_thread_id":77,"name":"Alpha","icon_color":7322096}}`))
		case "/editForumTopic", "/deleteForumTopic":
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	topic, err := client.CreateForumTopic(context.Background(), -10042, "Alpha")
	if err != nil {
		t.Fatal(err)
	}
	if topic == nil || topic.MessageThreadID != 77 || topic.Name != "Alpha" {
		t.Fatalf("topic = %#v", topic)
	}
	if err := client.EditForumTopic(context.Background(), -10042, 77, "Beta"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteForumTopic(context.Background(), -10042, 77); err != nil {
		t.Fatal(err)
	}

	if len(requests) != 3 {
		t.Fatalf("requests = %#v", requests)
	}
	if requests[0].path != "/createForumTopic" || requests[0].body["chat_id"] != float64(-10042) || requests[0].body["name"] != "Alpha" {
		t.Fatalf("create request = %#v", requests[0])
	}
	if requests[1].path != "/editForumTopic" || requests[1].body["message_thread_id"] != float64(77) || requests[1].body["name"] != "Beta" {
		t.Fatalf("edit request = %#v", requests[1])
	}
	if requests[2].path != "/deleteForumTopic" || requests[2].body["message_thread_id"] != float64(77) {
		t.Fatalf("delete request = %#v", requests[2])
	}
}

func TestClientProbeForumGroupAndValidateSecurity(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/getChat":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":-10042,"type":"supergroup","title":"AFC","is_forum":true}}`))
		case "/getChatMember":
			if body["user_id"] == float64(100) {
				_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"administrator","user":{"id":100,"is_bot":true},"can_manage_topics":true,"can_delete_messages":true}}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"status":"creator","user":{"id":200,"is_bot":false}}}`))
		case "/getChatMemberCount":
			_, _ = w.Write([]byte(`{"ok":true,"result":2}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	probe, err := client.ProbeForumGroup(context.Background(), -10042, 100, 200)
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Validate(-10042, 100, 200); err != nil {
		t.Fatalf("Validate failed: %v", err)
	}

	probe.MemberCount = 3
	if err := probe.Validate(-10042, 100, 200); !errors.Is(err, ErrForumSecurity) {
		t.Fatalf("extra-member validation error = %v, want ErrForumSecurity", err)
	}
	probe.MemberCount = 2
	probe.BotMember.CanManageTopics = false
	if err := probe.Validate(-10042, 100, 200); !errors.Is(err, ErrForumCapability) {
		t.Fatalf("missing-right validation error = %v, want ErrForumCapability", err)
	}
	probe.BotMember.CanManageTopics = true
	probe.Chat.Username = "public_afc"
	if err := probe.Validate(-10042, 100, 200); !errors.Is(err, ErrForumSecurity) {
		t.Fatalf("public-group validation error = %v, want ErrForumSecurity", err)
	}
}

func TestTelegramAPIErrorClassifiesTopicAndRetryFailures(t *testing.T) {
	t.Parallel()

	err := decodeAPIResponse("sendMessage", []byte(`{"ok":false,"error_code":400,"description":"Bad Request: message thread not found"}`), nil)
	if !IsTopicNotFound(err) {
		t.Fatalf("IsTopicNotFound(%v) = false", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Method != "sendMessage" || apiErr.Code != 400 {
		t.Fatalf("APIError = %#v", apiErr)
	}

	err = decodeAPIResponse("sendMessage", []byte(`{"ok":false,"error_code":429,"description":"Too Many Requests","parameters":{"retry_after":7}}`), nil)
	if !IsRetryable(err) {
		t.Fatalf("IsRetryable(%v) = false", err)
	}
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 7 {
		t.Fatalf("retry APIError = %#v", apiErr)
	}
}

func TestClientPlainHTTPFailureRemainsTypedAndRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("upstream unavailable"))
	}))
	defer server.Close()
	client := NewClient("token")
	client.baseURL = server.URL
	_, err := client.SendMessage(context.Background(), -10042, 77, "hello", nil, model.SendOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusServiceUnavailable || apiErr.Method != "sendMessage" {
		t.Fatalf("error = %#v", err)
	}
	if !IsRetryable(err) {
		t.Fatalf("IsRetryable(%v) = false", err)
	}
}

func TestClientEditMessageTextSendsExpectedJSON(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/editMessageText" {
			t.Fatalf("path = %q, want /editMessageText", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77,"chat":{"id":42,"type":"private"},"text":"updated"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	message, err := client.EditMessageText(context.Background(), 42, 77, "updated", &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{{{Text: "Open", CallbackData: "cb-1"}}},
	})
	if err != nil {
		t.Fatalf("EditMessageText failed: %v", err)
	}
	if message == nil || message.MessageID != 77 {
		t.Fatalf("message = %#v, want message_id=77", message)
	}
	if got, want := captured["chat_id"], float64(42); got != want {
		t.Fatalf("chat_id = %#v, want %#v", got, want)
	}
	if got, want := captured["message_id"], float64(77); got != want {
		t.Fatalf("message_id = %#v, want %#v", got, want)
	}
	if got, want := captured["text"], "updated"; got != want {
		t.Fatalf("text = %#v, want %q", got, want)
	}
	if got, ok := captured["disable_web_page_preview"].(bool); !ok || !got {
		t.Fatalf("disable_web_page_preview = %#v, want true", captured["disable_web_page_preview"])
	}
	if _, ok := captured["parse_mode"]; ok {
		t.Fatalf("parse_mode = %#v, want omitted for plain text", captured["parse_mode"])
	}
}

func TestClientSendMessageUsesHTMLParseModeForMixedCodeBlock(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("path = %q, want /sendMessage", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":78,"chat":{"id":42,"type":"private"},"text":"[Tool]"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	message, err := client.SendMessage(context.Background(), 42, 0, `[Tool]
<pre><code class="language-powershell">Status: completed</code></pre>`, nil, model.SendOptions{})
	if err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if message == nil || message.MessageID != 78 {
		t.Fatalf("message = %#v, want message_id=78", message)
	}
	if got, want := captured["parse_mode"], "HTML"; got != want {
		t.Fatalf("parse_mode = %#v, want %q", got, want)
	}
	if _, ok := captured["disable_notification"]; ok {
		t.Fatalf("disable_notification = %#v, want omitted for audible message", captured["disable_notification"])
	}
}

func TestClientSendMessageSilentSetsDisableNotification(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("path = %q, want /sendMessage", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":79,"chat":{"id":42,"type":"private"},"text":"silent"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	if _, err := client.SendMessage(context.Background(), 42, 0, "silent", nil, model.SendOptions{Silent: true}); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	if got, ok := captured["disable_notification"].(bool); !ok || !got {
		t.Fatalf("disable_notification = %#v, want true", captured["disable_notification"])
	}
}

func TestClientDeleteMessageSendsExpectedJSON(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/deleteMessage" {
			t.Fatalf("path = %q, want /deleteMessage", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	if err := client.DeleteMessage(context.Background(), 42, 77); err != nil {
		t.Fatalf("DeleteMessage failed: %v", err)
	}
	if got, want := captured["chat_id"], float64(42); got != want {
		t.Fatalf("chat_id = %#v, want %#v", got, want)
	}
	if got, want := captured["message_id"], float64(77); got != want {
		t.Fatalf("message_id = %#v, want %#v", got, want)
	}
}

func TestClientSendRenderedMessageUsesEntitiesWithoutParseMode(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendMessage" {
			t.Fatalf("path = %q, want /sendMessage", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":80,"chat":{"id":42,"type":"private"},"text":"formatted"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	message, err := client.SendRenderedMessage(context.Background(), 42, 0, model.RenderedMessage{
		Text: "formatted",
		Entities: []model.MessageEntity{{
			Type:     "pre",
			Offset:   0,
			Length:   9,
			Language: "bash",
		}},
	}, nil, model.SendOptions{})
	if err != nil {
		t.Fatalf("SendRenderedMessage failed: %v", err)
	}
	if message == nil || message.MessageID != 80 {
		t.Fatalf("message = %#v, want message_id=80", message)
	}
	if _, ok := captured["parse_mode"]; ok {
		t.Fatalf("parse_mode = %#v, want omitted when entities are supplied", captured["parse_mode"])
	}
	entities, ok := captured["entities"].([]any)
	if !ok || len(entities) != 1 {
		t.Fatalf("entities = %#v, want one entity", captured["entities"])
	}
	entity, ok := entities[0].(map[string]any)
	if !ok {
		t.Fatalf("entity = %#v, want object", entities[0])
	}
	if got, want := entity["type"], "pre"; got != want {
		t.Fatalf("entity.type = %#v, want %q", got, want)
	}
	if got, want := entity["language"], "bash"; got != want {
		t.Fatalf("entity.language = %#v, want %q", got, want)
	}
}

func TestClientEditMessageTextUsesHTMLParseModeForMixedCodeBlock(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/editMessageText" {
			t.Fatalf("path = %q, want /editMessageText", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":79,"chat":{"id":42,"type":"private"},"text":"[Output]"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	message, err := client.EditMessageText(context.Background(), 42, 79, `[Output]
<pre><code class="language-bash">hello</code></pre>`, nil)
	if err != nil {
		t.Fatalf("EditMessageText failed: %v", err)
	}
	if message == nil || message.MessageID != 79 {
		t.Fatalf("message = %#v, want message_id=79", message)
	}
	if got, want := captured["parse_mode"], "HTML"; got != want {
		t.Fatalf("parse_mode = %#v, want %q", got, want)
	}
}

func TestClientEditRenderedMessageTextUsesEntitiesWithoutParseMode(t *testing.T) {
	t.Parallel()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/editMessageText" {
			t.Fatalf("path = %q, want /editMessageText", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("ReadAll failed: %v", err)
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatalf("json.Unmarshal failed: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":81,"chat":{"id":42,"type":"private"},"text":"updated"}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	message, err := client.EditRenderedMessageText(context.Background(), 42, 81, model.RenderedMessage{
		Text:     "updated",
		Entities: []model.MessageEntity{{Type: "code", Offset: 0, Length: 7}},
	}, nil)
	if err != nil {
		t.Fatalf("EditRenderedMessageText failed: %v", err)
	}
	if message == nil || message.MessageID != 81 {
		t.Fatalf("message = %#v, want message_id=81", message)
	}
	if _, ok := captured["parse_mode"]; ok {
		t.Fatalf("parse_mode = %#v, want omitted when entities are supplied", captured["parse_mode"])
	}
	if entities, ok := captured["entities"].([]any); !ok || len(entities) != 1 {
		t.Fatalf("entities = %#v, want one entity", captured["entities"])
	}
}

func TestClientSendDocumentUsesMultipartForm(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendDocument" {
			t.Fatalf("path = %q, want /sendDocument", r.URL.Path)
		}
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("ParseMediaType failed: %v", err)
		}
		if mediaType != "multipart/form-data" {
			t.Fatalf("mediaType = %q, want multipart/form-data", mediaType)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string]string{}
		var documentName, documentBody, documentType string
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart failed: %v", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("ReadAll(part) failed: %v", err)
			}
			if part.FormName() == "document" {
				documentName = part.FileName()
				documentBody = string(data)
				documentType = part.Header.Get("Content-Type")
				continue
			}
			fields[part.FormName()] = string(data)
		}
		if got, want := fields["chat_id"], "42"; got != want {
			t.Fatalf("chat_id = %q, want %q", got, want)
		}
		if got, want := fields["message_thread_id"], "9"; got != want {
			t.Fatalf("message_thread_id = %q, want %q", got, want)
		}
		if got, want := fields["caption"], "observer dump"; got != want {
			t.Fatalf("caption = %q, want %q", got, want)
		}
		if !strings.Contains(fields["reply_markup"], `"callback_data":"cb-1"`) {
			t.Fatalf("reply_markup = %q, want callback data", fields["reply_markup"])
		}
		if _, ok := fields["disable_notification"]; ok {
			t.Fatalf("disable_notification = %q, want omitted for audible document", fields["disable_notification"])
		}
		if got, want := documentName, "observer.txt"; got != want {
			t.Fatalf("filename = %q, want %q", got, want)
		}
		if got, want := documentType, "text/plain"; got != want {
			t.Fatalf("content-type = %q, want %q", got, want)
		}
		if got, want := documentBody, "payload"; got != want {
			t.Fatalf("document body = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":501,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	message, err := client.SendDocument(context.Background(), 42, 9, DocumentFile{
		Name:        "observer.txt",
		ContentType: "text/plain",
		Data:        []byte("payload"),
	}, "observer dump", &InlineKeyboardMarkup{
		InlineKeyboard: [][]InlineKeyboardButton{{{Text: "Open", CallbackData: "cb-1"}}},
	}, model.SendOptions{})
	if err != nil {
		t.Fatalf("SendDocument failed: %v", err)
	}
	if message == nil || message.MessageID != 501 {
		t.Fatalf("message = %#v, want message_id=501", message)
	}
}

func TestClientSendDocumentSilentSetsDisableNotification(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sendDocument" {
			t.Fatalf("path = %q, want /sendDocument", r.URL.Path)
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Fatalf("ParseMediaType failed: %v", err)
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		fields := map[string]string{}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart failed: %v", err)
			}
			data, err := io.ReadAll(part)
			if err != nil {
				t.Fatalf("ReadAll(part) failed: %v", err)
			}
			if part.FormName() != "document" {
				fields[part.FormName()] = string(data)
			}
		}
		if got, want := fields["disable_notification"], "true"; got != want {
			t.Fatalf("disable_notification = %q, want %q", got, want)
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":502,"chat":{"id":42,"type":"private"}}}`))
	}))
	defer server.Close()

	client := NewClient("token")
	client.baseURL = server.URL
	if _, err := client.SendDocument(context.Background(), 42, 0, DocumentFile{
		Name: "silent.txt",
		Data: []byte("payload"),
	}, "", nil, model.SendOptions{Silent: true}); err != nil {
		t.Fatalf("SendDocument failed: %v", err)
	}
}
