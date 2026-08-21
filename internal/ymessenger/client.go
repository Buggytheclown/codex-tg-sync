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

const (
	defaultBaseURL        = "https://bp.mssngr.yandex.net/public/bot/v1"
	defaultHistoryBaseURL = "https://backend.messenger.yandex-team.ru"
)

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

type ContextMessage struct {
	MessageID int64
	Timestamp int64
	From      User
	Text      string
}

type historyResponse struct {
	Error struct {
		Message string `json:"Message"`
	} `json:"Error"`
	Chats []struct {
		Messages []struct {
			ServerMessage struct {
				ClientMessage struct {
					Plain struct {
						Text struct {
							MessageText string `json:"MessageText"`
						} `json:"Text"`
						Gallery struct {
							Text string `json:"Text"`
						} `json:"Gallery"`
					} `json:"Plain"`
				} `json:"ClientMessage"`
				ServerMessageInfo struct {
					Timestamp int64 `json:"Timestamp"`
					From      struct {
						Login       string `json:"Login"`
						DisplayName string `json:"DisplayName"`
						Robot       bool   `json:"IsRobot"`
					} `json:"From"`
				} `json:"ServerMessageInfo"`
			} `json:"ServerMessage"`
		} `json:"Messages"`
	} `json:"Chats"`
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
	token          string
	baseURL        string
	historyBaseURL string
	httpClient     *http.Client
}

type ClientOption func(*Client)

func WithBaseURL(baseURL string) ClientOption {
	return func(client *Client) { client.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

func WithHistoryBaseURL(baseURL string) ClientOption {
	return func(client *Client) { client.historyBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/") }
}

func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) { client.httpClient = httpClient }
}

func NewClient(token string, options ...ClientOption) *Client {
	client := &Client{
		token: strings.TrimSpace(token), baseURL: defaultBaseURL, historyBaseURL: defaultHistoryBaseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
	for _, option := range options {
		option(client)
	}
	return client
}

func (c *Client) GetThreadRoot(ctx context.Context, chatID string, messageID int64) (*ContextMessage, error) {
	if c == nil || strings.TrimSpace(c.token) == "" {
		return nil, errors.New("YMessenger robot OAuth token is not configured")
	}
	chatID = strings.TrimSpace(chatID)
	if chatID == "" || messageID <= 0 {
		return nil, errors.New("YMessenger thread root requires chat and message")
	}
	payload := struct {
		ChatID       string `json:"ChatId"`
		MinTimestamp int64  `json:"MinTimestamp"`
		MaxTimestamp int64  `json:"MaxTimestamp"`
		Limit        int    `json:"Limit"`
	}{ChatID: chatID, MinTimestamp: messageID - 1, MaxTimestamp: messageID + 1, Limit: 10}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.historyBaseURL+"/history", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "OAuth "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("YMessenger history: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("YMessenger history returned HTTP %d", response.StatusCode)
	}
	var result historyResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode YMessenger history: %w", err)
	}
	if strings.TrimSpace(result.Error.Message) != "" {
		return nil, errors.New("YMessenger history returned an API error")
	}
	for _, chat := range result.Chats {
		for _, wrapped := range chat.Messages {
			message := wrapped.ServerMessage
			if message.ServerMessageInfo.Timestamp != messageID {
				continue
			}
			text := strings.TrimSpace(message.ClientMessage.Plain.Text.MessageText)
			if text == "" {
				text = strings.TrimSpace(message.ClientMessage.Plain.Gallery.Text)
			}
			if text == "" {
				return nil, nil
			}
			from := message.ServerMessageInfo.From
			return &ContextMessage{
				MessageID: messageID, Timestamp: message.ServerMessageInfo.Timestamp, Text: text,
				From: User{Login: from.Login, DisplayName: from.DisplayName, Robot: from.Robot},
			}, nil
		}
	}
	return nil, nil
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
