package ymessenger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://bp.mssngr.yandex.net/public/bot/v1"

type User struct {
	ID          string `json:"id"`
	Login       string `json:"login"`
	DisplayName string `json:"display_name"`
	Robot       bool   `json:"robot"`
}

type Chat struct {
	ID       string `json:"id"`
	ThreadID int64  `json:"thread_id"`
	Title    string `json:"title"`
	Type     string `json:"type"`
}

type Update struct {
	UpdateID       int64   `json:"update_id"`
	MessageID      int64   `json:"message_id"`
	Timestamp      int64   `json:"timestamp"`
	From           User    `json:"from"`
	Chat           Chat    `json:"chat"`
	Text           string  `json:"text"`
	ReplyToMessage *Update `json:"reply_to_message"`
	MentionedUsers []User  `json:"mentioned_users"`
}

type updatesResponse struct {
	OK          bool     `json:"ok"`
	Updates     []Update `json:"updates"`
	Description string   `json:"description"`
}

type sendTextResponse struct {
	OK          bool   `json:"ok"`
	MessageID   int64  `json:"message_id"`
	Description string `json:"description"`
}

type Client struct {
	token      string
	baseURL    string
	httpClient *http.Client
}

type ClientOption func(*Client)

func WithBaseURL(baseURL string) ClientOption {
	return func(client *Client) { client.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) { client.httpClient = httpClient }
}

func NewClient(token string, options ...ClientOption) *Client {
	client := &Client{token: strings.TrimSpace(token), baseURL: defaultBaseURL, httpClient: &http.Client{Timeout: 30 * time.Second}}
	for _, option := range options {
		option(client)
	}
	return client
}

func (c *Client) GetUpdates(ctx context.Context, offset int64, limit int) ([]Update, error) {
	if c == nil || strings.TrimSpace(c.token) == "" {
		return nil, errors.New("YMessenger OAuthTeam token is not configured")
	}
	if limit <= 0 {
		limit = 100
	}
	endpoint, err := url.Parse(c.baseURL + "/messages/getUpdates")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("offset", strconv.FormatInt(offset, 10))
	query.Set("limit", strconv.Itoa(limit))
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "OAuthTeam "+c.token)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("YMessenger getUpdates: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("YMessenger getUpdates returned HTTP %d", response.StatusCode)
	}
	var payload updatesResponse
	decoder := json.NewDecoder(io.LimitReader(response.Body, 4<<20))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode YMessenger getUpdates: %w", err)
	}
	if !payload.OK {
		description := strings.TrimSpace(payload.Description)
		if description == "" {
			description = "API returned ok=false"
		}
		return nil, errors.New("YMessenger getUpdates: " + description)
	}
	return payload.Updates, nil
}

func (c *Client) SendExternalReply(ctx context.Context, chatID string, replyMessageID, threadID int64, text string) (int64, error) {
	if c == nil || strings.TrimSpace(c.token) == "" {
		return 0, errors.New("YMessenger OAuthTeam token is not configured")
	}
	chatID = strings.TrimSpace(chatID)
	text = strings.TrimSpace(text)
	if chatID == "" || replyMessageID == 0 || text == "" {
		return 0, errors.New("YMessenger reply requires chat, message, and text")
	}
	payload := struct {
		ChatID         string `json:"chat_id"`
		Text           string `json:"text"`
		ReplyMessageID int64  `json:"reply_message_id"`
		ThreadID       int64  `json:"thread_id,omitempty"`
	}{ChatID: chatID, Text: truncateText(text, 4000), ReplyMessageID: replyMessageID, ThreadID: threadID}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages/sendText", strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Authorization", "OAuthTeam "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return 0, fmt.Errorf("YMessenger sendText: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return 0, fmt.Errorf("YMessenger sendText returned HTTP %d", response.StatusCode)
	}
	var result sendTextResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return 0, fmt.Errorf("decode YMessenger sendText: %w", err)
	}
	if !result.OK || result.MessageID == 0 {
		description := strings.TrimSpace(result.Description)
		if description == "" {
			description = "API returned no message id"
		}
		return 0, errors.New("YMessenger sendText: " + description)
	}
	return result.MessageID, nil
}

func truncateText(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	const suffix = "\n\n[Response truncated]"
	suffixRunes := []rune(suffix)
	if limit <= len(suffixRunes) {
		return string(runes[:limit])
	}
	return strings.TrimSpace(string(runes[:limit-len(suffixRunes)])) + suffix
}
