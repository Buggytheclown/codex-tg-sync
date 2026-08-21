package ymessenger

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type UpdatesClient interface {
	GetUpdates(ctx context.Context, offset int64, limit int) ([]Update, error)
	GetThreadRoot(ctx context.Context, chatID string, messageID int64) (*ContextMessage, error)
}

type RequestSink interface {
	ExternalSourceCursor(ctx context.Context, source string) (int64, error)
	IngestExternalRequests(ctx context.Context, source string, cursor int64, requests []model.ExternalLaunchRequest) (int, error)
}

type Poller struct {
	client UpdatesClient
	sink   RequestSink
	filter FilterConfig
}

func NewPoller(client UpdatesClient, sink RequestSink, filter FilterConfig) *Poller {
	return &Poller{client: client, sink: sink, filter: filter}
}

func (p *Poller) PollOnce(ctx context.Context) error {
	if p == nil || p.client == nil || p.sink == nil {
		return errors.New("YMessenger poller is not configured")
	}
	cursor, err := p.sink.ExternalSourceCursor(ctx, Source)
	if err != nil {
		return err
	}
	offset := int64(0)
	if cursor > 0 {
		offset = cursor + 1
	}
	updates, err := p.client.GetUpdates(ctx, offset, 100)
	if err != nil {
		return err
	}
	if len(updates) == 0 {
		return nil
	}
	maxUpdateID := cursor
	for _, update := range updates {
		if update.UpdateID > maxUpdateID {
			maxUpdateID = update.UpdateID
		}
	}
	if maxUpdateID == cursor {
		return nil
	}
	actionable := actionableUpdates(updates, p.filter)
	roots := make(map[string]ContextMessage, len(actionable))
	resolvedRoots := make(map[string]struct{}, len(actionable))
	for _, update := range actionable {
		if update.Chat.ThreadID == 0 {
			continue
		}
		key := SourceMessageKey(update.Chat.ID, update.Chat.ThreadID)
		if _, ok := resolvedRoots[key]; ok {
			continue
		}
		message, lookupErr := p.client.GetThreadRoot(ctx, update.Chat.ID, update.Chat.ThreadID)
		if lookupErr != nil {
			return lookupErr
		}
		resolvedRoots[key] = struct{}{}
		if message != nil {
			roots[key] = *message
		}
	}
	_, err = p.sink.IngestExternalRequests(ctx, Source, maxUpdateID, RequestsFromUpdatesWithRoots(actionable, p.filter, roots))
	return err
}

func actionableUpdates(updates []Update, cfg FilterConfig) []Update {
	allowed := make(map[string]struct{}, len(cfg.AllowedSenders))
	for _, sender := range cfg.AllowedSenders {
		if normalized := normalizeLogin(sender); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	robotLogin := normalizeLogin(cfg.RobotLogin)
	result := make([]Update, 0, len(updates))
	for _, update := range updates {
		sender := normalizeLogin(update.From.Login)
		if update.UpdateID < 0 || update.MessageID == 0 || strings.TrimSpace(update.Chat.ID) == "" || sender == "" || update.From.Robot || strings.TrimSpace(update.Text) == "" {
			continue
		}
		if _, ok := allowed[sender]; !ok || !mentionsLogin(update.MentionedUsers, robotLogin) {
			continue
		}
		result = append(result, update)
	}
	return result
}

func (p *Poller) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	errorDelay := retryDelay(interval)
	for {
		err := p.PollOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		delay := interval
		if err != nil {
			delay = errorDelay
			if onError != nil {
				onError(err)
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func retryDelay(interval time.Duration) time.Duration {
	if interval < 5*time.Second {
		return 5 * time.Second
	}
	return interval
}
