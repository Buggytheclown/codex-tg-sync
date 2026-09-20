package telegram

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/config"
	"github.com/mideco-tech/codex-tg/internal/daemon"
	"github.com/mideco-tech/codex-tg/internal/model"
)

const telegramMessageLimit = 4096

var telegramBotTokenURLPattern = regexp.MustCompile(`bot[0-9]+:[A-Za-z0-9_-]+`)

type Bot struct {
	cfg     config.Config
	client  *Client
	egress  *egressGovernor
	service *daemon.Service
	logger  *log.Logger
	me      *User
}

type Document struct {
	Name        string
	ContentType string
	Data        []byte
	Caption     string
}

func NewBot(cfg config.Config, service *daemon.Service, logger *log.Logger) (*Bot, error) {
	if strings.TrimSpace(cfg.TelegramBotToken) == "" {
		return nil, errors.New("CTR_GO_TELEGRAM_BOT_TOKEN is not configured")
	}
	if logger == nil {
		logger = log.Default()
	}
	bot := &Bot{
		cfg:     cfg,
		client:  NewClient(cfg.TelegramBotToken),
		egress:  newEgressGovernor(defaultTelegramGroupWriteInterval),
		service: service,
		logger:  logger,
	}
	bot.egress.observe = bot.logEgressObservation
	service.SetSyncForum(bot)
	return bot, nil
}

func (b *Bot) ValidateSyncGroup(ctx context.Context, allowedUserID int64) error {
	if b.cfg.SyncGroupID == 0 {
		return errors.New("CTR_GO_SYNC_GROUP_ID is not configured")
	}
	if b.me == nil || b.me.ID == 0 {
		return errors.New("telegram bot identity is unavailable")
	}
	probe, err := b.client.ProbeForumGroup(ctx, b.cfg.SyncGroupID, b.me.ID, allowedUserID)
	if err != nil {
		return err
	}
	return probe.Validate(b.cfg.SyncGroupID, b.me.ID, allowedUserID)
}

func (b *Bot) PrepareSyncControl(ctx context.Context) error {
	err := b.groupWrite(ctx, egressOperationEditGeneralTopic, model.SendOptions{}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return b.client.EditGeneralForumTopic(attemptCtx, b.cfg.SyncGroupID, "Control")
	})
	if err == nil || IsTopicNotModified(err) {
		return nil
	}
	return errors.New(sanitizeTelegramLogError(err))
}

func (b *Bot) CreateSyncTopic(ctx context.Context, title string) (int64, error) {
	return b.createSyncTopic(ctx, title, true)
}

func (b *Bot) CreateLaunchTopic(ctx context.Context, title string) (int64, error) {
	return b.createSyncTopic(ctx, title, false)
}

func (b *Bot) createSyncTopic(ctx context.Context, title string, retry429 bool) (int64, error) {
	var topic *ForumTopic
	err := b.groupWrite(ctx, egressOperationCreateForumTopic, model.SendOptions{}, retry429, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var err error
		topic, err = b.client.CreateForumTopic(attemptCtx, b.cfg.SyncGroupID, title)
		return err
	})
	if err != nil {
		safeErr := errors.New(sanitizeTelegramLogError(err))
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if IsRetryable(err) {
				return 0, daemon.NewSyncForumFailure(daemon.SyncForumFailureRetryable, safeErr)
			}
			return 0, daemon.NewSyncForumFailure(daemon.SyncForumFailureDefinitive, safeErr)
		}
		return 0, daemon.NewSyncForumFailure(daemon.SyncForumFailureUnknown, safeErr)
	}
	if topic == nil {
		return 0, errors.New("telegram createForumTopic returned no topic")
	}
	return topic.MessageThreadID, nil
}

func (b *Bot) RenameSyncTopic(ctx context.Context, topicID int64, title string) error {
	err := b.groupWrite(ctx, egressOperationEditForumTopic, model.SendOptions{Background: true}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return b.client.EditForumTopic(attemptCtx, b.cfg.SyncGroupID, topicID, title)
	})
	if err == nil || IsTopicNotModified(err) {
		return nil
	}
	return errors.New(sanitizeTelegramLogError(err))
}

func (b *Bot) DeleteSyncTopic(ctx context.Context, topicID int64) error {
	err := b.groupWrite(ctx, egressOperationDeleteForumTopic, model.SendOptions{Background: true}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		return b.client.DeleteForumTopic(attemptCtx, b.cfg.SyncGroupID, topicID)
	})
	if IsTopicNotFound(err) {
		return nil
	}
	return err
}

func (b *Bot) SendSyncMessage(ctx context.Context, topicID int64, rendered model.RenderedMessage, options model.SendOptions) (int64, error) {
	var message *Message
	err := b.groupWrite(ctx, egressOperationSendMessage, options, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var err error
		message, err = b.client.SendRenderedMessage(attemptCtx, b.cfg.SyncGroupID, topicID, rendered, nil, options)
		return err
	})
	if err != nil {
		return 0, err
	}
	if message == nil {
		return 0, errors.New("telegram sendMessage returned no message")
	}
	return message.MessageID, nil
}

func (b *Bot) SendSyncActionMessage(ctx context.Context, topicID int64, text string, buttons [][]model.ButtonSpec) (int64, error) {
	var message *Message
	err := b.groupWrite(ctx, egressOperationSendMessage, model.SendOptions{}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var err error
		message, err = b.client.SendMessage(attemptCtx, b.cfg.SyncGroupID, topicID, text, toInlineKeyboard(buttons), model.SendOptions{})
		return err
	})
	if err != nil {
		return 0, err
	}
	if message == nil {
		return 0, errors.New("telegram sendMessage returned no message")
	}
	return message.MessageID, nil
}

func (b *Bot) EditSyncMessage(ctx context.Context, topicID, messageID int64, rendered model.RenderedMessage, options model.SendOptions) error {
	return b.groupWrite(ctx, egressOperationEditMessage, options, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, err := b.client.EditRenderedMessageText(attemptCtx, b.cfg.SyncGroupID, messageID, rendered, nil)
		return err
	})
}

func (b *Bot) Start(ctx context.Context) error {
	startCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	me, err := b.client.GetMe(startCtx)
	if err != nil {
		return err
	}
	b.me = me
	if err := b.client.DeleteMyCommands(startCtx); err != nil {
		return fmt.Errorf("clear default Telegram commands: %w", err)
	}
	if err := b.client.SetMyCommandsForChat(startCtx, b.cfg.SyncGroupID, defaultCommands()); err != nil {
		return fmt.Errorf("set Sync Telegram commands: %w", err)
	}
	if b.egress != nil {
		b.egress.Start(ctx)
	}
	b.logger.Printf("telegram bot ready: @%s", me.Username)
	return nil
}

func (b *Bot) Run(ctx context.Context) error {
	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		pollCtx, cancel := context.WithTimeout(ctx, 65*time.Second)
		updates, err := b.client.GetUpdates(pollCtx, offset, 30)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			b.logger.Printf("telegram getUpdates failed: %s", sanitizeTelegramLogError(err))
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
				continue
			}
		}
		b.acknowledgeCallbacks(ctx, updates)
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if err := b.handleUpdate(ctx, update); err != nil {
				b.logger.Printf("telegram update %d failed: %s", update.UpdateID, sanitizeTelegramLogError(err))
			}
		}
	}
}

func SanitizeLogError(err error) string {
	if err == nil {
		return ""
	}
	return telegramBotTokenURLPattern.ReplaceAllString(err.Error(), "bot<redacted>")
}

func sanitizeTelegramLogError(err error) string {
	return SanitizeLogError(err)
}

func (b *Bot) SendMessage(ctx context.Context, chatID, topicID int64, text string, buttons [][]model.ButtonSpec, options model.SendOptions) (int64, error) {
	chunks := splitText(strings.TrimSpace(text), telegramMessageLimit)
	if len(chunks) == 0 {
		chunks = []string{" "}
	}
	return b.sendTextChunks(ctx, chatID, topicID, chunks, buttons, options)
}

func (b *Bot) SendRenderedMessages(ctx context.Context, chatID, topicID int64, messages []model.RenderedMessage, buttons [][]model.ButtonSpec, options model.SendOptions) ([]int64, error) {
	if len(messages) == 0 {
		messages = []model.RenderedMessage{{Text: " "}}
	}
	ids := make([]int64, 0, len(messages))
	for index, rendered := range messages {
		if strings.TrimSpace(rendered.Text) == "" {
			rendered.Text = " "
			rendered.Entities = nil
		}
		var markup *InlineKeyboardMarkup
		if index == len(messages)-1 {
			markup = toInlineKeyboard(buttons)
		}
		var message *Message
		err := b.groupWrite(ctx, egressOperationSendMessage, options, true, func() error {
			attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			var err error
			message, err = b.client.SendRenderedMessage(attemptCtx, chatID, topicID, rendered, markup, options)
			return err
		})
		if err != nil {
			rendered.Entities = nil
			err = b.groupWrite(ctx, egressOperationSendMessage, options, true, func() error {
				attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				var err error
				message, err = b.client.SendRenderedMessage(attemptCtx, chatID, topicID, rendered, markup, options)
				return err
			})
			if err != nil {
				return nil, err
			}
		}
		if message != nil {
			ids = append(ids, message.MessageID)
		}
	}
	return ids, nil
}

func (b *Bot) EditMessage(ctx context.Context, chatID, topicID, messageID int64, text string, buttons [][]model.ButtonSpec) error {
	chunks := splitText(strings.TrimSpace(text), telegramMessageLimit)
	if len(chunks) != 1 {
		return fmt.Errorf("telegram editMessageText requires a single text chunk, got %d", len(chunks))
	}
	return b.groupWrite(ctx, egressOperationEditMessage, model.SendOptions{}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, err := b.client.EditMessageText(attemptCtx, chatID, messageID, chunks[0], toInlineKeyboard(buttons))
		return err
	})
}

func (b *Bot) EditRenderedMessage(ctx context.Context, chatID, topicID, messageID int64, rendered model.RenderedMessage, buttons [][]model.ButtonSpec) error {
	if strings.TrimSpace(rendered.Text) == "" {
		rendered.Text = " "
		rendered.Entities = nil
	}
	err := b.groupWrite(ctx, egressOperationEditMessage, model.SendOptions{}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, err := b.client.EditRenderedMessageText(attemptCtx, chatID, messageID, rendered, toInlineKeyboard(buttons))
		return err
	})
	if err == nil {
		return nil
	}
	rendered.Entities = nil
	fallbackErr := b.groupWrite(ctx, egressOperationEditMessage, model.SendOptions{}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		_, err := b.client.EditRenderedMessageText(attemptCtx, chatID, messageID, rendered, toInlineKeyboard(buttons))
		return err
	})
	if fallbackErr != nil {
		return errors.Join(err, fallbackErr)
	}
	return nil
}

func (b *Bot) SendDocument(ctx context.Context, chatID, topicID int64, fileName, filePath, caption string, options model.SendOptions) (int64, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return 0, err
	}
	return b.SendDocumentData(ctx, chatID, topicID, fileName, data, caption, options)
}

func (b *Bot) SendDocumentData(ctx context.Context, chatID, topicID int64, fileName string, data []byte, caption string, options model.SendOptions) (int64, error) {
	var message *Message
	err := b.groupWrite(ctx, egressOperationSendDocument, options, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		var err error
		message, err = b.client.SendDocument(attemptCtx, chatID, topicID, DocumentFile{
			Name:        fileName,
			ContentType: "application/octet-stream",
			Data:        data,
		}, strings.TrimSpace(caption), nil, options)
		return err
	})
	if err != nil {
		return 0, err
	}
	if message == nil {
		return 0, nil
	}
	return message.MessageID, nil
}

func (b *Bot) DeleteMessage(ctx context.Context, chatID, topicID, messageID int64) error {
	return b.groupWrite(ctx, egressOperationDeleteMessage, model.SendOptions{Background: true}, true, func() error {
		attemptCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return b.client.DeleteMessage(attemptCtx, chatID, messageID)
	})
}

func (b *Bot) handleUpdate(ctx context.Context, update Update) error {
	switch {
	case update.CallbackQuery != nil:
		return b.handleAcknowledgedCallback(ctx, *update.CallbackQuery)
	case update.Message != nil:
		return b.handleMessage(ctx, *update.Message)
	case update.EditedMessage != nil:
		return b.handleMessage(ctx, *update.EditedMessage)
	default:
		return nil
	}
}

func (b *Bot) handleMessage(ctx context.Context, message Message) error {
	if message.From == nil {
		return nil
	}
	text := telegramInboundText(message)
	if text == "" && !telegramMessageHasUnsupportedMedia(message) {
		return nil
	}
	replyTo := int64(0)
	if message.ReplyToMessage != nil {
		replyTo = message.ReplyToMessage.MessageID
	}
	response, err := b.service.HandleMessageWithID(ctx, message.Chat.ID, message.MessageThreadID, message.MessageID, message.From.ID, text, replyTo)
	if err != nil {
		return b.sendFailureMessage(ctx, message.Chat.ID, message.MessageThreadID, err)
	}
	return b.deliverDirectResponse(ctx, message.Chat.ID, message.MessageThreadID, response)
}

func telegramInboundText(message Message) string {
	if text := strings.TrimSpace(message.Text); text != "" {
		return text
	}
	return strings.TrimSpace(message.Caption)
}

func telegramMessageHasUnsupportedMedia(message Message) bool {
	return len(message.Photo) > 0 || len(message.Document) > 0 || len(message.Voice) > 0 || len(message.Audio) > 0 || len(message.Video) > 0
}

// handleAcknowledgedCallback is called only after Run's batch pre-pass has
// dismissed the Telegram callback spinner through the critical egress lane.
func (b *Bot) handleAcknowledgedCallback(ctx context.Context, callback CallbackQuery) error {
	if callback.From == nil {
		return nil
	}
	chatID := int64(0)
	topicID := int64(0)
	if callback.Message != nil {
		chatID = callback.Message.Chat.ID
		topicID = callback.Message.MessageThreadID
	}
	messageID := int64(0)
	if callback.Message != nil {
		messageID = callback.Message.MessageID
	}
	response, err := b.service.HandleCallback(ctx, chatID, topicID, messageID, callback.From.ID, callback.Data)
	if err != nil {
		return b.sendFailureMessage(ctx, chatID, topicID, err)
	}
	return b.deliverDirectResponse(ctx, chatID, topicID, response)
}

func (b *Bot) acknowledgeCallbacks(ctx context.Context, updates []Update) {
	for _, update := range updates {
		callback := update.CallbackQuery
		if callback == nil || strings.TrimSpace(callback.ID) == "" || callback.From == nil || callback.Message == nil || b.service == nil ||
			!b.service.IsAllowed(callback.From.ID, callback.Message.Chat.ID) {
			continue
		}
		answerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := b.criticalWrite(answerCtx, func() error {
			return b.client.AnswerCallbackQuery(answerCtx, callback.ID, "", false)
		})
		cancel()
		if err != nil && ctx.Err() == nil && b.logger != nil {
			b.logger.Printf("telegram callback acknowledgement failed: %s", sanitizeTelegramLogError(err))
		}
	}
}

func (b *Bot) deliverDirectResponse(ctx context.Context, chatID, topicID int64, response *daemon.DirectResponse) error {
	if response == nil || strings.TrimSpace(response.Text) == "" {
		return nil
	}
	messageID, err := b.SendMessage(ctx, chatID, topicID, response.Text, response.Buttons, model.SendOptions{Silent: true})
	if err != nil {
		return err
	}
	return b.service.RegisterDirectDelivery(ctx, chatID, topicID, messageID, response)
}

func (b *Bot) sendFailureMessage(ctx context.Context, chatID, topicID int64, cause error) error {
	if chatID == 0 {
		if cause != nil {
			b.logger.Printf("telegram handler error without chat context: %v", cause)
		}
		return nil
	}
	text := "Request failed inside the local Go bridge. Try /repair or /status."
	if cause != nil {
		b.logger.Printf("telegram handler error: %v", cause)
	}
	_, err := b.SendMessage(ctx, chatID, topicID, text, nil, model.SendOptions{Silent: true})
	if err != nil {
		if cause != nil {
			return errors.Join(cause, err)
		}
		return err
	}
	return nil
}

func defaultCommands() []BotCommand {
	return []BotCommand{
		{Command: "sync", Description: "Enable or disable Sync mode"},
		{Command: "status", Description: "Show Sync mode status"},
		{Command: "pollers", Description: "Show source poller health"},
		{Command: "requests", Description: "Show active launch requests"},
		{Command: "refresh", Description: "Refresh Desktop chat topics"},
		{Command: "projects", Description: "Start a task in a project"},
		{Command: "newchat", Description: "Start a new Codex chat"},
		{Command: "stop", Description: "Interrupt the active turn"},
	}
}

func toInlineKeyboard(rows [][]model.ButtonSpec) *InlineKeyboardMarkup {
	if len(rows) == 0 {
		return nil
	}
	keyboard := make([][]InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		buttonRow := make([]InlineKeyboardButton, 0, len(row))
		for _, button := range row {
			if strings.TrimSpace(button.Text) == "" {
				continue
			}
			buttonRow = append(buttonRow, InlineKeyboardButton{
				Text:         button.Text,
				CallbackData: button.CallbackData,
			})
		}
		if len(buttonRow) > 0 {
			keyboard = append(keyboard, buttonRow)
		}
	}
	if len(keyboard) == 0 {
		return nil
	}
	return &InlineKeyboardMarkup{InlineKeyboard: keyboard}
}

func (b *Bot) sendTextChunks(ctx context.Context, chatID, topicID int64, chunks []string, buttons [][]model.ButtonSpec, options model.SendOptions) (int64, error) {
	var messageID int64
	for index, chunk := range chunks {
		var markup *InlineKeyboardMarkup
		if index == len(chunks)-1 {
			markup = toInlineKeyboard(buttons)
		}
		var message *Message
		err := b.groupWrite(ctx, egressOperationSendMessage, options, true, func() error {
			attemptCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			var err error
			message, err = b.client.SendMessage(attemptCtx, chatID, topicID, chunk, markup, options)
			return err
		})
		if err != nil {
			return 0, err
		}
		if message != nil {
			messageID = message.MessageID
		}
	}
	return messageID, nil
}

func (b *Bot) groupWrite(ctx context.Context, operation egressOperationName, options model.SendOptions, retry429 bool, attempt func() error) error {
	if b.egress == nil {
		return attempt()
	}
	priority := egressForeground
	if options.Background {
		priority = egressBackground
	}
	return b.egress.Do(ctx, egressOperation{name: operation, priority: priority, retry429: retry429}, attempt)
}

func (b *Bot) logEgressObservation(value egressObservation) {
	if b == nil || b.logger == nil {
		return
	}
	b.logger.Printf("telegram_egress operation=%s priority=%s queue_wait_ms=%d api_duration_ms=%d outcome=%s",
		value.Operation, value.Priority, value.QueueWait.Milliseconds(), value.APIDuration.Milliseconds(), value.Outcome)
}

func (b *Bot) criticalWrite(ctx context.Context, attempt func() error) error {
	if b.egress == nil {
		return attempt()
	}
	return b.egress.DoCritical(ctx, attempt)
}

func splitText(text string, limit int) []string {
	if limit <= 0 {
		return []string{text}
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if len(text) <= limit {
		return []string{text}
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	current := strings.Builder{}
	flush := func() {
		if current.Len() == 0 {
			return
		}
		out = append(out, strings.TrimSpace(current.String()))
		current.Reset()
	}
	for _, line := range lines {
		line = strings.TrimRight(line, " ")
		candidate := line
		if current.Len() > 0 {
			candidate = current.String() + "\n" + line
		}
		if len(candidate) <= limit {
			if current.Len() > 0 {
				current.WriteByte('\n')
			}
			current.WriteString(line)
			continue
		}
		flush()
		for len(line) > limit {
			out = append(out, strings.TrimSpace(line[:limit]))
			line = line[limit:]
		}
		if line != "" {
			current.WriteString(line)
		}
	}
	flush()
	if len(out) == 0 {
		return []string{text}
	}
	return out
}

func (b *Bot) String() string {
	if b.me == nil {
		return "telegram bot"
	}
	return fmt.Sprintf("@%s", b.me.Username)
}
