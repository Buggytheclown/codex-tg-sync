package appserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/mideco-tech/codex-tg/internal/control"
	"github.com/mideco-tech/codex-tg/internal/version"
)

type Event = control.Event
type TurnStartOptions = control.TurnStartOptions
type ThreadStartOptions = control.ThreadStartOptions
type ModelOption = control.ModelOption
type CollaborationModeOption = control.CollaborationModeOption

var _ control.ControlPlane = (*Client)(nil)

type TransportMode string

const (
	TransportSpawned   TransportMode = "spawned"
	TransportDaemon    TransportMode = "daemon"
	TransportWebSocket TransportMode = "websocket"
)

type TransportConfig struct {
	Mode       TransportMode
	ListenURL  string
	SocketPath string
}

func IsSharedTransportMode(mode string) bool {
	switch TransportMode(strings.ToLower(strings.TrimSpace(mode))) {
	case TransportDaemon, TransportWebSocket:
		return true
	default:
		return false
	}
}

type rpcResponse struct {
	Result any
	Error  error
}

type websocketConnection struct {
	conn      *websocket.Conn
	closeOnce sync.Once
}

func (c *websocketConnection) Close() error {
	var err error
	c.closeOnce.Do(func() { err = c.conn.Close() })
	return err
}

type websocketWriteCloser struct {
	connection *websocketConnection
}

func (w *websocketWriteCloser) Write(payload []byte) (int, error) {
	message := bytes.TrimSuffix(payload, []byte{'\n'})
	if err := w.connection.conn.WriteMessage(websocket.TextMessage, message); err != nil {
		return 0, err
	}
	return len(payload), nil
}

func (w *websocketWriteCloser) Close() error { return w.connection.Close() }

type websocketReadCloser struct {
	connection *websocketConnection
	mu         sync.Mutex
	buffer     *bytes.Reader
}

func (r *websocketReadCloser) Read(destination []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if r.buffer != nil {
			read, err := r.buffer.Read(destination)
			if err == io.EOF {
				r.buffer = nil
				if read > 0 {
					return read, nil
				}
				continue
			}
			return read, err
		}
		messageType, payload, err := r.connection.conn.ReadMessage()
		if err != nil {
			return 0, err
		}
		if messageType != websocket.TextMessage && messageType != websocket.BinaryMessage {
			continue
		}
		framed := make([]byte, len(payload)+1)
		copy(framed, payload)
		framed[len(payload)] = '\n'
		r.buffer = bytes.NewReader(framed)
	}
}

func (r *websocketReadCloser) Close() error { return r.connection.Close() }

type Client struct {
	codexBin       string
	transport      TransportConfig
	cwd            string
	requestTimeout time.Duration

	startMu        sync.Mutex
	writeMu        sync.Mutex
	mu             sync.Mutex
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout         io.ReadCloser
	stderr         io.ReadCloser
	pending        map[uint64]chan rpcResponse
	subscribers    []chan Event
	nextID         uint64
	generation     uint64
	stderrLines    []string
	started        bool
	readerDone     chan struct{}
	stderrDone     chan struct{}
	serverRequests map[string]map[string]any
}

func NewClient(codexBin, listenURL, cwd string, requestTimeout time.Duration) *Client {
	return NewClientWithTransport(codexBin, TransportConfig{
		Mode:      TransportSpawned,
		ListenURL: listenURL,
	}, cwd, requestTimeout)
}

func NewClientWithTransport(codexBin string, transport TransportConfig, cwd string, requestTimeout time.Duration) *Client {
	if transport.Mode == "" {
		transport.Mode = TransportSpawned
	}
	if strings.TrimSpace(transport.ListenURL) == "" {
		transport.ListenURL = "stdio://"
	}
	return &Client{
		codexBin:       codexBin,
		transport:      transport,
		cwd:            cwd,
		requestTimeout: requestTimeout,
		pending:        map[uint64]chan rpcResponse{},
		serverRequests: map[string]map[string]any{},
		readerDone:     make(chan struct{}),
		stderrDone:     make(chan struct{}),
	}
}

func (c *Client) Start(ctx context.Context) error {
	c.startMu.Lock()
	defer c.startMu.Unlock()

	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return nil
	}
	cmd, stdin, stdout, stderr, err := c.openTransport(ctx)
	if err != nil {
		c.mu.Unlock()
		return err
	}
	c.cmd = cmd
	c.stdin = stdin
	c.stdout = stdout
	c.stderr = stderr
	c.started = true
	c.generation++
	generation := c.generation
	c.readerDone = make(chan struct{})
	c.stderrDone = make(chan struct{})
	c.mu.Unlock()

	go c.readStdout(generation)
	go c.readStderr(generation)
	if _, err := c.Request(ctx, "initialize", map[string]any{
		"capabilities": map[string]any{"experimentalApi": true},
		"clientInfo": map[string]any{
			"name":    "codex-tg",
			"title":   "codex-tg Telegram bridge",
			"version": version.Version,
		},
	}); err != nil {
		_ = c.closeRunning()
		return err
	}
	if err := c.Notify(ctx, "initialized", nil); err != nil {
		_ = c.closeRunning()
		return err
	}
	return nil
}

func (c *Client) Close() error {
	c.startMu.Lock()
	defer c.startMu.Unlock()
	return c.closeRunning()
}

func (c *Client) closeRunning() error {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return nil
	}
	cmd := c.cmd
	stdin := c.stdin
	pending := c.pending
	c.pending = map[uint64]chan rpcResponse{}
	c.started = false
	c.generation++
	c.cmd = nil
	c.stdin = nil
	c.stdout = nil
	c.stderr = nil
	c.mu.Unlock()

	if stdin != nil {
		c.writeMu.Lock()
		_ = stdin.Close()
		c.writeMu.Unlock()
	}
	for _, ch := range pending {
		select {
		case ch <- rpcResponse{Error: errors.New("app-server closed before response")}:
		default:
		}
		close(ch)
	}
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return nil
}

func (c *Client) Subscribe() <-chan Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan Event, 128)
	c.subscribers = append(c.subscribers, ch)
	return ch
}

func (c *Client) Request(ctx context.Context, method string, params map[string]any) (any, error) {
	c.mu.Lock()
	if !c.started || c.stdin == nil {
		c.mu.Unlock()
		return nil, errors.New("app-server is not running")
	}
	c.nextID++
	id := c.nextID
	reply := make(chan rpcResponse, 1)
	c.pending[id] = reply
	stdin := c.stdin
	c.mu.Unlock()

	message := map[string]any{
		"id":     id,
		"method": method,
	}
	if params != nil {
		message["params"] = params
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return nil, err
	}
	if err := c.writeJSONRPC(stdin, payload); err != nil {
		c.broadcast(Event{Channel: "transport_error", Params: map[string]any{"stream": "stdin", "method": method, "error": err.Error(), "stderr_tail": c.StderrTail()}})
		return nil, err
	}

	timeout := c.requestTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	if timeout <= 0 {
		timeout = c.requestTimeout
	}

	select {
	case response := <-reply:
		if response.Error != nil {
			return nil, response.Error
		}
		return response.Result, nil
	case <-time.After(timeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("request timeout for %s", method)
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *Client) Notify(ctx context.Context, method string, params map[string]any) error {
	c.mu.Lock()
	if !c.started || c.stdin == nil {
		c.mu.Unlock()
		return errors.New("app-server is not running")
	}
	stdin := c.stdin
	c.mu.Unlock()
	message := map[string]any{
		"method": method,
	}
	if params != nil {
		message["params"] = params
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if err := c.writeJSONRPC(stdin, payload); err != nil {
		c.broadcast(Event{Channel: "transport_error", Params: map[string]any{"stream": "stdin", "method": method, "error": err.Error(), "stderr_tail": c.StderrTail()}})
		return err
	}
	return nil
}

func (c *Client) RespondServerRequest(ctx context.Context, requestID string, result map[string]any) error {
	c.mu.Lock()
	if !c.started || c.stdin == nil {
		c.mu.Unlock()
		return errors.New("app-server is not running")
	}
	stdin := c.stdin
	delete(c.serverRequests, requestID)
	c.mu.Unlock()
	payload, err := json.Marshal(map[string]any{
		"id":     requestID,
		"result": result,
	})
	if err != nil {
		return err
	}
	if err := c.writeJSONRPC(stdin, payload); err != nil {
		c.broadcast(Event{Channel: "transport_error", Params: map[string]any{"stream": "stdin", "method": "serverRequest/respond", "error": err.Error(), "stderr_tail": c.StderrTail()}})
		return err
	}
	return nil
}

func (c *Client) writeJSONRPC(stdin io.Writer, payload []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := stdin.Write(append(payload, '\n'))
	return err
}

func (c *Client) ThreadList(ctx context.Context, limit int, cursor string) (map[string]any, error) {
	params := map[string]any{"limit": limit, "sortKey": "updated_at"}
	if strings.TrimSpace(cursor) != "" {
		params["cursor"] = cursor
	}
	result, err := c.Request(ctx, "thread/list", params)
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) ThreadFork(ctx context.Context, threadID, cwd string) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/fork", threadForkParams(threadID, cwd))
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func threadForkParams(threadID, cwd string) map[string]any {
	params := map[string]any{"threadId": threadID}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = cwd
	}
	return params
}

func (c *Client) ThreadSetName(ctx context.Context, threadID, name string) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/name/set", map[string]any{
		"threadId": threadID,
		"name":     name,
	})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) ThreadArchive(ctx context.Context, threadID string) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/archive", map[string]any{"threadId": threadID})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) ThreadUnarchive(ctx context.Context, threadID string) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/unarchive", map[string]any{"threadId": threadID})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) ThreadCompactStart(ctx context.Context, threadID string) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/compact/start", map[string]any{"threadId": threadID})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) ThreadRollback(ctx context.Context, threadID string, numTurns int) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/rollback", map[string]any{
		"threadId": threadID,
		"numTurns": numTurns,
	})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) ThreadRead(ctx context.Context, threadID string, includeTurns bool) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/read", map[string]any{
		"threadId":     threadID,
		"includeTurns": includeTurns,
	})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

// ThreadReadLatest returns thread metadata plus only the newest turn. Sync uses
// this bounded read when it must reconcile after a reconnect or event gap.
func (c *Client) ThreadReadLatest(ctx context.Context, threadID string) (map[string]any, error) {
	return c.threadReadLatest(ctx, threadID, "full")
}

// ThreadReadLatestSummary is the cheap safety-reconciliation view. It avoids
// transferring persisted command output for already observed idle turns.
func (c *Client) ThreadReadLatestSummary(ctx context.Context, threadID string) (map[string]any, error) {
	return c.threadReadLatest(ctx, threadID, "summary")
}

func (c *Client) threadReadLatest(ctx context.Context, threadID, itemsView string) (map[string]any, error) {
	metadata, err := c.ThreadRead(ctx, threadID, false)
	if err != nil {
		return nil, err
	}
	result, err := c.Request(ctx, "thread/turns/list", map[string]any{
		"threadId":      threadID,
		"limit":         1,
		"sortDirection": "desc",
		"itemsView":     itemsView,
	})
	if err != nil {
		return nil, fmt.Errorf("read latest turn: %w", err)
	}
	thread := asMap(metadata["thread"])
	if len(thread) == 0 {
		thread = metadata
	}
	turnPage := asMap(result)
	turns, _ := turnPage["data"].([]any)
	if turns == nil {
		turns, _ = turnPage["turns"].([]any)
	}
	composed := make(map[string]any, len(thread)+1)
	for key, value := range thread {
		composed[key] = value
	}
	if len(turns) > 1 {
		turns = turns[:1]
	}
	composed["turns"] = turns
	return map[string]any{"thread": composed}, nil
}

func (c *Client) ThreadResume(ctx context.Context, threadID, _ string) (map[string]any, error) {
	result, err := c.Request(ctx, "thread/resume", threadResumeParams(threadID))
	if err != nil {
		result, err = c.Request(ctx, "thread/resume", map[string]any{"threadId": threadID})
		if err != nil {
			return nil, err
		}
	}
	return asMap(result), nil
}

func threadResumeParams(threadID string) map[string]any {
	return map[string]any{
		"threadId":     threadID,
		"excludeTurns": true,
		"initialTurnsPage": map[string]any{
			"limit":         1,
			"sortDirection": "desc",
			"itemsView":     "summary",
		},
	}
}

func (c *Client) TurnStart(ctx context.Context, threadID, message, cwd string, options TurnStartOptions) (map[string]any, error) {
	resolved, err := c.resolveTurnStartOptions(ctx, options)
	if err != nil {
		return nil, err
	}
	params, err := turnStartParams(threadID, message, cwd, resolved)
	if err != nil {
		return nil, err
	}
	result, err := c.Request(ctx, "turn/start", params)
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func turnStartParams(threadID, message, cwd string, options TurnStartOptions) (map[string]any, error) {
	params := map[string]any{
		"threadId": threadID,
		"input": []map[string]any{
			{"type": "text", "text": message, "text_elements": []any{}},
		},
	}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = cwd
	}
	if err := addPermissionParams(params, options.ApprovalPolicy, options.ApprovalsReviewer, options.SandboxMode, true); err != nil {
		return nil, err
	}
	mode := normalizeCollaborationMode(options.CollaborationMode)
	if mode != "" {
		model := strings.TrimSpace(options.Model)
		if model == "" {
			return nil, fmt.Errorf("codex model is required for collaboration mode %q", mode)
		}
		settings := map[string]any{
			"model":                  model,
			"reasoning_effort":       normalizeReasoningEffort(options.ReasoningEffort),
			"developer_instructions": nil,
		}
		if settings["reasoning_effort"] == "" {
			settings["reasoning_effort"] = nil
		}
		params["collaborationMode"] = map[string]any{
			"mode":     mode,
			"settings": settings,
		}
	}
	return params, nil
}

func (c *Client) resolveTurnStartOptions(ctx context.Context, options TurnStartOptions) (TurnStartOptions, error) {
	options.CollaborationMode = normalizeCollaborationMode(options.CollaborationMode)
	options.Model = strings.TrimSpace(options.Model)
	options.ReasoningEffort = normalizeReasoningEffort(options.ReasoningEffort)
	if options.CollaborationMode == "" {
		return options, nil
	}
	if options.Model == "" {
		model, err := c.defaultModel(ctx)
		if err != nil {
			return options, fmt.Errorf("codex model is required for collaboration mode %q; select a supported model or fix model/list: %w", options.CollaborationMode, err)
		}
		options.Model = model
	}
	if options.ReasoningEffort == "" {
		if effort, err := c.collaborationModeReasoningEffort(ctx, options.CollaborationMode); err == nil {
			options.ReasoningEffort = effort
		}
	}
	return options, nil
}

func (c *Client) defaultModel(ctx context.Context) (string, error) {
	models, err := c.ModelList(ctx, false)
	if err != nil {
		return "", err
	}
	first := ""
	for _, model := range models {
		if model.ID == "" {
			continue
		}
		if first == "" {
			first = model.ID
		}
		if model.IsDefault {
			return model.ID, nil
		}
	}
	if first != "" {
		return first, nil
	}
	return "", errors.New("model/list returned no models")
}

func (c *Client) collaborationModeReasoningEffort(ctx context.Context, mode string) (string, error) {
	modes, err := c.CollaborationModeList(ctx)
	if err != nil {
		return "", err
	}
	for _, preset := range modes {
		if normalizeCollaborationMode(preset.Mode) != mode {
			continue
		}
		return normalizeReasoningEffort(preset.ReasoningEffort), nil
	}
	return "", nil
}

func (c *Client) ModelList(ctx context.Context, includeHidden bool) ([]ModelOption, error) {
	params := map[string]any{"limit": 50}
	if includeHidden {
		params["includeHidden"] = true
	}
	result, err := c.Request(ctx, "model/list", params)
	if err != nil {
		return nil, err
	}
	return modelOptionsFromResult(result), nil
}

func (c *Client) CollaborationModeList(ctx context.Context) ([]CollaborationModeOption, error) {
	result, err := c.Request(ctx, "collaborationMode/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return collaborationModeOptionsFromResult(result), nil
}

func (c *Client) SkillsList(ctx context.Context, cwds []string, forceReload bool) (map[string]any, error) {
	result, err := c.Request(ctx, "skills/list", skillsListParams(cwds, forceReload))
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func skillsListParams(cwds []string, forceReload bool) map[string]any {
	params := map[string]any{}
	if len(cwds) > 0 {
		params["cwds"] = cwds
	}
	if forceReload {
		params["forceReload"] = true
	}
	return params
}

func (c *Client) PluginSkillRead(ctx context.Context, remoteMarketplaceName, remotePluginID, skillName string) (map[string]any, error) {
	result, err := c.Request(ctx, "plugin/skill/read", map[string]any{
		"remoteMarketplaceName": remoteMarketplaceName,
		"remotePluginId":        remotePluginID,
		"skillName":             skillName,
	})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) HooksList(ctx context.Context, cwds []string) (map[string]any, error) {
	params := map[string]any{}
	if len(cwds) > 0 {
		params["cwds"] = cwds
	}
	result, err := c.Request(ctx, "hooks/list", params)
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) MCPServerStatusList(ctx context.Context, limit int, cursor string, detail bool) (map[string]any, error) {
	params := map[string]any{}
	if limit > 0 {
		params["limit"] = limit
	}
	if strings.TrimSpace(cursor) != "" {
		params["cursor"] = cursor
	}
	if detail {
		params["detail"] = true
	}
	result, err := c.Request(ctx, "mcpServerStatus/list", params)
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) AppList(ctx context.Context, limit int, cursor, threadID string, forceRefetch bool) (map[string]any, error) {
	params := map[string]any{}
	if limit > 0 {
		params["limit"] = limit
	}
	if strings.TrimSpace(cursor) != "" {
		params["cursor"] = cursor
	}
	if strings.TrimSpace(threadID) != "" {
		params["threadId"] = threadID
	}
	if forceRefetch {
		params["forceRefetch"] = true
	}
	result, err := c.Request(ctx, "app/list", params)
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) ConfigRead(ctx context.Context, cwd string, includeLayers bool) (map[string]any, error) {
	params := map[string]any{}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = cwd
	}
	if includeLayers {
		params["includeLayers"] = true
	}
	result, err := c.Request(ctx, "config/read", params)
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func modelOptionsFromResult(result any) []ModelOption {
	data, _ := asMap(result)["data"].([]any)
	out := make([]ModelOption, 0, len(data))
	for _, item := range data {
		model := asMap(item)
		id := strings.TrimSpace(stringValue(model["model"], stringValue(model["id"], "")))
		if id == "" {
			continue
		}
		out = append(out, ModelOption{
			ID:                       id,
			DisplayName:              strings.TrimSpace(stringValue(model["displayName"], "")),
			Description:              strings.TrimSpace(stringValue(model["description"], "")),
			DefaultReasoningEffort:   normalizeReasoningEffort(stringValue(model["defaultReasoningEffort"], "")),
			SupportedReasoningEffort: supportedReasoningEfforts(model["supportedReasoningEfforts"]),
			IsDefault:                boolValue(model["isDefault"]),
			Hidden:                   boolValue(model["hidden"]),
		})
	}
	return out
}

func collaborationModeOptionsFromResult(result any) []CollaborationModeOption {
	data, _ := asMap(result)["data"].([]any)
	out := make([]CollaborationModeOption, 0, len(data))
	for _, item := range data {
		preset := asMap(item)
		out = append(out, CollaborationModeOption{
			Name:            strings.TrimSpace(stringValue(preset["name"], "")),
			Mode:            normalizeCollaborationMode(stringValue(preset["mode"], "")),
			Model:           strings.TrimSpace(stringValue(preset["model"], "")),
			ReasoningEffort: normalizeReasoningEffort(firstStringValue(preset["reasoning_effort"], preset["reasoningEffort"])),
		})
	}
	return out
}

func supportedReasoningEfforts(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		option := asMap(item)
		effort := normalizeReasoningEffort(firstStringValue(option["reasoning_effort"], option["reasoningEffort"], item))
		if effort == "" {
			continue
		}
		if _, ok := seen[effort]; ok {
			continue
		}
		seen[effort] = struct{}{}
		out = append(out, effort)
	}
	return out
}

func normalizeCollaborationMode(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "plan", "plan_mode", "plan-mode":
		return "plan"
	case "default":
		return "default"
	default:
		return ""
	}
}

func normalizeReasoningEffort(value string) string {
	normalized := strings.TrimSpace(strings.ToLower(value))
	switch normalized {
	case "":
		return ""
	case "x-high", "x_high", "extra-high", "extra_high":
		return "xhigh"
	default:
		return normalized
	}
}

func firstStringValue(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(stringValue(value, "")); text != "" {
			return text
		}
	}
	return ""
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(typed)
		return parsed
	default:
		return false
	}
}

func (c *Client) ThreadStart(ctx context.Context, cwd string, options ThreadStartOptions) (map[string]any, error) {
	params, err := threadStartParams(cwd, options)
	if err != nil {
		return nil, err
	}
	result, err := c.Request(ctx, "thread/start", params)
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func threadStartParams(cwd string, options ThreadStartOptions) (map[string]any, error) {
	params := map[string]any{
		"experimentalRawEvents":  false,
		"persistExtendedHistory": true,
	}
	if strings.TrimSpace(cwd) != "" {
		params["cwd"] = cwd
	}
	if err := addPermissionParams(params, options.ApprovalPolicy, options.ApprovalsReviewer, options.SandboxMode, false); err != nil {
		return nil, err
	}
	return params, nil
}

func addPermissionParams(params map[string]any, approvalPolicy, approvalsReviewer, sandboxMode string, turn bool) error {
	if value, err := appServerApprovalPolicy(approvalPolicy); err != nil {
		return err
	} else if value != "" {
		params["approvalPolicy"] = value
	}
	if value, err := appServerApprovalsReviewer(approvalsReviewer); err != nil {
		return err
	} else if value != "" {
		params["approvalsReviewer"] = value
	}
	if value, err := appServerSandboxMode(sandboxMode, turn); err != nil {
		return err
	} else if value != "" {
		if turn {
			params["sandboxPolicy"] = map[string]any{"type": value}
		} else {
			params["sandbox"] = value
		}
	}
	return nil
}

func appServerApprovalsReviewer(value string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", "_")) {
	case "":
		return "", nil
	case "user":
		return "user", nil
	case "auto_review", "autoreview":
		return "auto_review", nil
	default:
		return "", fmt.Errorf("unsupported approvals reviewer %q", value)
	}
}

func appServerApprovalPolicy(value string) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-")) {
	case "":
		return "", nil
	case "never":
		return "never", nil
	case "on-request", "onrequest":
		return "on-request", nil
	case "untrusted", "unless-trusted", "unlesstrusted":
		return "untrusted", nil
	default:
		return "", fmt.Errorf("unsupported approval policy %q", value)
	}
}

func appServerSandboxMode(value string, turn bool) (string, error) {
	switch strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "_", "-")) {
	case "":
		return "", nil
	case "read-only", "readonly":
		if !turn {
			return "read-only", nil
		}
		return "readOnly", nil
	case "workspace-write", "workspacewrite":
		if !turn {
			return "workspace-write", nil
		}
		return "workspaceWrite", nil
	case "danger-full-access", "dangerfullaccess":
		if !turn {
			return "danger-full-access", nil
		}
		return "dangerFullAccess", nil
	default:
		return "", fmt.Errorf("unsupported sandbox mode %q", value)
	}
}

func (c *Client) TurnInterrupt(ctx context.Context, threadID, turnID string) error {
	_, err := c.Request(ctx, "turn/interrupt", map[string]any{
		"threadId": threadID,
		"turnId":   turnID,
	})
	return err
}

func (c *Client) TurnSteer(ctx context.Context, threadID, turnID, message string) (map[string]any, error) {
	result, err := c.Request(ctx, "turn/steer", map[string]any{
		"threadId":       threadID,
		"expectedTurnId": turnID,
		"input": []map[string]any{
			{
				"type":          "text",
				"text":          message,
				"text_elements": []any{},
			},
		},
	})
	if err != nil {
		return nil, err
	}
	return asMap(result), nil
}

func (c *Client) StderrTail() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.stderrLines))
	copy(out, c.stderrLines)
	return out
}

func (c *Client) buildCommand() (*exec.Cmd, error) {
	if c.transport.Mode != TransportSpawned {
		_, err := c.appServerArgs()
		return nil, err
	}
	executable, err := exec.LookPath(c.codexBin)
	if err != nil {
		executable = c.codexBin
	}
	args, err := c.appServerArgs()
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		ext := strings.ToLower(filepath.Ext(executable))
		if ext == ".cmd" || ext == ".bat" {
			command := strings.Join(append([]string{executable}, args...), " ")
			cmd := exec.Command(os.Getenv("ComSpec"), "/d", "/c", command)
			cmd.Dir = c.cwd
			return cmd, nil
		}
	}
	cmd := exec.Command(executable, args...)
	cmd.Dir = c.cwd
	return cmd, nil
}

func (c *Client) openTransport(ctx context.Context) (*exec.Cmd, io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	switch c.transport.Mode {
	case TransportDaemon:
		stdin, stdout, err := c.openDaemonWebSocket(ctx)
		return nil, stdin, stdout, nil, err
	case TransportWebSocket:
		stdin, stdout, err := c.openLoopbackWebSocket(ctx)
		return nil, stdin, stdout, nil, err
	case TransportSpawned:
		// Continue below and start the configured child process.
	default:
		return nil, nil, nil, nil, fmt.Errorf("unsupported app-server transport mode %q", c.transport.Mode)
	}
	cmd, err := c.buildCommand()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, nil, nil, nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, nil, nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, nil, nil, nil, err
	}
	return cmd, stdin, stdout, stderr, nil
}

func (c *Client) openDaemonWebSocket(ctx context.Context) (io.WriteCloser, io.ReadCloser, error) {
	socketPath, err := c.daemonSocketPath()
	if err != nil {
		return nil, nil, err
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: c.requestTimeout,
		NetDialContext: func(dialContext context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(dialContext, "unix", socketPath)
		},
	}
	conn, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("connect app-server daemon socket %q: %w", socketPath, err)
	}
	return websocketStreams(conn)
}

func (c *Client) openLoopbackWebSocket(ctx context.Context) (io.WriteCloser, io.ReadCloser, error) {
	endpoint, err := loopbackWebSocketEndpoint(c.transport.ListenURL)
	if err != nil {
		return nil, nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: c.requestTimeout}
	conn, response, err := dialer.DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, nil, fmt.Errorf("connect shared app-server WebSocket %q: %w", endpoint, err)
	}
	return websocketStreams(conn)
}

func loopbackWebSocketEndpoint(raw string) (string, error) {
	endpoint := strings.TrimSpace(raw)
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse app-server WebSocket URL %q: %w", endpoint, err)
	}
	if !strings.EqualFold(parsed.Scheme, "ws") {
		return "", errors.New("app-server WebSocket URL must use ws:// for loopback transport")
	}
	host := strings.TrimSpace(parsed.Hostname())
	if host == "" {
		return "", errors.New("app-server WebSocket URL requires a host")
	}
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return "", fmt.Errorf("app-server plaintext WebSocket host %q must be loopback", host)
	}
	return parsed.String(), nil
}

func websocketStreams(conn *websocket.Conn) (io.WriteCloser, io.ReadCloser, error) {
	connection := &websocketConnection{conn: conn}
	return &websocketWriteCloser{connection: connection}, &websocketReadCloser{connection: connection}, nil
}

func (c *Client) daemonSocketPath() (string, error) {
	if socketPath := strings.TrimSpace(c.transport.SocketPath); socketPath != "" {
		return socketPath, nil
	}
	codexHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if codexHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home for app-server daemon socket: %w", err)
		}
		codexHome = filepath.Join(home, ".codex")
	}
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock"), nil
}

func (c *Client) appServerArgs() ([]string, error) {
	switch c.transport.Mode {
	case TransportSpawned:
		return []string{"app-server", "--listen", c.transport.ListenURL}, nil
	case TransportDaemon:
		return nil, errors.New("managed daemon uses a direct Unix WebSocket connection")
	case TransportWebSocket:
		return nil, errors.New("shared app-server uses a direct loopback WebSocket connection")
	default:
		return nil, fmt.Errorf("unsupported app-server transport mode %q", c.transport.Mode)
	}
}

func (c *Client) readStdout(generation uint64) {
	c.mu.Lock()
	stdout := c.stdout
	readerDone := c.readerDone
	c.mu.Unlock()
	defer close(readerDone)
	if stdout == nil {
		return
	}
	reader := bufio.NewReaderSize(stdout, 64*1024)
	for {
		line, err := reader.ReadBytes('\n')
		if len(bytes.TrimSpace(line)) > 0 {
			if !c.isStartedGeneration(generation) {
				return
			}
			var payload map[string]any
			if decodeErr := json.Unmarshal(bytes.TrimSpace(line), &payload); decodeErr != nil {
				c.broadcast(Event{Channel: "transport_error", Params: map[string]any{"stream": "stdout", "generation": generation, "error": decodeErr.Error(), "line_len": len(line), "stderr_tail": c.StderrTail()}})
			} else {
				c.handlePayload(payload, generation)
			}
		}
		if err == nil {
			continue
		}
		if !c.isStartedGeneration(generation) {
			return
		}
		if !errors.Is(err, io.EOF) {
			c.broadcast(Event{Channel: "transport_error", Params: map[string]any{"stream": "stdout", "generation": generation, "error": err.Error(), "stderr_tail": c.StderrTail()}})
			return
		}
		c.broadcast(Event{Channel: "transport_closed", Params: map[string]any{"stream": "stdout", "generation": generation, "reason": "eof", "stderr_tail": c.StderrTail()}})
		return
	}
}

func (c *Client) isStartedGeneration(generation uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started && c.generation == generation
}

func (c *Client) readStderr(generation uint64) {
	c.mu.Lock()
	stderr := c.stderr
	stderrDone := c.stderrDone
	c.mu.Unlock()
	defer close(stderrDone)
	if stderr == nil {
		return
	}
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 512), 64*1024)
	for scanner.Scan() {
		if !c.isStartedGeneration(generation) {
			return
		}
		line := scanner.Text()
		c.mu.Lock()
		c.stderrLines = append(c.stderrLines, line)
		if len(c.stderrLines) > 100 {
			c.stderrLines = c.stderrLines[len(c.stderrLines)-100:]
		}
		c.mu.Unlock()
	}
}

func (c *Client) handlePayload(payload map[string]any, generation uint64) {
	if id, ok := payload["id"]; ok {
		if _, hasResult := payload["result"]; hasResult || payload["error"] != nil {
			responseID := uint64FromAny(id)
			c.mu.Lock()
			if !c.started || c.generation != generation {
				c.mu.Unlock()
				return
			}
			reply := c.pending[responseID]
			delete(c.pending, responseID)
			c.mu.Unlock()
			if reply != nil {
				if payload["error"] != nil {
					reply <- rpcResponse{Error: fmt.Errorf("%v", payload["error"])}
				} else {
					reply <- rpcResponse{Result: payload["result"]}
				}
				close(reply)
			}
			return
		}
		if method, ok := payload["method"].(string); ok {
			requestID := rpcString(id)
			if requestID == "" {
				return
			}
			params := asMap(payload["params"])
			c.mu.Lock()
			if !c.started || c.generation != generation {
				c.mu.Unlock()
				return
			}
			c.serverRequests[requestID] = params
			c.mu.Unlock()
			c.broadcast(Event{Channel: "server_request", Method: method, Params: params, ID: id})
			return
		}
	}
	method, _ := payload["method"].(string)
	params := asMap(payload["params"])
	if strings.EqualFold(method, "serverRequest/resolved") {
		if requestID := rpcString(params["requestId"]); requestID != "" {
			c.mu.Lock()
			if !c.started || c.generation != generation {
				c.mu.Unlock()
				return
			}
			delete(c.serverRequests, requestID)
			c.mu.Unlock()
		}
	}
	if !c.isStartedGeneration(generation) {
		return
	}
	c.broadcast(Event{Channel: "notification", Method: method, Params: params})
}

func rpcString(value any) string {
	if value == nil {
		return ""
	}
	out := strings.TrimSpace(fmt.Sprintf("%v", value))
	if out == "" || out == "<nil>" {
		return ""
	}
	return out
}

func (c *Client) broadcast(event Event) {
	c.mu.Lock()
	subs := append([]chan Event(nil), c.subscribers...)
	c.mu.Unlock()
	for _, subscriber := range subs {
		select {
		case subscriber <- event:
		default:
			// Never hide a lossy subscription. Replace one stale queued event
			// with an explicit gap marker so consumers can reconcile once.
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- Event{Channel: "event_gap"}:
			default:
			}
		}
	}
}

func asMap(value any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func uint64FromAny(value any) uint64 {
	switch typed := value.(type) {
	case float64:
		return uint64(typed)
	case int:
		return uint64(typed)
	case int64:
		return uint64(typed)
	case uint64:
		return typed
	default:
		return 0
	}
}
