package appserver

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestBuildCommandRejectsManagedDaemonTransport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command args are platform-specific")
	}
	client := NewClientWithTransport("codex", TransportConfig{
		Mode:       TransportDaemon,
		SocketPath: "/tmp/codex-app-server.sock",
	}, t.TempDir(), time.Second)

	_, err := client.buildCommand()
	if err == nil || !strings.Contains(err.Error(), "direct Unix WebSocket") {
		t.Fatalf("buildCommand error = %v, want direct Unix WebSocket message", err)
	}
}

func TestDaemonTransportConnectsDirectlyOverUnixWebSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix sockets are platform-specific")
	}
	root, err := os.MkdirTemp("/tmp", "codex-tg-ws-")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socketPath := filepath.Join(root, "app-server.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("Listen(unix) failed: %v", err)
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	messages := make(chan map[string]any, 2)
	serverErrors := make(chan error, 1)
	server := &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		conn, upgradeErr := upgrader.Upgrade(writer, request, nil)
		if upgradeErr != nil {
			serverErrors <- upgradeErr
			return
		}
		defer conn.Close()
		for index := 0; index < 2; index++ {
			var payload map[string]any
			if readErr := conn.ReadJSON(&payload); readErr != nil {
				serverErrors <- readErr
				return
			}
			messages <- payload
			if index == 0 {
				if writeErr := conn.WriteJSON(map[string]any{"id": payload["id"], "result": map[string]any{}}); writeErr != nil {
					serverErrors <- writeErr
					return
				}
			}
		}
	})}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && serveErr != http.ErrServerClosed {
			serverErrors <- serveErr
		}
	}()
	t.Cleanup(func() { _ = server.Close() })

	client := NewClientWithTransport("codex", TransportConfig{
		Mode:       TransportDaemon,
		SocketPath: socketPath,
	}, t.TempDir(), 2*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	client.mu.Lock()
	cmd := client.cmd
	client.mu.Unlock()
	if cmd != nil {
		t.Fatalf("daemon transport spawned process: %v", cmd.Args)
	}
	for _, wantMethod := range []string{"initialize", "initialized"} {
		select {
		case payload := <-messages:
			if got := payload["method"]; got != wantMethod {
				t.Fatalf("method = %v, want %q", got, wantMethod)
			}
			if _, present := payload["jsonrpc"]; present {
				t.Fatalf("wire message contains jsonrpc header: %#v", payload)
			}
		case serverErr := <-serverErrors:
			t.Fatalf("WebSocket server failed: %v", serverErr)
		case <-ctx.Done():
			t.Fatalf("waiting for %s: %v", wantMethod, ctx.Err())
		}
	}
}

func TestBuildCommandKeepsSpawnedListenTransport(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("command args are platform-specific")
	}
	client := NewClientWithTransport("codex", TransportConfig{
		Mode:      TransportSpawned,
		ListenURL: "stdio://",
	}, t.TempDir(), time.Second)

	cmd, err := client.buildCommand()
	if err != nil {
		t.Fatalf("buildCommand failed: %v", err)
	}
	want := []string{"app-server", "--listen", "stdio://"}
	if got := cmd.Args[1:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("command args = %#v, want %#v", got, want)
	}
}

func TestBuildCommandRejectsUnknownTransportMode(t *testing.T) {
	client := NewClientWithTransport("codex", TransportConfig{
		Mode: "unexpected",
	}, t.TempDir(), time.Second)

	_, err := client.buildCommand()
	if err == nil {
		t.Fatal("buildCommand succeeded, want unsupported transport error")
	}
	if !strings.Contains(err.Error(), "unsupported app-server transport mode") {
		t.Fatalf("error = %v, want unsupported transport message", err)
	}
}

func TestThreadResumeParamsContainOnlyThreadID(t *testing.T) {
	params := threadResumeParams("thread-1")
	want := map[string]any{"threadId": "thread-1"}
	if !reflect.DeepEqual(params, want) {
		t.Fatalf("threadResumeParams = %#v, want %#v", params, want)
	}
}

type overlapDetectingWriteCloser struct {
	mu      sync.Mutex
	active  int
	overlap bool
	lines   [][]byte
}

func (w *overlapDetectingWriteCloser) Write(data []byte) (int, error) {
	w.mu.Lock()
	w.active++
	if w.active > 1 {
		w.overlap = true
	}
	w.mu.Unlock()
	time.Sleep(time.Millisecond)
	w.mu.Lock()
	w.lines = append(w.lines, append([]byte(nil), data...))
	w.active--
	w.mu.Unlock()
	return len(data), nil
}

func (w *overlapDetectingWriteCloser) Close() error { return nil }

func TestRPCStringSkipsNilLikeValues(t *testing.T) {
	t.Parallel()

	for _, value := range []any{nil, "", " ", "<nil>"} {
		if got := rpcString(value); got != "" {
			t.Fatalf("rpcString(%#v) = %q, want empty", value, got)
		}
	}
	if got := rpcString(float64(42)); got != "42" {
		t.Fatalf("rpcString(42) = %q, want 42", got)
	}
}

func TestHandlePayloadIgnoresStaleGeneration(t *testing.T) {
	t.Parallel()

	client := NewClient("codex", "stdio", t.TempDir(), time.Second)
	events := client.Subscribe()
	reply := make(chan rpcResponse, 1)
	client.mu.Lock()
	client.started = true
	client.generation = 2
	client.pending[1] = reply
	client.mu.Unlock()

	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"result":  map[string]any{"ok": true},
	}, 1)
	select {
	case response := <-reply:
		t.Fatalf("stale generation resolved pending response: %#v", response)
	default:
	}
	client.mu.Lock()
	if _, ok := client.pending[1]; !ok {
		t.Fatal("stale generation deleted pending response")
	}
	client.mu.Unlock()

	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      "req-stale",
		"method":  "serverRequest/approval",
		"params":  map[string]any{"requestId": "req-stale"},
	}, 1)
	client.mu.Lock()
	_, stored := client.serverRequests["req-stale"]
	client.mu.Unlock()
	if stored {
		t.Fatal("stale generation stored server request")
	}
	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"method":  "thread/status/changed",
		"params":  map[string]any{"threadId": "thread-stale"},
	}, 1)
	select {
	case event := <-events:
		t.Fatalf("stale generation broadcast event: %#v", event)
	default:
	}

	client.handlePayload(map[string]any{
		"jsonrpc": "2.0",
		"id":      float64(1),
		"result":  map[string]any{"ok": true},
	}, 2)
	select {
	case response := <-reply:
		if response.Error != nil {
			t.Fatalf("current generation response error: %v", response.Error)
		}
	default:
		t.Fatal("current generation did not resolve pending response")
	}
}

func TestTurnStartParamsIncludesCollaborationMode(t *testing.T) {
	params, err := turnStartParams("thread-1", "Draft a plan", "/tmp/project", TurnStartOptions{
		CollaborationMode: "plan",
		Model:             "gpt-test",
		ReasoningEffort:   "x-high",
	})
	if err != nil {
		t.Fatalf("turnStartParams failed: %v", err)
	}
	if got, want := params["threadId"], "thread-1"; got != want {
		t.Fatalf("threadId = %v, want %q", got, want)
	}
	collaborationMode, ok := params["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("collaborationMode = %#v, want object", params["collaborationMode"])
	}
	if got, want := collaborationMode["mode"], "plan"; got != want {
		t.Fatalf("mode = %v, want %q", got, want)
	}
	settings, ok := collaborationMode["settings"].(map[string]any)
	if !ok {
		t.Fatalf("settings = %#v, want object", collaborationMode["settings"])
	}
	if got, want := settings["model"], "gpt-test"; got != want {
		t.Fatalf("model = %v, want %q", got, want)
	}
	if got, want := settings["reasoning_effort"], "xhigh"; got != want {
		t.Fatalf("reasoning_effort = %v, want %q", got, want)
	}
	if _, ok := settings["developer_instructions"]; !ok {
		t.Fatal("developer_instructions key is missing")
	}
}

func TestTurnStartParamsIncludesDefaultCollaborationMode(t *testing.T) {
	params, err := turnStartParams("thread-1", "Run it", "/tmp/project", TurnStartOptions{
		CollaborationMode: "default",
		Model:             "gpt-test",
	})
	if err != nil {
		t.Fatalf("turnStartParams failed: %v", err)
	}
	collaborationMode, ok := params["collaborationMode"].(map[string]any)
	if !ok {
		t.Fatalf("collaborationMode = %#v, want object", params["collaborationMode"])
	}
	if got, want := collaborationMode["mode"], "default"; got != want {
		t.Fatalf("mode = %v, want %q", got, want)
	}
}

func TestStartParamsIncludeExplicitPermissions(t *testing.T) {
	threadParams, err := threadStartParams("/tmp/project", ThreadStartOptions{
		ApprovalPolicy:    "never",
		ApprovalsReviewer: "auto_review",
		SandboxMode:       "danger-full-access",
	})
	if err != nil {
		t.Fatal(err)
	}
	if threadParams["approvalPolicy"] != "never" || threadParams["approvalsReviewer"] != "auto_review" || threadParams["sandbox"] != "danger-full-access" {
		t.Fatalf("thread permissions = %#v", threadParams)
	}

	turnParams, err := turnStartParams("thread-1", "Run it", "/tmp/project", TurnStartOptions{
		ApprovalPolicy:    "never",
		ApprovalsReviewer: "auto_review",
		SandboxMode:       "danger-full-access",
	})
	if err != nil {
		t.Fatal(err)
	}
	sandbox, ok := turnParams["sandboxPolicy"].(map[string]any)
	if turnParams["approvalPolicy"] != "never" || turnParams["approvalsReviewer"] != "auto_review" || !ok || sandbox["type"] != "dangerFullAccess" {
		t.Fatalf("turn permissions = %#v", turnParams)
	}
}

func TestStartParamsUseProtocolSpecificPermissionEnums(t *testing.T) {
	threadParams, err := threadStartParams("/tmp/project", ThreadStartOptions{
		ApprovalPolicy: "on-request",
		SandboxMode:    "workspace-write",
	})
	if err != nil {
		t.Fatal(err)
	}
	if threadParams["approvalPolicy"] != "on-request" || threadParams["sandbox"] != "workspace-write" {
		t.Fatalf("thread permissions = %#v", threadParams)
	}

	turnParams, err := turnStartParams("thread-1", "Run it", "/tmp/project", TurnStartOptions{
		ApprovalPolicy: "untrusted",
		SandboxMode:    "read-only",
	})
	if err != nil {
		t.Fatal(err)
	}
	sandbox, ok := turnParams["sandboxPolicy"].(map[string]any)
	if turnParams["approvalPolicy"] != "untrusted" || !ok || sandbox["type"] != "readOnly" {
		t.Fatalf("turn permissions = %#v", turnParams)
	}
}

func TestStartParamsRejectUnsupportedPermissions(t *testing.T) {
	if _, err := threadStartParams("", ThreadStartOptions{ApprovalPolicy: "sometimes"}); err == nil {
		t.Fatal("threadStartParams accepted unsupported approval policy")
	}
	if _, err := turnStartParams("thread-1", "Run it", "", TurnStartOptions{SandboxMode: "host"}); err == nil {
		t.Fatal("turnStartParams accepted unsupported sandbox mode")
	}
	if _, err := threadStartParams("", ThreadStartOptions{ApprovalsReviewer: "robot"}); err == nil {
		t.Fatal("threadStartParams accepted unsupported approvals reviewer")
	}
}

func TestTurnStartParamsRejectsModeWithoutModel(t *testing.T) {
	_, err := turnStartParams("thread-1", "Draft a plan", "", TurnStartOptions{CollaborationMode: "plan"})
	if err == nil {
		t.Fatal("turnStartParams succeeded, want missing model error")
	}
}

func TestControlPlaneThreadForkParams(t *testing.T) {
	params := threadForkParams("thread-1", "/tmp/project")
	if got, want := params["threadId"], "thread-1"; got != want {
		t.Fatalf("threadId = %v, want %q", got, want)
	}
	if got, want := params["cwd"], "/tmp/project"; got != want {
		t.Fatalf("cwd = %v, want %q", got, want)
	}

	params = threadForkParams("thread-1", "")
	if _, ok := params["cwd"]; ok {
		t.Fatalf("cwd should be omitted for empty cwd: %#v", params)
	}
}

func TestControlPlaneSkillsListParams(t *testing.T) {
	params := skillsListParams([]string{"/tmp/a", "/tmp/b"}, true)
	cwds, ok := params["cwds"].([]string)
	if !ok {
		t.Fatalf("cwds = %#v, want []string", params["cwds"])
	}
	if got, want := len(cwds), 2; got != want {
		t.Fatalf("cwds len = %d, want %d", got, want)
	}
	if got, want := params["forceReload"], true; got != want {
		t.Fatalf("forceReload = %v, want %v", got, want)
	}

	params = skillsListParams(nil, false)
	if len(params) != 0 {
		t.Fatalf("empty params = %#v, want empty", params)
	}
}

func TestStartConcurrentCallsShareInitializedProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server shell script is Unix-only")
	}
	root := t.TempDir()
	logPath := filepath.Join(root, "rpc.log")
	t.Setenv("CODEX_TG_FAKE_APPSERVER_LOG", logPath)
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
log="${CODEX_TG_FAKE_APPSERVER_LOG:-}"
if IFS= read -r line; then
  if [ -n "$log" ]; then printf '%s\n' "$line" >> "$log"; fi
  sleep 0.2
  printf '{"jsonrpc":"2.0","id":1,"result":{}}\n'
fi
if IFS= read -r line; then
  if [ -n "$log" ]; then printf '%s\n' "$line" >> "$log"; fi
fi
sleep 5
`)
	client := NewClient(script, "stdio", root, 5*time.Second)
	defer client.Close()

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs[index] = client.Start(ctx)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Start[%d] failed: %v", i, err)
		}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) failed: %v", logPath, err)
	}
	if got := strings.Count(string(data), `"method":"initialize"`); got != 1 {
		t.Fatalf("initialize requests = %d, want 1; log:\n%s", got, data)
	}
}

func TestStartCleansUpAfterInitializeFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake app-server shell script is Unix-only")
	}
	root := t.TempDir()
	script := writeFakeAppServer(t, root, `#!/bin/sh
set -eu
if IFS= read -r line; then
  printf '{"jsonrpc":"2.0","id":1,"error":{"message":"init failed"}}\n'
fi
sleep 5
`)
	client := NewClient(script, "stdio", root, 5*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := client.Start(ctx)
	if err == nil {
		t.Fatal("Start succeeded, want initialize failure")
	}

	client.mu.Lock()
	started := client.started
	cmd := client.cmd
	stdin := client.stdin
	pending := len(client.pending)
	client.mu.Unlock()
	if started || cmd != nil || stdin != nil || pending != 0 {
		t.Fatalf("client state after failed Start: started=%t cmd_nil=%t stdin_nil=%t pending=%d", started, cmd == nil, stdin == nil, pending)
	}
	if _, requestErr := client.Request(context.Background(), "thread/list", nil); requestErr == nil || !strings.Contains(requestErr.Error(), "not running") {
		t.Fatalf("Request after failed Start error = %v, want not running", requestErr)
	}
}

func TestClientSerializesConcurrentJSONRPCWrites(t *testing.T) {
	writer := &overlapDetectingWriteCloser{}
	client := NewClient("codex", "stdio", t.TempDir(), 5*time.Millisecond)
	client.mu.Lock()
	client.started = true
	client.stdin = writer
	client.mu.Unlock()
	t.Cleanup(func() { _ = client.Close() })

	var wg sync.WaitGroup
	for index := range 10 {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_ = client.Notify(context.Background(), "event/test", map[string]any{"index": index})
		}()
		go func() {
			defer wg.Done()
			_ = client.RespondServerRequest(context.Background(), "request-"+string(rune('a'+index)), map[string]any{"ok": true})
		}()
		go func() {
			defer wg.Done()
			_, _ = client.Request(context.Background(), "request/test", map[string]any{"index": index})
		}()
	}
	wg.Wait()

	writer.mu.Lock()
	defer writer.mu.Unlock()
	if writer.overlap {
		t.Fatal("concurrent JSON-RPC writes overlapped")
	}
	if got, want := len(writer.lines), 30; got != want {
		t.Fatalf("written lines = %d, want %d", got, want)
	}
	for _, line := range writer.lines {
		var payload map[string]any
		if err := json.Unmarshal(line, &payload); err != nil {
			t.Fatalf("invalid JSON-RPC line %q: %v", line, err)
		}
		if _, present := payload["jsonrpc"]; present {
			t.Fatalf("App Server wire message contains forbidden jsonrpc header: %s", line)
		}
	}
}

func writeFakeAppServer(t *testing.T, root, body string) string {
	t.Helper()
	path := filepath.Join(root, "fake-codex")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("WriteFile(fake app-server) failed: %v", err)
	}
	return path
}
