package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFromEnvReadsCodexChatsRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Codex")
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_CODEX_CHATS_ROOT", root)

	cfg := FromEnv()

	if cfg.CodexChatsRoot != root {
		t.Fatalf("CodexChatsRoot = %q, want %q", cfg.CodexChatsRoot, root)
	}
}

func TestFromEnvReadsAFCGroupID(t *testing.T) {
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_AFC_GROUP_ID", "-1001234567890")
	t.Setenv("CTR_GO_AFC_INITIAL_TOPIC_LIMIT", "9")

	cfg := FromEnv()

	if cfg.AFCGroupID != -1001234567890 {
		t.Fatalf("AFCGroupID = %d, want -1001234567890", cfg.AFCGroupID)
	}
	if cfg.AFCInitialTopicLimit != 9 {
		t.Fatalf("AFCInitialTopicLimit = %d, want 9", cfg.AFCInitialTopicLimit)
	}
}

func TestFromEnvDefaultsAFCInitialTopicLimitToFive(t *testing.T) {
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_AFC_INITIAL_TOPIC_LIMIT", "0")

	cfg := FromEnv()

	if cfg.AFCInitialTopicLimit != 5 {
		t.Fatalf("AFCInitialTopicLimit = %d, want 5", cfg.AFCInitialTopicLimit)
	}
}

func TestFromEnvReadsYMessengerLaunchRequestConfig(t *testing.T) {
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_YMESSENGER_ENABLED", "true")
	t.Setenv("CTR_GO_YMESSENGER_ROBOT_LOGIN", "robot-example")
	t.Setenv("CTR_GO_YMESSENGER_OAUTH_TEAM_TOKEN", "private-oauth-token")
	t.Setenv("CTR_GO_YMESSENGER_ALLOWED_SENDERS", "alice,bob")
	t.Setenv("CTR_GO_YMESSENGER_POLL_SECONDS", "2.5")
	t.Setenv("CTR_GO_YMESSENGER_REQUIRE_APPROVAL", "false")
	t.Setenv("CTR_GO_EXTERNAL_REQUESTS_TOPIC_ID", "77")
	t.Setenv("CTR_GO_EXTERNAL_REQUEST_DEFAULT_CWD", filepath.Join(t.TempDir(), "project"))
	t.Setenv("CTR_GO_EXTERNAL_REQUEST_APPROVAL_POLICY", "never")
	t.Setenv("CTR_GO_EXTERNAL_REQUEST_APPROVALS_REVIEWER", "auto_review")
	t.Setenv("CTR_GO_EXTERNAL_REQUEST_SANDBOX_MODE", "danger-full-access")

	cfg := FromEnv()

	if !cfg.YMessengerEnabled || cfg.YMessengerRobotLogin != "robot-example" || cfg.YMessengerOAuthTeamToken != "private-oauth-token" {
		t.Fatalf("YMessenger config = %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.YMessengerAllowedSenders, []string{"alice", "bob"}) {
		t.Fatalf("YMessengerAllowedSenders = %#v", cfg.YMessengerAllowedSenders)
	}
	if cfg.YMessengerPollInterval != 2500*time.Millisecond {
		t.Fatalf("YMessengerPollInterval = %s, want 2.5s", cfg.YMessengerPollInterval)
	}
	if cfg.YMessengerRequireApproval {
		t.Fatal("YMessengerRequireApproval = true, want false from env")
	}
	if cfg.ExternalRequestsTopicID != 77 || cfg.ExternalRequestDefaultCWD == "" {
		t.Fatalf("external request config = topic %d cwd %q", cfg.ExternalRequestsTopicID, cfg.ExternalRequestDefaultCWD)
	}
	if cfg.ExternalApprovalPolicy != "never" || cfg.ExternalApprovalsReviewer != "auto_review" || cfg.ExternalSandboxMode != "danger-full-access" {
		t.Fatalf("external request permissions = %q / %q / %q", cfg.ExternalApprovalPolicy, cfg.ExternalApprovalsReviewer, cfg.ExternalSandboxMode)
	}
}

func TestYMessengerApprovalDefaultsToRequired(t *testing.T) {
	t.Parallel()
	cfg := fromSource(envSource{lookup: func(string) (string, bool) { return "", false }})
	if !cfg.YMessengerRequireApproval {
		t.Fatal("YMessengerRequireApproval = false, want secure default true")
	}
}

func TestFromEnvReadsArcanumReviewConfig(t *testing.T) {
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_ARCANUM_REVIEW_ENABLED", "true")
	t.Setenv("CTR_GO_ARCANUM_REVIEW_LOGIN", "reviewer-example")
	t.Setenv("CTR_GO_ARCANUM_YA_BIN", "/usr/local/bin/ya")
	t.Setenv("CTR_GO_ARCANUM_REVIEW_POLL_SECONDS", "45")
	t.Setenv("CTR_GO_ARCANUM_REVIEW_CWD", filepath.Join(t.TempDir(), "projects"))
	t.Setenv("CTR_GO_ARCANUM_REVIEW_AUTO_START_AUTHORS", "ampir9999, @Plastinina-LS")

	cfg := FromEnv()

	if !cfg.ArcanumReviewEnabled || cfg.ArcanumReviewLogin != "reviewer-example" || cfg.ArcanumYABin != "/usr/local/bin/ya" {
		t.Fatalf("Arcanum review config = %#v", cfg)
	}
	if cfg.ArcanumReviewPollInterval != 45*time.Second || cfg.ArcanumReviewCWD == "" {
		t.Fatalf("Arcanum poll/cwd = %s / %q", cfg.ArcanumReviewPollInterval, cfg.ArcanumReviewCWD)
	}
	if got := strings.Join(cfg.ArcanumAutoStartAuthors, ","); got != "ampir9999,@plastinina-ls" {
		t.Fatalf("Arcanum auto-start authors = %q", got)
	}
}

func TestValidateArcanumReviewRequiresEnabledFields(t *testing.T) {
	t.Parallel()
	cfg := Config{ArcanumReviewEnabled: true}
	if err := cfg.ValidateArcanumReview(); err == nil {
		t.Fatal("ValidateArcanumReview succeeded with missing fields")
	}
	cfg.ArcanumReviewLogin = "reviewer-example"
	cfg.ArcanumYABin = "/usr/local/bin/ya"
	cfg.ArcanumReviewPollInterval = time.Minute
	cfg.ArcanumReviewCWD = t.TempDir()
	cfg.AFCGroupID = -10042
	cfg.ExternalRequestsTopicID = 77
	if err := cfg.ValidateArcanumReview(); err != nil {
		t.Fatalf("ValidateArcanumReview(valid) failed: %v", err)
	}
	cfg.ExternalApprovalPolicy = "sometimes"
	if err := cfg.ValidateArcanumReview(); err == nil {
		t.Fatal("ValidateArcanumReview accepted invalid external approval policy")
	}
}

func TestValidateYMessengerRequiresEnabledFields(t *testing.T) {
	cfg := Config{YMessengerEnabled: true}
	if err := cfg.ValidateYMessenger(); err == nil {
		t.Fatal("ValidateYMessenger succeeded with missing fields")
	}
	cfg.YMessengerRobotLogin = "robot-example"
	cfg.YMessengerOAuthTeamToken = "token"
	cfg.YMessengerAllowedSenders = []string{"alice"}
	cfg.YMessengerPollInterval = 2 * time.Second
	cfg.AFCGroupID = -10042
	cfg.ExternalRequestsTopicID = 77
	cfg.ExternalRequestDefaultCWD = t.TempDir()
	if err := cfg.ValidateYMessenger(); err != nil {
		t.Fatalf("ValidateYMessenger(valid) failed: %v", err)
	}
}

func TestValidateYMessengerRequiresVisibilityTopicWhenApprovalDisabled(t *testing.T) {
	t.Parallel()
	cfg := Config{
		YMessengerEnabled: true, YMessengerRobotLogin: "robot-example", YMessengerOAuthTeamToken: "token",
		YMessengerAllowedSenders: []string{"alice"}, YMessengerPollInterval: 2 * time.Second,
		YMessengerRequireApproval: false, AFCGroupID: -1001, ExternalRequestDefaultCWD: t.TempDir(),
	}
	if err := cfg.ValidateYMessenger(); err == nil {
		t.Fatal("ValidateYMessenger succeeded without required Telegram visibility topic")
	}
	cfg.ExternalRequestsTopicID = 77
	cfg.ExternalApprovalPolicy = "never"
	cfg.ExternalSandboxMode = "danger-full-access"
	if err := cfg.ValidateYMessenger(); err != nil {
		t.Fatalf("ValidateYMessenger(valid auto-start) failed: %v", err)
	}
}

func TestFromEnvReadsManagedDaemonTransport(t *testing.T) {
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_APP_SERVER_MODE", "daemon")
	t.Setenv("CTR_GO_APP_SERVER_SOCKET", filepath.Join(t.TempDir(), "app-server.sock"))

	cfg := FromEnv()

	if got, want := cfg.AppServerMode, "daemon"; got != want {
		t.Fatalf("AppServerMode = %q, want %q", got, want)
	}
	if got, want := cfg.AppServerSocket, os.Getenv("CTR_GO_APP_SERVER_SOCKET"); got != want {
		t.Fatalf("AppServerSocket = %q, want %q", got, want)
	}
}

func TestFromEnvReadsLoopbackWebSocketTransport(t *testing.T) {
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_APP_SERVER_MODE", "websocket")
	t.Setenv("CTR_GO_APP_SERVER_LISTEN", "ws://127.0.0.1:4500")

	cfg := FromEnv()

	if got, want := cfg.AppServerMode, "websocket"; got != want {
		t.Fatalf("AppServerMode = %q, want %q", got, want)
	}
	if got, want := cfg.AppServerListen, "ws://127.0.0.1:4500"; got != want {
		t.Fatalf("AppServerListen = %q, want %q", got, want)
	}
}

func TestFromEnvDefaultsToSpawnedAppServer(t *testing.T) {
	t.Setenv("CTR_GO_CONFIG", filepath.Join(t.TempDir(), "missing.env"))
	t.Setenv("CTR_GO_APP_SERVER_MODE", "")

	cfg := FromEnv()

	if got, want := cfg.AppServerMode, "spawned"; got != want {
		t.Fatalf("AppServerMode = %q, want %q", got, want)
	}
	if cfg.AppServerSocket != "" {
		t.Fatalf("AppServerSocket = %q, want empty default socket", cfg.AppServerSocket)
	}
}

func TestMarshalJSONIncludesPublicRuntimeConfig(t *testing.T) {
	t.Parallel()

	data, err := json.Marshal(Config{AppServerMode: "daemon", AppServerSocket: "/tmp/codex.sock", NotifyNewRun: true, ControlAPIListen: "127.0.0.1:8765", AFCGroupID: -100123, AFCInitialTopicLimit: 9})
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if got["notify_new_run"] != true {
		t.Fatalf("notify_new_run = %#v, want true", got["notify_new_run"])
	}
	if got["control_api_listen"] != "127.0.0.1:8765" {
		t.Fatalf("control_api_listen = %#v, want listen address", got["control_api_listen"])
	}
	if got["afc_group_id"] != float64(-100123) {
		t.Fatalf("afc_group_id = %#v, want -100123", got["afc_group_id"])
	}
	if got["afc_initial_topic_limit"] != float64(9) {
		t.Fatalf("afc_initial_topic_limit = %#v, want 9", got["afc_initial_topic_limit"])
	}
	if got["app_server_mode"] != "daemon" {
		t.Fatalf("app_server_mode = %#v, want daemon", got["app_server_mode"])
	}
	if got["app_server_socket"] != "/tmp/codex.sock" {
		t.Fatalf("app_server_socket = %#v, want configured socket", got["app_server_socket"])
	}
}

func TestMarshalJSONRedactsYMessengerToken(t *testing.T) {
	data, err := json.Marshal(Config{YMessengerEnabled: true, YMessengerOAuthTeamToken: "do-not-leak"})
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}
	if strings.Contains(string(data), "do-not-leak") || strings.Contains(string(data), "oauth_team_token") {
		t.Fatalf("config JSON leaked token: %s", data)
	}
}

func TestParseEnvFileSupportsCommentsAndQuotes(t *testing.T) {
	t.Parallel()

	values, err := ParseEnvFile([]byte(`
# comment
CTR_GO_TELEGRAM_BOT_TOKEN="token with spaces"
CTR_GO_ALLOWED_USER_IDS='123,456'
CTR_GO_NOTIFY_NEW_RUN=off
`), "test.env")
	if err != nil {
		t.Fatalf("ParseEnvFile failed: %v", err)
	}
	want := map[string]string{
		"CTR_GO_TELEGRAM_BOT_TOKEN": "token with spaces",
		"CTR_GO_ALLOWED_USER_IDS":   "123,456",
		"CTR_GO_NOTIFY_NEW_RUN":     "off",
	}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values = %#v, want %#v", values, want)
	}
}

func TestParseEnvFileRejectsInvalidLine(t *testing.T) {
	t.Parallel()

	_, err := ParseEnvFile([]byte("not-an-assignment\n"), "bad.env")
	if err == nil {
		t.Fatal("ParseEnvFile succeeded, want invalid line error")
	}
	if !strings.Contains(err.Error(), "expected KEY=VALUE") {
		t.Fatalf("error = %v, want KEY=VALUE message", err)
	}
}

func TestLoadReadsConfigFileAndEnvOverridesIt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.env")
	fileDefaultCWD := filepath.Join(dir, "from-file")
	envDefaultCWD := filepath.Join(dir, "from-env")
	home := filepath.Join(dir, "home")
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		`CTR_GO_HOME="` + home + `"`,
		`CTR_GO_TELEGRAM_BOT_TOKEN="file-token"`,
		`CTR_GO_ALLOWED_USER_IDS="101 202"`,
		`CTR_GO_DEFAULT_CWD="` + fileDefaultCWD + `"`,
		`CTR_GO_CONTROL_API_LISTEN="127.0.0.1:9876"`,
		`CTR_GO_NOTIFY_NEW_RUN="off"`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	t.Setenv("CTR_GO_CONFIG", configPath)
	t.Setenv("CTR_GO_DEFAULT_CWD", envDefaultCWD)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.TelegramBotToken != "file-token" {
		t.Fatalf("TelegramBotToken = %q, want file-token", cfg.TelegramBotToken)
	}
	if !reflect.DeepEqual(cfg.AllowedUserIDs, []int64{101, 202}) {
		t.Fatalf("AllowedUserIDs = %#v, want 101,202", cfg.AllowedUserIDs)
	}
	if cfg.DefaultCWD != envDefaultCWD {
		t.Fatalf("DefaultCWD = %q, want env override %q", cfg.DefaultCWD, envDefaultCWD)
	}
	if cfg.Paths.Home != home {
		t.Fatalf("Home = %q, want %q", cfg.Paths.Home, home)
	}
	if cfg.NotifyNewRun {
		t.Fatal("NotifyNewRun = true, want false from config file")
	}
	if cfg.ControlAPIListen != "127.0.0.1:9876" {
		t.Fatalf("ControlAPIListen = %q, want configured listen", cfg.ControlAPIListen)
	}
}

func TestLoadAppliesRuntimeProxyEnvFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.env")
	proxy := "http://127.0.0.1:18080"
	if err := os.WriteFile(configPath, []byte(strings.Join([]string{
		`CTR_GO_HOME="` + filepath.Join(dir, "home") + `"`,
		`HTTPS_PROXY="` + proxy + `"`,
		`NODE_USE_ENV_PROXY="1"`,
		"",
	}, "\n")), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	t.Setenv("CTR_GO_CONFIG", configPath)
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NODE_USE_ENV_PROXY", "")

	if _, err := Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := os.Getenv("HTTPS_PROXY"); got != proxy {
		t.Fatalf("HTTPS_PROXY = %q, want %q", got, proxy)
	}
	if got := os.Getenv("NODE_USE_ENV_PROXY"); got != "1" {
		t.Fatalf("NODE_USE_ENV_PROXY = %q, want 1", got)
	}
}

func TestLoadDoesNotOverrideExplicitRuntimeProxyEnv(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.env")
	if err := os.WriteFile(configPath, []byte(`HTTPS_PROXY="http://file-proxy"`+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	t.Setenv("CTR_GO_CONFIG", configPath)
	t.Setenv("HTTPS_PROXY", "http://env-proxy")

	if _, err := Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if got := os.Getenv("HTTPS_PROXY"); got != "http://env-proxy" {
		t.Fatalf("HTTPS_PROXY = %q, want env value", got)
	}
}

func TestConfigFilePathOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.env")
	t.Setenv("CTR_GO_CONFIG", path)

	if got := ConfigFilePath(); got != path {
		t.Fatalf("ConfigFilePath = %q, want %q", got, path)
	}
}
