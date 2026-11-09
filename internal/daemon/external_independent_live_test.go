package daemon

// Opt-in, isolated store and ephemeral Codex chat. Never consumes getUpdates or
// modifies the working daemon. Only its own card and topics are cleaned up.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mideco-tech/codex-tg/internal/appserver"
	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/model"
)

type probeTelegram struct {
	*fakeSyncForum
	*recordingSender
	token     string
	group     int64
	mu        sync.Mutex
	lastWrite time.Time
	created   []int64
	card      int64
	buttons   [][]model.ButtonSpec
}

func (p *probeTelegram) call(ctx context.Context, method string, data map[string]any, result any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if wait := time.Until(p.lastWrite.Add(time.Second)); wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.lastWrite = time.Now()
	body, _ := json.Marshal(data)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.telegram.org/bot"+p.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errors.New("invalid Telegram probe request")
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 25 * time.Second}).Do(req)
	if err != nil {
		return errors.New("Telegram probe transport failed")
	}
	defer response.Body.Close()
	var envelope struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
	}
	if err = json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return errors.New("invalid Telegram probe response")
	}
	if !envelope.OK {
		return errors.New(envelope.Description)
	}
	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}
	return nil
}
func probeButtons(buttons [][]model.ButtonSpec) map[string]any {
	rows := [][]map[string]string{}
	for _, row := range buttons {
		buttons := []map[string]string{}
		for _, b := range row {
			buttons = append(buttons, map[string]string{"text": b.Text, "callback_data": b.CallbackData})
		}
		rows = append(rows, buttons)
	}
	return map[string]any{"inline_keyboard": rows}
}
func (p *probeTelegram) SendMessage(ctx context.Context, chat, topic int64, text string, buttons [][]model.ButtonSpec, options model.SendOptions) (int64, error) {
	var r struct {
		ID int64 `json:"message_id"`
	}
	err := p.call(ctx, "sendMessage", map[string]any{"chat_id": chat, "message_thread_id": topic, "text": text, "reply_markup": probeButtons(buttons), "disable_notification": true}, &r)
	if err == nil && len(buttons) > 0 {
		p.card = r.ID
		p.buttons = buttons
	}
	return r.ID, err
}
func (p *probeTelegram) EditMessage(ctx context.Context, chat, topic, id int64, text string, buttons [][]model.ButtonSpec) error {
	err := p.call(ctx, "editMessageText", map[string]any{"chat_id": chat, "message_id": id, "text": text, "reply_markup": probeButtons(buttons)}, nil)
	if err == nil && id == p.card {
		p.buttons = buttons
	}
	return err
}
func (p *probeTelegram) CreateSyncTopic(ctx context.Context, title string) (int64, error) {
	return p.CreateLaunchTopic(ctx, title)
}
func (p *probeTelegram) CreateLaunchTopic(ctx context.Context, title string) (int64, error) {
	if err := CheckSyncTopicCreation(ctx); err != nil {
		return 0, err
	}
	var r struct {
		ID int64 `json:"message_thread_id"`
	}
	if err := p.call(ctx, "createForumTopic", map[string]any{"chat_id": p.group, "name": title}, &r); err != nil {
		return 0, NewSyncForumFailure(SyncForumFailureRetryable, err)
	}
	p.created = append(p.created, r.ID)
	if len(p.created) == 1 {
		return 0, NewSyncForumFailure(SyncForumFailureUnknown, errors.New("probe intentionally lost successful creation response"))
	}
	return r.ID, nil
}
func (p *probeTelegram) SendSyncMessage(ctx context.Context, topic int64, m model.RenderedMessage, o model.SendOptions) (int64, error) {
	return p.SendMessage(ctx, p.group, topic, m.Text, nil, o)
}
func (p *probeTelegram) EditSyncMessage(ctx context.Context, topic, id int64, m model.RenderedMessage, o model.SendOptions) error {
	return p.EditMessage(ctx, p.group, topic, id, m.Text, nil)
}
func (p *probeTelegram) RenameSyncTopic(ctx context.Context, topic int64, title string) error {
	return p.call(ctx, "editForumTopic", map[string]any{"chat_id": p.group, "message_thread_id": topic, "name": title}, nil)
}
func (p *probeTelegram) DeleteSyncTopic(ctx context.Context, topic int64) error {
	return p.call(ctx, "deleteForumTopic", map[string]any{"chat_id": p.group, "message_thread_id": topic}, nil)
}

type probeEphemeralSession struct {
	*appserver.Client
	starts, turns int
}

func (p *probeEphemeralSession) ThreadStart(ctx context.Context, cwd string, o appserver.ThreadStartOptions) (map[string]any, error) {
	p.starts++
	r, err := p.Request(ctx, "thread/start", map[string]any{"cwd": cwd, "ephemeral": true, "approvalPolicy": o.ApprovalPolicy, "approvalsReviewer": o.ApprovalsReviewer, "sandbox": o.SandboxMode, "persistExtendedHistory": true})
	if err != nil {
		return nil, err
	}
	m, ok := r.(map[string]any)
	if !ok {
		return nil, errors.New("invalid live thread result")
	}
	return m, nil
}
func (p *probeEphemeralSession) TurnStart(ctx context.Context, thread, prompt, cwd string, o appserver.TurnStartOptions) (map[string]any, error) {
	p.turns++
	return p.Client.TurnStart(ctx, thread, prompt, cwd, o)
}

func TestLiveExternalLaunchTelegramRetry(t *testing.T) {
	if os.Getenv("CODEX_TG_EXTERNAL_LIVE") != "1" {
		t.Skip("opt-in: real ephemeral Codex turn and private Telegram probe card/topics")
	}
	readback := os.Getenv("CODEX_TG_PROBE_READBACK")
	if readback == "" {
		t.Fatal("CODEX_TG_PROBE_READBACK must name an authenticated Telegram readback script")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal("cannot load live probe configuration")
	}
	if !appserver.IsSharedTransportMode(cfg.AppServerMode) || cfg.TelegramBotToken == "" || cfg.SyncGroupID == 0 || cfg.ExternalRequestsTopicID == 0 {
		t.Fatal("configured shared App Server and private Sync forum required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	s := newTestService(t)
	s.cfg.SyncGroupID = cfg.SyncGroupID
	s.cfg.AppServerMode = cfg.AppServerMode
	if err = s.store.BeginSyncActivation(ctx, "live-probe", cfg.SyncGroupID); err != nil {
		t.Fatal(err)
	}
	if err = s.store.FinishSyncActivation(ctx, "live-probe", "{}", true); err != nil {
		t.Fatal(err)
	}
	wire := appserver.NewClientWithTransport(cfg.CodexBin, appserver.TransportConfig{Mode: appserver.TransportMode(cfg.AppServerMode), ListenURL: cfg.AppServerListen, SocketPath: cfg.AppServerSocket}, t.TempDir(), 30*time.Second)
	writer := &probeEphemeralSession{Client: wire}
	s.liveFactory = func() Session { return writer }
	s.poll = writer
	s.pollConnected = true
	tg := &probeTelegram{fakeSyncForum: &fakeSyncForum{}, recordingSender: &recordingSender{}, token: cfg.TelegramBotToken, group: cfg.SyncGroupID}
	s.SetSyncForum(tg)
	s.SetSender(tg)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		for _, id := range tg.created {
			if err := tg.DeleteSyncTopic(cleanup, id); err != nil {
				t.Errorf("probe topic cleanup: %s", sanitizeDiagnosticString(err.Error()))
			}
		}
		if tg.card > 0 {
			if err := tg.call(cleanup, "deleteMessage", map[string]any{"chat_id": tg.group, "message_id": tg.card}, nil); err != nil {
				t.Errorf("probe card cleanup: %s", sanitizeDiagnosticString(err.Error()))
			}
		}
	}()
	r := daemonExternalRequest("live-probe:"+randomToken(), "Reply exactly EXTERNAL_LAUNCH_PROBE_OK. Do not use tools or edit files.")
	r.Title = "External launch recovery probe"
	r.CWD = t.TempDir()
	r.TelegramTopicID = cfg.ExternalRequestsTopicID
	policy, reviewer, sandbox := "on-request", "auto_review", "workspace-write"
	r.ApprovalPolicy = &policy
	r.ApprovalsReviewer = &reviewer
	r.SandboxMode = &sandbox
	if _, err = s.EnqueueExternalRequests(ctx, "test", []model.ExternalLaunchRequest{r}); err != nil {
		t.Fatal(err)
	}
	s.processExternalLaunchRequests(ctx)
	if ok, err := s.store.ClaimExternalLaunchRequest(ctx, r.ID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	events := wire.Subscribe()
	s.dispatchExternalLaunchRequest(ctx, r.ID)
	stored, _ := s.store.GetExternalLaunchRequest(ctx, r.ID)
	if stored.Status != model.ExternalLaunchSessionStarted || len(tg.created) != 0 || writer.starts != 1 || writer.turns != 1 {
		t.Fatalf("live launch not independently confirmed: status=%s", stored.Status)
	}
	defer func() {
		if stored.ThreadID != "" {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_ = wire.TurnInterrupt(cleanup, stored.ThreadID, stored.TurnID)
			_ = wire.ThreadUnsubscribe(cleanup, stored.ThreadID)
		}
	}()
	// Ephemeral chats do not expose history. Read real completed items and turn
	// status from this connection's native events, plus thread/read metadata.
	items := []any{}
	var completed map[string]any
	starts := 0
	for completed == nil {
		select {
		case <-ctx.Done():
			t.Fatal("live turn did not finish")
		case event := <-events:
			if threadIDFromEvent(event) != stored.ThreadID {
				continue
			}
			switch event.Method {
			case "turn/started":
				starts++
			case "item/completed":
				if item, ok := event.Params["item"].(map[string]any); ok {
					items = append(items, item)
				}
			case "turn/completed":
				completed, _ = event.Params["turn"].(map[string]any)
			}
		}
	}
	if starts != 1 || completed["id"] != stored.TurnID {
		t.Fatal("live authority returned a different turn")
	}
	payload, err := wire.ThreadRead(ctx, stored.ThreadID, false)
	if err != nil {
		t.Fatal(sanitizeDiagnosticString(err.Error()))
	}
	metadata, ok := payload["thread"].(map[string]any)
	if !ok || metadata["id"] != stored.ThreadID {
		t.Fatal("live thread metadata mismatch")
	}
	completed["items"] = items
	metadata["turns"] = []any{completed}
	snapshot := appserver.SnapshotFromThreadRead(payload)
	if snapshot.LatestFinalText != "EXTERNAL_LAUNCH_PROBE_OK" {
		t.Fatalf("unexpected live final: %q", snapshot.LatestFinalText)
	}
	s.processBoardSnapshotLocked(ctx, snapshot)
	s.runSyncDelivery(ctx, stored.ThreadID) // Real create succeeds, but its response is hidden from the daemon.
	s.processExternalLaunchRequests(ctx)
	token := callbackTokenForButton(tg.buttons, "Retry")
	if token == "" {
		t.Fatal("live card has no Telegram Retry")
	}
	route, err := s.store.GetCallbackRoute(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.handleExternalLaunchCallback(ctx, cfg.SyncGroupID, r.TelegramTopicID, tg.card, route); err != nil {
		t.Fatal(err)
	}
	s.wg.Wait()
	s.runSyncDelivery(ctx, stored.ThreadID)
	s.processExternalLaunchRequests(ctx)
	p, err := s.store.ThreadTelegramProjection(ctx, "live-probe", stored.ThreadID)
	if err != nil || p.State != "connected" || writer.starts != 1 || writer.turns != 1 || len(tg.created) != 2 {
		t.Fatal("live recovery did not preserve one execution", p.State, err)
	}
	output, err := exec.CommandContext(ctx, "python3", readback, strconv.FormatInt(tg.group, 10), strconv.FormatInt(tg.card, 10), strconv.FormatInt(p.TopicID, 10)).CombinedOutput()
	if err != nil {
		t.Fatalf("Telegram readback failed: %s", sanitizeDiagnosticString(string(output)))
	}
	t.Log("Real App Server confirmed one turn; real Telegram Retry recovered its completed result after lost create response; authenticated readback passed.")
}
