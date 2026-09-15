package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(token string) *Client {
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        20,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	}
	return &Client{
		baseURL: fmt.Sprintf("https://api.telegram.org/bot%s", token),
		http: &http.Client{
			Transport: transport,
			Timeout:   70 * time.Second,
		},
	}
}

type apiResponse[T any] struct {
	OK          bool               `json:"ok"`
	Result      T                  `json:"result"`
	Description string             `json:"description"`
	ErrorCode   int                `json:"error_code"`
	Parameters  apiErrorParameters `json:"parameters,omitempty"`
}

type apiErrorParameters struct {
	RetryAfter int `json:"retry_after,omitempty"`
}

type User struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

type Chat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title,omitempty"`
	Username string `json:"username,omitempty"`
	IsForum  bool   `json:"is_forum,omitempty"`
}

type ChatMember struct {
	Status            string `json:"status"`
	User              User   `json:"user"`
	CanManageTopics   bool   `json:"can_manage_topics,omitempty"`
	CanDeleteMessages bool   `json:"can_delete_messages,omitempty"`
}

type ForumTopic struct {
	MessageThreadID   int64  `json:"message_thread_id"`
	Name              string `json:"name"`
	IconColor         int    `json:"icon_color,omitempty"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
}

type ForumGroupProbe struct {
	Chat        Chat
	BotMember   ChatMember
	UserMember  ChatMember
	MemberCount int
}

var (
	ErrForumSecurity   = errors.New("forum group security contract failed")
	ErrForumCapability = errors.New("forum group capability contract failed")
)

func (p ForumGroupProbe) Validate(expectedChatID, expectedBotID, expectedUserID int64) error {
	if p.Chat.ID != expectedChatID {
		return fmt.Errorf("%w: getChat returned %d for configured chat %d", ErrForumSecurity, p.Chat.ID, expectedChatID)
	}
	if p.Chat.Type != "supergroup" || !p.Chat.IsForum {
		return fmt.Errorf("%w: configured chat must be a forum supergroup", ErrForumCapability)
	}
	if strings.TrimSpace(p.Chat.Username) != "" {
		return fmt.Errorf("%w: configured forum group must be private", ErrForumSecurity)
	}
	if p.BotMember.User.ID != expectedBotID || !p.BotMember.User.IsBot {
		return fmt.Errorf("%w: bot membership identity mismatch", ErrForumSecurity)
	}
	botStatus := strings.ToLower(strings.TrimSpace(p.BotMember.Status))
	if botStatus != "creator" && botStatus != "administrator" {
		return fmt.Errorf("%w: bot must be creator or administrator", ErrForumCapability)
	}
	if botStatus != "creator" && (!p.BotMember.CanManageTopics || !p.BotMember.CanDeleteMessages) {
		return fmt.Errorf("%w: bot requires manage-topics and delete-messages rights", ErrForumCapability)
	}
	if p.UserMember.User.ID != expectedUserID || p.UserMember.User.IsBot || !activeChatMemberStatus(p.UserMember.Status) {
		return fmt.Errorf("%w: allowed user membership mismatch", ErrForumSecurity)
	}
	if p.MemberCount != 2 {
		return fmt.Errorf("%w: expected exactly user + bot, got %d members", ErrForumSecurity, p.MemberCount)
	}
	return nil
}

func activeChatMemberStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "creator", "administrator", "member":
		return true
	default:
		return false
	}
}

type Message struct {
	MessageID       int64             `json:"message_id"`
	MessageThreadID int64             `json:"message_thread_id,omitempty"`
	From            *User             `json:"from"`
	Chat            Chat              `json:"chat"`
	Text            string            `json:"text"`
	Caption         string            `json:"caption,omitempty"`
	Photo           []json.RawMessage `json:"photo,omitempty"`
	Document        json.RawMessage   `json:"document,omitempty"`
	Voice           json.RawMessage   `json:"voice,omitempty"`
	Audio           json.RawMessage   `json:"audio,omitempty"`
	Video           json.RawMessage   `json:"video,omitempty"`
	Entities        []MessageEntity   `json:"entities,omitempty"`
	ReplyToMessage  *Message          `json:"reply_to_message"`
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	EditedMessage *Message       `json:"edited_message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type botCommandScope struct {
	Type   string `json:"type"`
	ChatID int64  `json:"chat_id,omitempty"`
}

type InlineKeyboardButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type MessageEntity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
}

type sendMessageRequest struct {
	ChatID              int64                 `json:"chat_id"`
	Text                string                `json:"text"`
	MessageThreadID     int64                 `json:"message_thread_id,omitempty"`
	ReplyMarkup         *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	DisablePreview      bool                  `json:"disable_web_page_preview,omitempty"`
	DisableNotification bool                  `json:"disable_notification,omitempty"`
	ParseMode           string                `json:"parse_mode,omitempty"`
	Entities            []MessageEntity       `json:"entities,omitempty"`
}

type editMessageTextRequest struct {
	ChatID         int64                 `json:"chat_id"`
	MessageID      int64                 `json:"message_id"`
	Text           string                `json:"text"`
	ReplyMarkup    *InlineKeyboardMarkup `json:"reply_markup,omitempty"`
	DisablePreview bool                  `json:"disable_web_page_preview,omitempty"`
	ParseMode      string                `json:"parse_mode,omitempty"`
	Entities       []MessageEntity       `json:"entities,omitempty"`
}

type deleteMessageRequest struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

type getUpdatesRequest struct {
	Offset         int64    `json:"offset,omitempty"`
	Timeout        int      `json:"timeout,omitempty"`
	AllowedUpdates []string `json:"allowed_updates,omitempty"`
}

type setMyCommandsRequest struct {
	Commands []BotCommand     `json:"commands"`
	Scope    *botCommandScope `json:"scope,omitempty"`
}

type deleteMyCommandsRequest struct {
	Scope *botCommandScope `json:"scope,omitempty"`
}

type answerCallbackQueryRequest struct {
	CallbackQueryID string `json:"callback_query_id"`
	Text            string `json:"text,omitempty"`
	ShowAlert       bool   `json:"show_alert,omitempty"`
}

type chatRequest struct {
	ChatID int64 `json:"chat_id"`
}

type chatMemberRequest struct {
	ChatID int64 `json:"chat_id"`
	UserID int64 `json:"user_id"`
}

type createForumTopicRequest struct {
	ChatID int64  `json:"chat_id"`
	Name   string `json:"name"`
}

type editGeneralForumTopicRequest struct {
	ChatID int64  `json:"chat_id"`
	Name   string `json:"name"`
}

type editForumTopicRequest struct {
	ChatID          int64  `json:"chat_id"`
	MessageThreadID int64  `json:"message_thread_id"`
	Name            string `json:"name"`
}

type forumTopicRequest struct {
	ChatID          int64 `json:"chat_id"`
	MessageThreadID int64 `json:"message_thread_id"`
}

type ForumAPI interface {
	ProbeForumGroup(ctx context.Context, chatID, botID, userID int64) (ForumGroupProbe, error)
	EditGeneralForumTopic(ctx context.Context, chatID int64, name string) error
	CreateForumTopic(ctx context.Context, chatID int64, name string) (*ForumTopic, error)
	EditForumTopic(ctx context.Context, chatID, topicID int64, name string) error
	DeleteForumTopic(ctx context.Context, chatID, topicID int64) error
	SendMessage(ctx context.Context, chatID, topicID int64, text string, markup *InlineKeyboardMarkup, options model.SendOptions) (*Message, error)
	EditMessageText(ctx context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) (*Message, error)
	DeleteMessage(ctx context.Context, chatID, messageID int64) error
}

var _ ForumAPI = (*Client)(nil)

type DocumentFile struct {
	Name        string
	ContentType string
	Data        []byte
}

func (c *Client) GetMe(ctx context.Context) (*User, error) {
	var user User
	if err := c.callJSON(ctx, "getMe", nil, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

func (c *Client) GetChat(ctx context.Context, chatID int64) (*Chat, error) {
	var chat Chat
	if err := c.callJSON(ctx, "getChat", chatRequest{ChatID: chatID}, &chat); err != nil {
		return nil, err
	}
	return &chat, nil
}

func (c *Client) GetChatMember(ctx context.Context, chatID, userID int64) (*ChatMember, error) {
	var member ChatMember
	if err := c.callJSON(ctx, "getChatMember", chatMemberRequest{ChatID: chatID, UserID: userID}, &member); err != nil {
		return nil, err
	}
	return &member, nil
}

func (c *Client) GetChatMemberCount(ctx context.Context, chatID int64) (int, error) {
	var count int
	if err := c.callJSON(ctx, "getChatMemberCount", chatRequest{ChatID: chatID}, &count); err != nil {
		return 0, err
	}
	return count, nil
}

func (c *Client) ProbeForumGroup(ctx context.Context, chatID, botID, userID int64) (ForumGroupProbe, error) {
	chat, err := c.GetChat(ctx, chatID)
	if err != nil {
		return ForumGroupProbe{}, err
	}
	botMember, err := c.GetChatMember(ctx, chatID, botID)
	if err != nil {
		return ForumGroupProbe{}, err
	}
	userMember, err := c.GetChatMember(ctx, chatID, userID)
	if err != nil {
		return ForumGroupProbe{}, err
	}
	count, err := c.GetChatMemberCount(ctx, chatID)
	if err != nil {
		return ForumGroupProbe{}, err
	}
	return ForumGroupProbe{Chat: *chat, BotMember: *botMember, UserMember: *userMember, MemberCount: count}, nil
}

func (c *Client) CreateForumTopic(ctx context.Context, chatID int64, name string) (*ForumTopic, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("forum topic name is required")
	}
	var topic ForumTopic
	if err := c.callJSON(ctx, "createForumTopic", createForumTopicRequest{ChatID: chatID, Name: name}, &topic); err != nil {
		return nil, err
	}
	return &topic, nil
}

func (c *Client) EditGeneralForumTopic(ctx context.Context, chatID int64, name string) error {
	name = strings.TrimSpace(name)
	if chatID == 0 || name == "" {
		return errors.New("forum chat id and general topic name are required")
	}
	return c.callJSON(ctx, "editGeneralForumTopic", editGeneralForumTopicRequest{ChatID: chatID, Name: name}, nil)
}

func (c *Client) EditForumTopic(ctx context.Context, chatID, topicID int64, name string) error {
	name = strings.TrimSpace(name)
	if topicID == 0 || name == "" {
		return errors.New("forum topic id and name are required")
	}
	return c.callJSON(ctx, "editForumTopic", editForumTopicRequest{ChatID: chatID, MessageThreadID: topicID, Name: name}, nil)
}

func (c *Client) DeleteForumTopic(ctx context.Context, chatID, topicID int64) error {
	if topicID == 0 {
		return errors.New("forum topic id is required")
	}
	return c.callJSON(ctx, "deleteForumTopic", forumTopicRequest{ChatID: chatID, MessageThreadID: topicID}, nil)
}

func (c *Client) SetMyCommandsForChat(ctx context.Context, chatID int64, commands []BotCommand) error {
	return c.callJSON(ctx, "setMyCommands", setMyCommandsRequest{
		Commands: commands,
		Scope:    &botCommandScope{Type: "chat", ChatID: chatID},
	}, nil)
}

func (c *Client) DeleteMyCommands(ctx context.Context) error {
	return c.callJSON(ctx, "deleteMyCommands", deleteMyCommandsRequest{}, nil)
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]Update, error) {
	request := getUpdatesRequest{
		Offset:         offset,
		Timeout:        timeoutSeconds,
		AllowedUpdates: []string{"message", "edited_message", "callback_query"},
	}
	var updates []Update
	if err := c.callJSON(ctx, "getUpdates", request, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (c *Client) SendMessage(ctx context.Context, chatID, topicID int64, text string, markup *InlineKeyboardMarkup, options model.SendOptions) (*Message, error) {
	request := sendMessageRequest{
		ChatID:              chatID,
		Text:                text,
		MessageThreadID:     topicID,
		ReplyMarkup:         markup,
		DisablePreview:      true,
		DisableNotification: options.Silent,
	}
	if topicID == 0 {
		request.MessageThreadID = 0
	}
	request.ParseMode = parseModeForText(text)
	var message Message
	if err := c.callJSON(ctx, "sendMessage", request, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func (c *Client) SendRenderedMessage(ctx context.Context, chatID, topicID int64, rendered model.RenderedMessage, markup *InlineKeyboardMarkup, options model.SendOptions) (*Message, error) {
	request := sendMessageRequest{
		ChatID:              chatID,
		Text:                rendered.Text,
		MessageThreadID:     topicID,
		ReplyMarkup:         markup,
		DisablePreview:      true,
		DisableNotification: options.Silent,
		Entities:            toAPIEntities(rendered.Entities),
	}
	if topicID == 0 {
		request.MessageThreadID = 0
	}
	var message Message
	if err := c.callJSON(ctx, "sendMessage", request, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func (c *Client) EditMessageText(ctx context.Context, chatID, messageID int64, text string, markup *InlineKeyboardMarkup) (*Message, error) {
	request := editMessageTextRequest{
		ChatID:         chatID,
		MessageID:      messageID,
		Text:           text,
		ReplyMarkup:    markup,
		DisablePreview: true,
	}
	request.ParseMode = parseModeForText(text)
	var message Message
	if err := c.callJSON(ctx, "editMessageText", request, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func (c *Client) EditRenderedMessageText(ctx context.Context, chatID, messageID int64, rendered model.RenderedMessage, markup *InlineKeyboardMarkup) (*Message, error) {
	request := editMessageTextRequest{
		ChatID:         chatID,
		MessageID:      messageID,
		Text:           rendered.Text,
		ReplyMarkup:    markup,
		DisablePreview: true,
		Entities:       toAPIEntities(rendered.Entities),
	}
	var message Message
	if err := c.callJSON(ctx, "editMessageText", request, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func (c *Client) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return c.callJSON(ctx, "deleteMessage", deleteMessageRequest{
		ChatID:    chatID,
		MessageID: messageID,
	}, nil)
}

func (c *Client) SendDocument(ctx context.Context, chatID, topicID int64, document DocumentFile, caption string, markup *InlineKeyboardMarkup, options model.SendOptions) (*Message, error) {
	if strings.TrimSpace(document.Name) == "" {
		document.Name = "document.txt"
	}
	fields := map[string]string{
		"chat_id": strconvFormatInt(chatID),
	}
	if topicID != 0 {
		fields["message_thread_id"] = strconvFormatInt(topicID)
	}
	if options.Silent {
		fields["disable_notification"] = "true"
	}
	if strings.TrimSpace(caption) != "" {
		fields["caption"] = caption
	}
	if markup != nil {
		encoded, err := json.Marshal(markup)
		if err != nil {
			return nil, err
		}
		fields["reply_markup"] = string(encoded)
	}
	var message Message
	if err := c.callMultipart(ctx, "sendDocument", fields, "document", document, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

func (c *Client) AnswerCallbackQuery(ctx context.Context, callbackQueryID, text string, showAlert bool) error {
	request := answerCallbackQueryRequest{
		CallbackQueryID: callbackQueryID,
		Text:            text,
		ShowAlert:       showAlert,
	}
	return c.callJSON(ctx, "answerCallbackQuery", request, nil)
}

func (c *Client) callMultipart(ctx context.Context, method string, fields map[string]string, fileField string, document DocumentFile, out any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range fields {
		if strings.TrimSpace(value) == "" {
			continue
		}
		if err := writer.WriteField(key, value); err != nil {
			return err
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fileField, escapeQuotes(document.Name)))
	contentType := strings.TrimSpace(document.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(document.Data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body.Bytes()))
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiHTTPError(method, response.StatusCode, data)
	}
	return decodeAPIResponse(method, data, out)
}

func (c *Client) callJSON(ctx context.Context, method string, payload any, out any) error {
	var body io.Reader = http.NoBody
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := strings.TrimRight(c.baseURL, "/") + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiHTTPError(method, response.StatusCode, data)
	}
	return decodeAPIResponse(method, data, out)
}

func decodeAPIResponse(method string, data []byte, out any) error {
	if out == nil {
		var envelope apiResponse[json.RawMessage]
		if err := json.Unmarshal(data, &envelope); err != nil {
			return err
		}
		if !envelope.OK {
			return apiError(method, envelope.ErrorCode, envelope.Description, envelope.Parameters.RetryAfter)
		}
		return nil
	}
	envelope := apiResponse[json.RawMessage]{}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	if !envelope.OK {
		return apiError(method, envelope.ErrorCode, envelope.Description, envelope.Parameters.RetryAfter)
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil
	}
	return json.Unmarshal(envelope.Result, out)
}

func escapeQuotes(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func parseModeForText(text string) string {
	if strings.Contains(text, "<pre><code") {
		return "HTML"
	}
	return ""
}

func toAPIEntities(entities []model.MessageEntity) []MessageEntity {
	out := make([]MessageEntity, 0, len(entities))
	for _, entity := range entities {
		out = append(out, MessageEntity{
			Type:     entity.Type,
			Offset:   entity.Offset,
			Length:   entity.Length,
			URL:      entity.URL,
			Language: entity.Language,
		})
	}
	return out
}

func strconvFormatInt(value int64) string {
	return fmt.Sprintf("%d", value)
}

type APIError struct {
	Method      string
	Code        int
	HTTPStatus  int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	description := strings.TrimSpace(e.Description)
	if description == "" {
		description = "unknown telegram api error"
	}
	code := e.Code
	if code == 0 {
		code = e.HTTPStatus
	}
	if code == 0 {
		return fmt.Sprintf("telegram %s: %s", e.Method, description)
	}
	return fmt.Sprintf("telegram %s: %d %s", e.Method, code, description)
}

func IsTopicNotFound(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return strings.Contains(description, "message thread not found") ||
		strings.Contains(description, "topic_id_invalid") ||
		strings.Contains(description, "topic_closed") ||
		strings.Contains(description, "topic was deleted")
}

func IsTopicNotModified(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	description := strings.ToLower(apiErr.Description)
	return strings.Contains(description, "topic_not_modified") ||
		strings.Contains(description, "not modified")
}

func IsRetryable(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == http.StatusTooManyRequests || apiErr.HTTPStatus == http.StatusTooManyRequests || apiErr.Code >= 500 || apiErr.HTTPStatus >= 500
}

func apiHTTPError(method string, status int, data []byte) error {
	if err := decodeAPIResponse(method, data, nil); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			apiErr.HTTPStatus = status
			return err
		}
	}
	return &APIError{Method: method, HTTPStatus: status, Description: strings.TrimSpace(string(data))}
}

func apiError(method string, code int, description string, retryAfter int) error {
	description = strings.TrimSpace(description)
	return &APIError{Method: method, Code: code, Description: description, RetryAfter: retryAfter}
}
