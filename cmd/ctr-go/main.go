package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/controlapi"
	"github.com/mideco-tech/codex-tg/internal/cronpoller"
	"github.com/mideco-tech/codex-tg/internal/daemon"
	"github.com/mideco-tech/codex-tg/internal/telegram"
	"github.com/mideco-tech/codex-tg/internal/version"
	"github.com/mideco-tech/codex-tg/internal/ymessenger"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.SetOutput(os.Stderr)
		log.Printf("ctr-go: %s", telegram.SanitizeLogError(err))
		os.Exit(1)
	}
}

func run(args []string) error {
	return runWithIO(args, os.Stdin, os.Stdout)
}

func runWithIO(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 {
		printUsage(out)
		return nil
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], in, out)
	case "service":
		return runService(args[1:], in, out)
	case "daemon":
		if len(args) < 2 || args[1] != "run" {
			return errors.New("usage: ctr-go daemon run")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runDaemon(cfg)
	case "status":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runStatus(cfg, out)
	case "doctor":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runDoctor(cfg, out)
	case "repair":
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return runRepair(cfg, out)
	case "version":
		_, _ = fmt.Fprintf(out, "ctr-go v%s\n", version.Version)
		return nil
	case "help", "--help", "-h":
		printUsage(out)
		return nil
	default:
		return fmt.Errorf("unknown command: %s", strings.Join(args, " "))
	}
}

func runDaemon(cfg config.Config) error {
	if strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return errors.New("CTR_GO_TELEGRAM_BOT_TOKEN or CTR_TELEGRAM_BOT_TOKEN must be set")
	}
	if err := cfg.ValidateTelegramSurface(); err != nil {
		return err
	}
	if err := cfg.ValidateYMessenger(); err != nil {
		return err
	}
	if err := cfg.ValidateArcanumReview(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger := daemonLogger(cfg)
	service, err := daemon.New(cfg)
	if err != nil {
		return err
	}
	defer service.Close()
	service.SetLogger(diagnosticLogger(cfg, logger))
	if cfg.YMessengerEnabled {
		service.SetExternalReplySender(ymessenger.NewClient(cfg.YMessengerOAuthTeamToken))
	}

	bot, err := telegram.NewBot(cfg, service, logger)
	if err != nil {
		return err
	}
	service.SetSender(bot)
	if err := bot.Start(ctx); err != nil {
		return err
	}
	if err := service.Start(ctx); err != nil {
		return err
	}
	if err := startControlAPI(ctx, cfg, service, logger); err != nil {
		return err
	}
	for _, poller := range []struct {
		source   string
		interval time.Duration
		enabled  bool
	}{
		{source: ymessenger.Source, interval: cfg.YMessengerPollInterval, enabled: cfg.YMessengerEnabled},
		{source: arcanumreview.Source, interval: cfg.ArcanumReviewPollInterval, enabled: cfg.ArcanumReviewEnabled},
		{source: arcanumreview.ChangesSource, interval: cfg.ArcanumReviewPollInterval, enabled: cfg.ArcanumReviewEnabled},
		{source: cronpoller.Source, interval: cronpoller.DefaultPollInterval, enabled: true},
	} {
		if err := service.RegisterExternalPoller(ctx, poller.source, poller.interval, poller.enabled); err != nil {
			return fmt.Errorf("register %s poller: %w", poller.source, err)
		}
	}
	startYMessengerPoller(ctx, cfg, service, logger)
	startArcanumReviewPoller(ctx, cfg, service, logger)
	startArcanumChangesPoller(ctx, cfg, service, logger)
	startCronPoller(ctx, cfg, service, logger)
	service.FinishStartup(ctx)
	logger.Printf("ctr-go daemon running with %s", bot.String())
	return bot.Run(ctx)
}

func startCronPoller(ctx context.Context, cfg config.Config, sink cronpoller.RequestSink, logger *log.Logger) {
	poller := cronpoller.New(filepath.Join(cfg.Paths.Home, "cron.json"), cfg.ExternalRequestsTopicID, sink)
	lastError := ""
	go poller.Run(ctx, cronpoller.DefaultPollInterval, func(err error) {
		if err == nil {
			lastError = ""
			return
		}
		currentError := telegram.SanitizeLogError(err)
		if currentError == lastError {
			return
		}
		lastError = currentError
		if logger != nil {
			logger.Printf("cron launch request poll failed: %s", currentError)
		}
	})
}

func startYMessengerPoller(ctx context.Context, cfg config.Config, sink ymessenger.RequestSink, logger *log.Logger) bool {
	if !cfg.YMessengerEnabled {
		return false
	}
	client := ymessenger.NewClient(cfg.YMessengerOAuthTeamToken)
	poller := ymessenger.NewPoller(client, sink, ymessenger.FilterConfig{
		RobotLogin:      cfg.YMessengerRobotLogin,
		AllowedSenders:  cfg.YMessengerAllowedSenders,
		DefaultCWD:      cfg.ExternalRequestDefaultCWD,
		TelegramTopicID: cfg.ExternalRequestsTopicID,
		RequireApproval: cfg.YMessengerRequireApproval,
	})
	go poller.Run(ctx, cfg.YMessengerPollInterval, func(err error) {
		if logger != nil {
			logger.Printf("YMessenger poll failed: %s", telegram.SanitizeLogError(err))
		}
	})
	if logger != nil {
		logger.Printf("YMessenger launch requests enabled (poll interval %s)", cfg.YMessengerPollInterval)
	}
	return true
}

func startArcanumReviewPoller(ctx context.Context, cfg config.Config, sink arcanumreview.RequestSink, logger *log.Logger) bool {
	if !cfg.ArcanumReviewEnabled {
		return false
	}
	client := arcanumreview.NewCLIClient(cfg.ArcanumYABin, nil)
	poller := arcanumreview.NewPoller(client, sink, arcanumreview.Config{
		Login: cfg.ArcanumReviewLogin, CWD: cfg.ArcanumReviewCWD, TelegramTopicID: cfg.ExternalRequestsTopicID,
		AutoStartAuthors: cfg.ArcanumAutoStartAuthors,
	})
	go poller.Run(ctx, cfg.ArcanumReviewPollInterval, func(err error) {
		if logger != nil {
			logger.Printf("Arcanum review poll failed: %s", telegram.SanitizeLogError(err))
		}
	})
	if logger != nil {
		logger.Printf("Arcanum review requests enabled for %s (poll interval %s)", cfg.ArcanumReviewLogin, cfg.ArcanumReviewPollInterval)
	}
	return true
}

func startArcanumChangesPoller(ctx context.Context, cfg config.Config, sink arcanumreview.RequestSink, logger *log.Logger) bool {
	if !cfg.ArcanumReviewEnabled {
		return false
	}
	client := arcanumreview.NewCLIClient(cfg.ArcanumYABin, nil)
	poller := arcanumreview.NewChangesPoller(client, sink, arcanumreview.Config{
		Login: cfg.ArcanumReviewLogin, CWD: cfg.ArcanumReviewCWD, TelegramTopicID: cfg.ExternalRequestsTopicID,
	})
	go poller.Run(ctx, cfg.ArcanumReviewPollInterval, func(err error) {
		if logger != nil {
			logger.Printf("Arcanum changes poll failed: %s", telegram.SanitizeLogError(err))
		}
	})
	if logger != nil {
		logger.Printf("Arcanum changes requests enabled for %s (poll interval %s)", cfg.ArcanumReviewLogin, cfg.ArcanumReviewPollInterval)
	}
	return true
}

func startControlAPI(ctx context.Context, cfg config.Config, service *daemon.Service, logger *log.Logger) error {
	listener, err := controlapi.Listen(cfg.ControlAPIListen)
	if err != nil {
		return err
	}
	if listener == nil {
		return nil
	}
	logger.Printf("ctr-go control api listening on %s", listener.Addr().String())
	go func() {
		if err := controlapi.Serve(ctx, listener, controlapi.NewHandler(version.Version, service), logger); err != nil && ctx.Err() == nil {
			logger.Printf("ctr-go control api stopped: %v", err)
		}
	}()
	return nil
}

func daemonLogger(cfg config.Config) *log.Logger {
	return log.New(daemonLogOutput(cfg), "", log.LstdFlags)
}

func daemonLogOutput(cfg config.Config) io.Writer {
	if !cfg.LogEnabled {
		return io.Discard
	}
	return os.Stdout
}

func diagnosticLogger(cfg config.Config, logger *log.Logger) *log.Logger {
	if !cfg.LogEnabled || !cfg.DiagnosticLogs {
		return nil
	}
	return logger
}

func runStatus(cfg config.Config, out io.Writer) error {
	service, err := daemon.New(cfg)
	if err != nil {
		return err
	}
	defer service.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	doctor, err := service.Doctor(ctx)
	if err != nil {
		return err
	}
	state := normalizeStringMap(doctor["daemon_state"])
	backlog := normalizeInt64(doctor["delivery_backlog"])

	lines := []string{
		"ctr-go status",
		fmt.Sprintf("Home: %s", cfg.Paths.Home),
		fmt.Sprintf("DB: %s", cfg.Paths.DBPath),
		fmt.Sprintf("Codex bin: %s", cfg.CodexBin),
		fmt.Sprintf("Control API: %s", formatOptional(cfg.ControlAPIListen, "off")),
		fmt.Sprintf("Telegram configured: %t", strings.TrimSpace(cfg.TelegramBotToken) != ""),
		fmt.Sprintf("YMessenger launch requests: %s", map[bool]string{true: "enabled", false: "off"}[cfg.YMessengerEnabled]),
		fmt.Sprintf("YMessenger approval required: %t", cfg.YMessengerRequireApproval),
		fmt.Sprintf("Allowed users: %s", formatIDs(cfg.AllowedUserIDs)),
		fmt.Sprintf("Sync group: %d", cfg.SyncGroupID),
		fmt.Sprintf("Default cwd: %s", cfg.DefaultCWD),
		fmt.Sprintf("Codex Chats root: %s", cfg.CodexChatsRoot),
		fmt.Sprintf("Delivery backlog: %d", backlog),
		"",
		"Persisted daemon state:",
	}
	if len(state) == 0 {
		lines = append(lines, "(empty)")
	} else {
		keys := make([]string, 0, len(state))
		for key := range state {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			lines = append(lines, fmt.Sprintf("%s = %s", key, state[key]))
		}
	}
	_, _ = fmt.Fprintln(out, strings.Join(lines, "\n"))
	return nil
}

func formatOptional(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func runDoctor(cfg config.Config, out io.Writer) error {
	service, err := daemon.New(cfg)
	if err != nil {
		return err
	}
	defer service.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	doctor, err := service.Doctor(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(doctor, "", "  ")
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, string(encoded))
	return nil
}

func runRepair(cfg config.Config, out io.Writer) error {
	service, err := daemon.New(cfg)
	if err != nil {
		return err
	}
	defer service.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := service.RequestRepair(ctx, "cli"); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(out, "Repair requested.")
	return nil
}

func runInit(args []string, in io.Reader, out io.Writer) error {
	force := false
	for _, arg := range args {
		switch arg {
		case "--force":
			force = true
		case "help", "--help", "-h":
			printInitUsage(out)
			return nil
		default:
			return fmt.Errorf("usage: ctr-go init [--force]")
		}
	}

	path := config.ConfigFilePath()
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("%s already exists; rerun with --force to overwrite it", path)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	codexBin := "codex"
	if found, err := exec.LookPath("codex"); err == nil && strings.TrimSpace(found) != "" {
		codexBin = found
	}

	reader := bufio.NewReader(in)
	_, _ = fmt.Fprintln(out, "This writes a local codex-tg config file. Keep it private.")
	token, err := promptRequired(reader, out, "Telegram bot token")
	if err != nil {
		return err
	}
	allowedUsers, err := promptRequired(reader, out, "Allowed Telegram user id")
	if err != nil {
		return err
	}
	if err := validateTelegramUserID(allowedUsers); err != nil {
		return fmt.Errorf("Allowed Telegram user id: %w", err)
	}
	syncGroupID, err := promptRequired(reader, out, "Sync forum group id")
	if err != nil {
		return err
	}
	if err := validateSyncGroupID(syncGroupID); err != nil {
		return fmt.Errorf("Sync forum group id: %w", err)
	}
	defaultCWD, err := prompt(reader, out, "Default cwd", cwd)
	if err != nil {
		return err
	}
	chatsRoot, err := prompt(reader, out, "Codex Chats root", config.DefaultCodexChatsRoot())
	if err != nil {
		return err
	}
	selectedCodexBin, err := prompt(reader, out, "Codex binary", codexBin)
	if err != nil {
		return err
	}
	values := map[string]string{
		"CTR_GO_TELEGRAM_BOT_TOKEN": token,
		"CTR_GO_ALLOWED_USER_IDS":   allowedUsers,
		"CTR_GO_SYNC_GROUP_ID":      syncGroupID,
		"CTR_GO_DEFAULT_CWD":        defaultCWD,
		"CTR_GO_CODEX_CHATS_ROOT":   chatsRoot,
		"CTR_GO_CODEX_BIN":          selectedCodexBin,
	}
	if err := writeConfigEnv(path, values, force); err != nil {
		return err
	}

	_, _ = fmt.Fprintf(out, "\nWrote %s\n", path)
	_, _ = fmt.Fprintln(out, "Next steps:")
	_, _ = fmt.Fprintln(out, "  ctr-go doctor")
	_, _ = fmt.Fprintln(out, "  ctr-go daemon run")
	return nil
}

func promptRequired(reader *bufio.Reader, out io.Writer, label string) (string, error) {
	value, err := prompt(reader, out, label, "")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	return value, nil
}

func prompt(reader *bufio.Reader, out io.Writer, label, fallback string) (string, error) {
	if fallback == "" {
		_, _ = fmt.Fprintf(out, "%s: ", label)
	} else {
		_, _ = fmt.Fprintf(out, "%s [%s]: ", label, fallback)
	}
	line, err := reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return fallback, nil
	}
	return value, nil
}

func writeConfigEnv(path string, values map[string]string, force bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if force {
		flag = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	file, err := os.OpenFile(path, flag, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	_, _ = fmt.Fprintln(file, "# codex-tg local configuration")
	_, _ = fmt.Fprintln(file, "# Keep this file private. It contains Telegram credentials.")
	for _, key := range keys {
		_, _ = fmt.Fprintf(file, "%s=%s\n", key, strconv.Quote(values[key]))
	}
	return file.Chmod(0o600)
}

func printUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage:")
	_, _ = fmt.Fprintln(out, "  ctr-go init [--force]")
	_, _ = fmt.Fprintln(out, "  ctr-go service install [flags]")
	_, _ = fmt.Fprintln(out, "  ctr-go service start|stop|restart|status|enable-login|disable-login|uninstall")
	_, _ = fmt.Fprintln(out, "  ctr-go daemon run")
	_, _ = fmt.Fprintln(out, "  ctr-go status")
	_, _ = fmt.Fprintln(out, "  ctr-go doctor")
	_, _ = fmt.Fprintln(out, "  ctr-go repair")
	_, _ = fmt.Fprintln(out, "  ctr-go version")
}

func printInitUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage:")
	_, _ = fmt.Fprintln(out, "  ctr-go init [--force]")
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "Creates a private local config.env for codex-tg.")
	_, _ = fmt.Fprintln(out, "Set CTR_GO_CONFIG to choose a custom config path.")
}

func formatIDs(values []int64) string {
	if len(values) == 0 {
		return "(none)"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return strings.Join(parts, ", ")
}

func normalizeStringMap(value any) map[string]string {
	switch typed := value.(type) {
	case map[string]string:
		return typed
	case map[string]any:
		out := make(map[string]string, len(typed))
		for key, raw := range typed {
			out[key] = fmt.Sprintf("%v", raw)
		}
		return out
	default:
		return nil
	}
}

func normalizeInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}
