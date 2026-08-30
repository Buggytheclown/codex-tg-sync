package arcanumreview

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

const (
	Source              = "arcanum_review"
	DefaultPollInterval = time.Minute
)

type RequestSink interface {
	EnqueueExternalRequests(ctx context.Context, source string, requests []model.ExternalLaunchRequest) (int, error)
	NoteExternalPollStarted(ctx context.Context, source string)
	NoteExternalPollResult(ctx context.Context, source string, err error)
}

type Config struct {
	Login            string
	CWD              string
	TelegramTopicID  int64
	AutoStartAuthors []string
}

type Poller struct {
	client AssignedClient
	sink   RequestSink
	config Config
	seen   map[string]struct{}
}

func NewPoller(client AssignedClient, sink RequestSink, config Config) *Poller {
	config.Login = strings.TrimSpace(config.Login)
	config.CWD = strings.TrimSpace(config.CWD)
	config.AutoStartAuthors = normalizeAuthors(config.AutoStartAuthors)
	return &Poller{client: client, sink: sink, config: config, seen: make(map[string]struct{})}
}

func (p *Poller) PollOnce(ctx context.Context) (int, error) {
	if p == nil || p.client == nil || p.sink == nil || p.config.Login == "" || p.config.CWD == "" || p.config.TelegramTopicID == 0 {
		return 0, errors.New("Arcanum review poller is not configured")
	}
	pullRequests, err := p.client.ListAssigned(ctx, p.config.Login)
	if err != nil {
		return 0, err
	}
	requests := make([]model.ExternalLaunchRequest, 0, len(pullRequests))
	for _, pullRequest := range pullRequests {
		externalID := strconv.FormatInt(pullRequest.ID, 10)
		if _, ok := p.seen[externalID]; ok {
			continue
		}
		requests = append(requests, requestForPullRequest(pullRequest, p.config))
	}
	if len(requests) == 0 {
		return 0, nil
	}
	created, err := p.sink.EnqueueExternalRequests(ctx, Source, requests)
	if err != nil {
		return 0, err
	}
	for _, request := range requests {
		p.seen[request.ExternalID] = struct{}{}
	}
	return created, nil
}

func (p *Poller) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	for {
		p.sink.NoteExternalPollStarted(ctx, Source)
		_, err := p.PollOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		p.sink.NoteExternalPollResult(ctx, Source, err)
		if err != nil && onError != nil {
			onError(err)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func requestForPullRequest(pullRequest PullRequest, config Config) model.ExternalLaunchRequest {
	externalID := strconv.FormatInt(pullRequest.ID, 10)
	url := fmt.Sprintf("https://a.yandex-team.ru/review/%d", pullRequest.ID)
	prompt := "$arc-pr-review [" + url + "](" + url + ")"
	now := model.NowString()
	return model.ExternalLaunchRequest{
		ID: Source + ":" + externalID, Source: Source, ExternalID: externalID,
		Sender: strings.TrimSpace(pullRequest.Author), Title: strings.TrimSpace(pullRequest.Summary),
		SafePreview: "Review Arcadia PR #" + externalID, SourceURL: url, Prompt: prompt,
		CWD: config.CWD, Status: model.ExternalLaunchPendingApproval, TelegramTopicID: config.TelegramTopicID,
		AutoStart: authorAllowed(pullRequest.Author, config.AutoStartAuthors),
		CreatedAt: now, UpdatedAt: now,
	}
}

func normalizeAuthors(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		author := normalizeAuthor(value)
		if author == "" {
			continue
		}
		if _, ok := seen[author]; ok {
			continue
		}
		seen[author] = struct{}{}
		result = append(result, author)
	}
	return result
}

func authorAllowed(author string, allowed []string) bool {
	author = normalizeAuthor(author)
	for _, candidate := range allowed {
		if author == candidate {
			return true
		}
	}
	return false
}

func normalizeAuthor(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "@")))
}
