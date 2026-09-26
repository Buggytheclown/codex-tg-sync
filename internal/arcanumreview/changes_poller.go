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

const ChangesSource = "arcanum_changes"

type ChangesPoller struct {
	client WaitingChangesClient
	sink   RequestSink
	config Config
	seen   map[string]struct{}
}

func NewChangesPoller(client WaitingChangesClient, sink RequestSink, config Config) *ChangesPoller {
	config.Login = strings.TrimSpace(config.Login)
	config.CWD = strings.TrimSpace(config.CWD)
	return &ChangesPoller{client: client, sink: sink, config: config, seen: make(map[string]struct{})}
}

func (p *ChangesPoller) PollOnce(ctx context.Context) (int, error) {
	if p == nil || p.client == nil || p.sink == nil || p.config.Login == "" || p.config.CWD == "" || p.config.TelegramTopicID == 0 {
		return 0, errors.New("Arcanum changes poller is not configured")
	}
	pullRequests, err := p.client.ListAuthoredWaitingForChanges(ctx, p.config.Login)
	if err != nil {
		return 0, err
	}
	requests := make([]model.ExternalLaunchRequest, 0, len(pullRequests))
	for _, pullRequest := range pullRequests {
		if normalizeAuthor(pullRequest.Author) != normalizeAuthor(p.config.Login) {
			continue
		}
		externalID := strconv.FormatInt(pullRequest.ID, 10)
		if _, ok := p.seen[externalID]; ok {
			continue
		}
		requests = append(requests, changesRequestForPullRequest(pullRequest, p.config))
	}
	if len(requests) == 0 {
		return 0, nil
	}
	created, err := p.sink.EnqueueExternalRequests(ctx, ChangesSource, requests)
	if err != nil {
		return 0, err
	}
	for _, request := range requests {
		p.seen[request.ExternalID] = struct{}{}
	}
	return created, nil
}

func (p *ChangesPoller) Run(ctx context.Context, interval time.Duration, onError func(error)) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	for {
		p.sink.NoteExternalPollStarted(ctx, ChangesSource)
		_, err := p.PollOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		p.sink.NoteExternalPollResult(ctx, ChangesSource, err)
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

func changesRequestForPullRequest(pullRequest PullRequest, config Config) model.ExternalLaunchRequest {
	externalID := strconv.FormatInt(pullRequest.ID, 10)
	url := fmt.Sprintf("https://a.yandex-team.ru/review/%d", pullRequest.ID)
	now := model.NowString()
	return model.ExternalLaunchRequest{
		ID: ChangesSource + ":" + externalID, Source: ChangesSource, ExternalID: externalID,
		Sender: strings.TrimSpace(pullRequest.Author), Title: strings.TrimSpace(pullRequest.Summary),
		SafePreview: "Summarize feedback on Arcadia PR #" + externalID, SourceURL: url,
		Prompt: url + " кратко расскажи суть замечаний",
		CWD:    config.CWD, Status: model.ExternalLaunchPendingApproval, TelegramTopicID: config.TelegramTopicID,
		AutoStart: true, CreatedAt: now, UpdatedAt: now,
	}
}
