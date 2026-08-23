package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Paths struct {
	Home    string
	DataDir string
	LogDir  string
	DBPath  string
}

func DefaultPaths() Paths {
	return defaultPaths(envSource{lookup: os.LookupEnv}.get("CTR_GO_HOME"))
}

func defaultPaths(home string) Paths {
	if strings.TrimSpace(home) == "" {
		userHome, _ := os.UserHomeDir()
		home = filepath.Join(userHome, ".codex-tg")
	}
	return Paths{
		Home:    home,
		DataDir: filepath.Join(home, "data"),
		LogDir:  filepath.Join(home, "logs"),
		DBPath:  filepath.Join(home, "data", "state.sqlite"),
	}
}

func (p Paths) Ensure() error {
	for _, dir := range []string{p.Home, p.DataDir, p.LogDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}

type Config struct {
	Paths                       Paths
	CodexBin                    string
	AppServerMode               string
	AppServerListen             string
	AppServerSocket             string
	ControlAPIListen            string
	YMessengerEnabled           bool
	YMessengerRobotLogin        string
	YMessengerOAuthTeamToken    string
	YMessengerAllowedSenders    []string
	YMessengerPollInterval      time.Duration
	YMessengerRequireApproval   bool
	ExternalRequestsTopicID     int64
	ExternalRequestDefaultCWD   string
	ExternalApprovalPolicy      string
	ExternalSandboxMode         string
	TelegramBotToken            string
	AllowedUserIDs              []int64
	AllowedChatIDs              []int64
	AFCGroupID                  int64
	AFCInitialTopicLimit        int
	DefaultCWD                  string
	CodexChatsRoot              string
	PanelMode                   string
	LogEnabled                  bool
	DiagnosticLogs              bool
	NotifyNewRun                bool
	ObserverPollInterval        time.Duration
	RequestTimeout              time.Duration
	IndexRefreshInterval        time.Duration
	AttachRefreshInterval       time.Duration
	DeliveryRetryBase           time.Duration
	DeliveryMaxAttempts         int
	ProjectsProjectPreviewLimit int
	ProjectsChatPreviewLimit    int
	ChatsPageSize               int
}

const DefaultAFCInitialTopicLimit = 5

var runtimeEnvPassthroughKeys = []string{
	"HTTP_PROXY",
	"HTTPS_PROXY",
	"ALL_PROXY",
	"NO_PROXY",
	"http_proxy",
	"https_proxy",
	"all_proxy",
	"no_proxy",
	"NODE_USE_ENV_PROXY",
}

func RuntimeEnvPassthroughKeys() []string {
	return append([]string(nil), runtimeEnvPassthroughKeys...)
}

func Load() (Config, error) {
	values, err := LoadEnvFile(ConfigFilePath())
	if err != nil {
		return Config{}, err
	}
	applyRuntimeEnv(values)
	return fromSource(envSource{lookup: os.LookupEnv, file: values}), nil
}

func applyRuntimeEnv(values map[string]string) {
	for _, key := range runtimeEnvPassthroughKeys {
		value := strings.TrimSpace(values[key])
		if value == "" {
			continue
		}
		if existing, ok := os.LookupEnv(key); ok && strings.TrimSpace(existing) != "" {
			continue
		}
		_ = os.Setenv(key, value)
	}
}

func FromEnv() Config {
	cfg, err := Load()
	if err == nil {
		return cfg
	}
	return fromSource(envSource{lookup: os.LookupEnv})
}

func fromSource(source envSource) Config {
	paths := defaultPaths(source.get("CTR_GO_HOME"))
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	defaultCWD := source.path("CTR_GO_DEFAULT_CWD", cwd)
	codexBin := source.get("CTR_GO_CODEX_BIN")
	if codexBin == "" {
		codexBin = "codex"
	}
	listen := source.get("CTR_GO_APP_SERVER_LISTEN")
	if listen == "" {
		listen = "stdio://"
	}
	appServerMode := strings.ToLower(strings.TrimSpace(source.get("CTR_GO_APP_SERVER_MODE")))
	if appServerMode == "" {
		appServerMode = "spawned"
	}
	return Config{
		Paths:                       paths,
		CodexBin:                    codexBin,
		AppServerMode:               appServerMode,
		AppServerListen:             listen,
		AppServerSocket:             source.get("CTR_GO_APP_SERVER_SOCKET"),
		ControlAPIListen:            source.get("CTR_GO_CONTROL_API_LISTEN"),
		YMessengerEnabled:           source.bool("CTR_GO_YMESSENGER_ENABLED", false),
		YMessengerRobotLogin:        source.get("CTR_GO_YMESSENGER_ROBOT_LOGIN"),
		YMessengerOAuthTeamToken:    source.get("CTR_GO_YMESSENGER_OAUTH_TEAM_TOKEN"),
		YMessengerAllowedSenders:    parseStringList(source.get("CTR_GO_YMESSENGER_ALLOWED_SENDERS")),
		YMessengerPollInterval:      source.durationSeconds("CTR_GO_YMESSENGER_POLL_SECONDS", 2*time.Second),
		YMessengerRequireApproval:   source.bool("CTR_GO_YMESSENGER_REQUIRE_APPROVAL", true),
		ExternalRequestsTopicID:     parseInt64(source.get("CTR_GO_EXTERNAL_REQUESTS_TOPIC_ID")),
		ExternalRequestDefaultCWD:   source.path("CTR_GO_EXTERNAL_REQUEST_DEFAULT_CWD", defaultCWD),
		ExternalApprovalPolicy:      strings.TrimSpace(source.get("CTR_GO_EXTERNAL_REQUEST_APPROVAL_POLICY")),
		ExternalSandboxMode:         strings.TrimSpace(source.get("CTR_GO_EXTERNAL_REQUEST_SANDBOX_MODE")),
		TelegramBotToken:            source.first("CTR_GO_TELEGRAM_BOT_TOKEN", "CTR_TELEGRAM_BOT_TOKEN"),
		AllowedUserIDs:              parseInt64List(source.first("CTR_GO_ALLOWED_USER_IDS", "CTR_ALLOWED_USER_IDS")),
		AllowedChatIDs:              parseInt64List(source.first("CTR_GO_ALLOWED_CHAT_IDS", "CTR_ALLOWED_CHAT_IDS")),
		AFCGroupID:                  parseInt64(source.get("CTR_GO_AFC_GROUP_ID")),
		AFCInitialTopicLimit:        source.positiveInt("CTR_GO_AFC_INITIAL_TOPIC_LIMIT", DefaultAFCInitialTopicLimit),
		DefaultCWD:                  defaultCWD,
		CodexChatsRoot:              source.path("CTR_GO_CODEX_CHATS_ROOT", DefaultCodexChatsRoot()),
		PanelMode:                   normalizePanelMode(source.string("CTR_GO_PANEL_MODE", "per_run")),
		LogEnabled:                  source.bool("CTR_GO_LOG_ENABLED", true),
		DiagnosticLogs:              source.bool("CTR_GO_DIAGNOSTIC_LOGS", true),
		NotifyNewRun:                source.bool("CTR_GO_NOTIFY_NEW_RUN", true),
		ObserverPollInterval:        source.durationSeconds("CTR_GO_OBSERVER_POLL_SECONDS", 5*time.Second),
		RequestTimeout:              source.durationSeconds("CTR_GO_REQUEST_TIMEOUT_SECONDS", 30*time.Second),
		IndexRefreshInterval:        source.durationSeconds("CTR_GO_INDEX_REFRESH_SECONDS", 45*time.Second),
		AttachRefreshInterval:       source.durationSeconds("CTR_GO_ATTACH_REFRESH_SECONDS", 20*time.Second),
		DeliveryRetryBase:           source.durationSeconds("CTR_GO_DELIVERY_RETRY_SECONDS", 5*time.Second),
		DeliveryMaxAttempts:         source.int("CTR_GO_DELIVERY_MAX_ATTEMPTS", 5),
		ProjectsProjectPreviewLimit: source.positiveInt("CTR_GO_PROJECTS_PROJECT_PREVIEW_LIMIT", 7),
		ProjectsChatPreviewLimit:    source.positiveInt("CTR_GO_PROJECTS_CHAT_PREVIEW_LIMIT", 3),
		ChatsPageSize:               source.positiveInt("CTR_GO_CHATS_PAGE_SIZE", 8),
	}
}

func (c Config) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Home                        string  `json:"home"`
		DBPath                      string  `json:"db_path"`
		CodexBin                    string  `json:"codex_bin"`
		AppServerMode               string  `json:"app_server_mode"`
		AppServerListen             string  `json:"app_server_listen"`
		AppServerSocket             string  `json:"app_server_socket,omitempty"`
		ControlAPIListen            string  `json:"control_api_listen,omitempty"`
		YMessengerEnabled           bool    `json:"ymessenger_enabled"`
		YMessengerConfigured        bool    `json:"ymessenger_configured"`
		YMessengerPollSeconds       float64 `json:"ymessenger_poll_seconds"`
		YMessengerRequireApproval   bool    `json:"ymessenger_require_approval"`
		ExternalRequestsTopicID     int64   `json:"external_requests_topic_id,omitempty"`
		ExternalRequestDefaultCWD   string  `json:"external_request_default_cwd,omitempty"`
		ExternalApprovalPolicy      string  `json:"external_request_approval_policy,omitempty"`
		ExternalSandboxMode         string  `json:"external_request_sandbox_mode,omitempty"`
		HasTelegramToken            bool    `json:"telegram_configured"`
		AllowedUserIDs              []int64 `json:"allowed_user_ids"`
		AllowedChatIDs              []int64 `json:"allowed_chat_ids"`
		AFCGroupID                  int64   `json:"afc_group_id,omitempty"`
		AFCInitialTopicLimit        int     `json:"afc_initial_topic_limit"`
		DefaultCWD                  string  `json:"default_cwd"`
		CodexChatsRoot              string  `json:"codex_chats_root"`
		PanelMode                   string  `json:"panel_mode"`
		LogEnabled                  bool    `json:"log_enabled"`
		DiagnosticLogs              bool    `json:"diagnostic_logs"`
		NotifyNewRun                bool    `json:"notify_new_run"`
		ObserverPollSeconds         float64 `json:"observer_poll_seconds"`
		RequestTimeoutSeconds       float64 `json:"request_timeout_seconds"`
		ProjectsProjectPreviewLimit int     `json:"projects_project_preview_limit"`
		ProjectsChatPreviewLimit    int     `json:"projects_chat_preview_limit"`
		ChatsPageSize               int     `json:"chats_page_size"`
		GoOS                        string  `json:"goos"`
		GoArch                      string  `json:"goarch"`
	}{
		Home:                        c.Paths.Home,
		DBPath:                      c.Paths.DBPath,
		CodexBin:                    c.CodexBin,
		AppServerMode:               c.AppServerMode,
		AppServerListen:             c.AppServerListen,
		AppServerSocket:             c.AppServerSocket,
		ControlAPIListen:            c.ControlAPIListen,
		YMessengerEnabled:           c.YMessengerEnabled,
		YMessengerConfigured:        strings.TrimSpace(c.YMessengerOAuthTeamToken) != "",
		YMessengerPollSeconds:       c.YMessengerPollInterval.Seconds(),
		YMessengerRequireApproval:   c.YMessengerRequireApproval,
		ExternalRequestsTopicID:     c.ExternalRequestsTopicID,
		ExternalRequestDefaultCWD:   c.ExternalRequestDefaultCWD,
		ExternalApprovalPolicy:      c.ExternalApprovalPolicy,
		ExternalSandboxMode:         c.ExternalSandboxMode,
		HasTelegramToken:            c.TelegramBotToken != "",
		AllowedUserIDs:              c.AllowedUserIDs,
		AllowedChatIDs:              c.AllowedChatIDs,
		AFCGroupID:                  c.AFCGroupID,
		AFCInitialTopicLimit:        positiveOrDefault(c.AFCInitialTopicLimit, DefaultAFCInitialTopicLimit),
		DefaultCWD:                  c.DefaultCWD,
		CodexChatsRoot:              c.CodexChatsRoot,
		PanelMode:                   normalizePanelMode(c.PanelMode),
		LogEnabled:                  c.LogEnabled,
		DiagnosticLogs:              c.DiagnosticLogs,
		NotifyNewRun:                c.NotifyNewRun,
		ObserverPollSeconds:         c.ObserverPollInterval.Seconds(),
		RequestTimeoutSeconds:       c.RequestTimeout.Seconds(),
		ProjectsProjectPreviewLimit: positiveOrDefault(c.ProjectsProjectPreviewLimit, 7),
		ProjectsChatPreviewLimit:    positiveOrDefault(c.ProjectsChatPreviewLimit, 3),
		ChatsPageSize:               positiveOrDefault(c.ChatsPageSize, 8),
		GoOS:                        runtime.GOOS,
		GoArch:                      runtime.GOARCH,
	})
}

func (c Config) ValidateYMessenger() error {
	if !c.YMessengerEnabled {
		return nil
	}
	required := []struct {
		name  string
		value string
	}{
		{"CTR_GO_YMESSENGER_ROBOT_LOGIN", c.YMessengerRobotLogin},
		{"CTR_GO_YMESSENGER_OAUTH_TEAM_TOKEN", c.YMessengerOAuthTeamToken},
		{"CTR_GO_EXTERNAL_REQUEST_DEFAULT_CWD", c.ExternalRequestDefaultCWD},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required when YMessenger is enabled", field.name)
		}
	}
	if len(c.YMessengerAllowedSenders) == 0 {
		return fmt.Errorf("CTR_GO_YMESSENGER_ALLOWED_SENDERS is required when YMessenger is enabled")
	}
	if c.YMessengerPollInterval <= 0 {
		return fmt.Errorf("CTR_GO_YMESSENGER_POLL_SECONDS must be positive")
	}
	if c.AFCGroupID == 0 {
		return fmt.Errorf("CTR_GO_AFC_GROUP_ID is required when YMessenger is enabled")
	}
	if c.ExternalRequestsTopicID == 0 {
		return fmt.Errorf("CTR_GO_EXTERNAL_REQUESTS_TOPIC_ID is required when YMessenger is enabled")
	}
	if !oneOf(c.ExternalApprovalPolicy, "", "never", "on-request", "untrusted") {
		return fmt.Errorf("CTR_GO_EXTERNAL_REQUEST_APPROVAL_POLICY must be never, on-request, or untrusted")
	}
	if !oneOf(c.ExternalSandboxMode, "", "read-only", "workspace-write", "danger-full-access") {
		return fmt.Errorf("CTR_GO_EXTERNAL_REQUEST_SANDBOX_MODE must be read-only, workspace-write, or danger-full-access")
	}
	return nil
}

func oneOf(value string, allowed ...string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func DefaultCodexChatsRoot() string {
	userHome, _ := os.UserHomeDir()
	if strings.TrimSpace(userHome) == "" {
		return filepath.Join("Documents", "Codex")
	}
	return filepath.Join(userHome, "Documents", "Codex")
}

func ConfigFilePath() string {
	if value := strings.TrimSpace(os.Getenv("CTR_GO_CONFIG")); value != "" {
		return filepath.Clean(value)
	}
	return filepath.Join(DefaultPaths().Home, "config.env")
}

func LoadEnvFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseEnvFile(data, path)
}

func ParseEnvFile(data []byte, name string) (map[string]string, error) {
	values := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for i, rawLine := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", name, i+1)
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return nil, fmt.Errorf("%s:%d: invalid key %q", name, i+1, key)
		}
		parsed, err := parseEnvFileValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %w", name, i+1, err)
		}
		values[key] = parsed
	}
	return values, nil
}

func parseEnvFileValue(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strconv.Unquote(value)
	}
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], nil
	}
	return value, nil
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	first := key[0]
	if !(first == '_' || first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z') {
		return false
	}
	for i := 1; i < len(key); i++ {
		ch := key[i]
		if !(ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func positiveOrDefault(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

type envSource struct {
	lookup func(string) (string, bool)
	file   map[string]string
}

func (s envSource) get(key string) string {
	if s.lookup != nil {
		if value, ok := s.lookup(key); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if s.file != nil {
		if value, ok := s.file[key]; ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s envSource) first(keys ...string) string {
	for _, key := range keys {
		if value := s.get(key); value != "" {
			return value
		}
	}
	return ""
}

func (s envSource) string(key, fallback string) string {
	value := s.get(key)
	if value == "" {
		return fallback
	}
	return value
}

func (s envSource) path(key, fallback string) string {
	value := s.string(key, fallback)
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return filepath.Clean(value)
}

func (s envSource) int(key string, fallback int) int {
	value := s.get(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func (s envSource) positiveInt(key string, fallback int) int {
	return positiveOrDefault(s.int(key, fallback), fallback)
}

func (s envSource) bool(key string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(s.get(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "t", "yes", "y", "on", "enabled":
		return true
	case "0", "false", "f", "no", "n", "off", "disabled":
		return false
	default:
		return fallback
	}
}

func (s envSource) durationSeconds(key string, fallback time.Duration) time.Duration {
	value := s.get(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return time.Duration(parsed * float64(time.Second))
}

func parseInt64List(raw string) []int64 {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	out := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		value, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, value)
	}
	return out
}

func parseStringList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})
	seen := make(map[string]struct{}, len(parts))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.ToLower(strings.TrimSpace(part))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func parseInt64(raw string) int64 {
	value, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return value
}

func normalizePanelMode(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "stable":
		return "stable"
	default:
		return "per_run"
	}
}
