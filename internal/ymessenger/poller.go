package ymessenger

import (
	"context"
	"errors"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type UpdatesClient interface {
	GetUpdates(ctx context.Context, offset int64, limit int) ([]Update, error)
}

type RequestSink interface {
	ExternalSourceCursor(ctx context.Context, source string) (int64, error)
	ExternalSourceMessage(ctx context.Context, source, chatID string, messageID int64) (*model.ExternalSourceMessage, error)
	IngestExternalBatch(ctx context.Context, source string, cursor int64, messages []model.ExternalSourceMessage, requests []model.ExternalLaunchRequest) (int, error)
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
	messages := RootMessagesFromUpdates(updates)
	roots := make(map[string]model.ExternalSourceMessage, len(messages))
	for _, message := range messages {
		roots[SourceMessageKey(message.ChatID, message.MessageID)] = message
	}
	for _, update := range updates {
		if update.Chat.ThreadID == 0 {
			continue
		}
		key := SourceMessageKey(update.Chat.ID, update.Chat.ThreadID)
		if _, ok := roots[key]; ok {
			continue
		}
		message, lookupErr := p.sink.ExternalSourceMessage(ctx, Source, update.Chat.ID, update.Chat.ThreadID)
		if lookupErr != nil {
			return lookupErr
		}
		if message != nil {
			roots[key] = *message
		}
	}
	_, err = p.sink.IngestExternalBatch(ctx, Source, maxUpdateID, messages, RequestsFromUpdatesWithRoots(updates, p.filter, roots))
	return err
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
