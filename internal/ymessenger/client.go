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
	ID    string `json:"id"`
	Login string `json:"login"`
	Robot bool   `json:"robot"`
}

type Chat struct {
	ID       string `json:"id"`
	ThreadID int64  `json:"thread_id"`
}

type Update struct {
	UpdateID       int64  `json:"update_id"`
	MessageID      int64  `json:"message_id"`
	Timestamp      int64  `json:"timestamp"`
	From           User   `json:"from"`
	Chat           Chat   `json:"chat"`
	Text           string `json:"text"`
	MentionedUsers []User `json:"mentioned_users"`
}

type updatesResponse struct {
	OK          bool     `json:"ok"`
	Updates     []Update `json:"updates"`
	Description string   `json:"description"`
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
