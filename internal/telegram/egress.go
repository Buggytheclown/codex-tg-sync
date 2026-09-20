package telegram

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	defaultTelegramGroupWriteInterval   = time.Second
	defaultTelegramGroupWriteLimit      = 20
	defaultTelegramBackgroundWriteLimit = 12
	defaultTelegramGroupWriteWindow     = 60*time.Second + 250*time.Millisecond
	defaultTelegramRetryAfter           = 5 * time.Second
	foregroundWriteBurst                = 4
)

type egressPriority uint8

const (
	egressForeground egressPriority = iota
	egressBackground
)

type egressOperation struct {
	name     egressOperationName
	priority egressPriority
	retry429 bool
}

type egressOperationName string

const (
	egressOperationUnknown          egressOperationName = "unknown"
	egressOperationEditGeneralTopic egressOperationName = "edit_general_topic"
	egressOperationCreateForumTopic egressOperationName = "create_forum_topic"
	egressOperationEditForumTopic   egressOperationName = "edit_forum_topic"
	egressOperationDeleteForumTopic egressOperationName = "delete_forum_topic"
	egressOperationSendMessage      egressOperationName = "send_message"
	egressOperationEditMessage      egressOperationName = "edit_message"
	egressOperationSendDocument     egressOperationName = "send_document"
	egressOperationDeleteMessage    egressOperationName = "delete_message"
)

type egressObservation struct {
	Operation   egressOperationName
	Priority    string
	QueueWait   time.Duration
	APIDuration time.Duration
	Outcome     string
}

type egressRequest struct {
	ctx       context.Context
	op        egressOperation
	attempt   func() error
	queuedAt  time.Time
	notBefore time.Time
	result    chan error
}

type egressClock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type realEgressClock struct{}

func (realEgressClock) Now() time.Time                             { return time.Now() }
func (realEgressClock) After(delay time.Duration) <-chan time.Time { return time.After(delay) }

type egressGovernor struct {
	interval time.Duration
	clock    egressClock
	incoming chan *egressRequest
	wake     chan struct{}
	done     chan struct{}
	observe  func(egressObservation)

	startOnce sync.Once
	mu        sync.Mutex
	cooldown  time.Time
}

func newEgressGovernor(interval time.Duration) *egressGovernor {
	return newEgressGovernorWithClock(interval, realEgressClock{})
}

func newEgressGovernorWithClock(interval time.Duration, clock egressClock) *egressGovernor {
	if interval < 0 {
		interval = 0
	}
	if clock == nil {
		clock = realEgressClock{}
	}
	return &egressGovernor{
		interval: interval,
		clock:    clock,
		incoming: make(chan *egressRequest, 256),
		wake:     make(chan struct{}, 1),
		done:     make(chan struct{}),
	}
}

func (g *egressGovernor) Start(ctx context.Context) {
	if g == nil {
		return
	}
	g.startOnce.Do(func() {
		go g.run(ctx)
	})
}

func (g *egressGovernor) Do(ctx context.Context, operation egressOperation, attempt func() error) error {
	if attempt == nil {
		return errors.New("telegram egress attempt is required")
	}
	if g == nil {
		return attempt()
	}
	if operation.name == "" {
		operation.name = egressOperationUnknown
	}
	request := &egressRequest{ctx: ctx, op: operation, attempt: attempt, queuedAt: g.clock.Now(), result: make(chan error, 1)}
	select {
	case g.incoming <- request:
	case <-ctx.Done():
		return ctx.Err()
	case <-g.done:
		return context.Canceled
	}
	select {
	case err := <-request.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-g.done:
		return context.Canceled
	}
}

// DoCritical uses the same 429 observation point but bypasses group pacing and
// cooldown. It is reserved for answerCallbackQuery, which does not write into
// the Sync group and must dismiss Telegram's callback spinner promptly.
func (g *egressGovernor) DoCritical(ctx context.Context, attempt func() error) error {
	if attempt == nil {
		return errors.New("telegram critical egress attempt is required")
	}
	err := attempt()
	if delay, ok := telegramRetryAfter(err); ok && g != nil {
		g.noteCooldown(g.clock.Now().Add(delay))
	}
	return err
}

func (g *egressGovernor) run(ctx context.Context) {
	defer close(g.done)
	foreground := make([]*egressRequest, 0)
	background := make([]*egressRequest, 0)
	foregroundStreak := 0
	nextWriteAt := time.Time{}
	writeAttempts := make([]time.Time, 0, defaultTelegramGroupWriteLimit)
	backgroundAttempts := make([]time.Time, 0, defaultTelegramBackgroundWriteLimit)

	finishPending := func(err error) {
		for _, queue := range [][]*egressRequest{foreground, background} {
			for _, request := range queue {
				finishEgressRequest(request, err)
			}
		}
		for {
			select {
			case request := <-g.incoming:
				finishEgressRequest(request, err)
			default:
				return
			}
		}
	}

	for {
		for {
			select {
			case request := <-g.incoming:
				if request.op.priority == egressBackground {
					background = append(background, request)
				} else {
					foreground = append(foreground, request)
				}
			default:
				goto drained
			}
		}
	drained:
		foreground = pruneCanceledEgressRequests(foreground)
		background = pruneCanceledEgressRequests(background)
		now := g.clock.Now()
		writeAttempts = pruneEgressAttempts(writeAttempts, now, defaultTelegramGroupWriteWindow)
		backgroundAttempts = pruneEgressAttempts(backgroundAttempts, now, defaultTelegramGroupWriteWindow)
		windowGate := nextEgressWindowAt(writeAttempts, defaultTelegramGroupWriteLimit, defaultTelegramGroupWriteWindow)
		foregroundGate := latestTime(latestTime(nextWriteAt, windowGate), g.cooldownUntil())
		backgroundWindowGate := nextEgressWindowAt(backgroundAttempts, defaultTelegramBackgroundWriteLimit, defaultTelegramGroupWriteWindow)
		backgroundGate := latestTime(foregroundGate, backgroundWindowGate)
		request, fromBackground := selectEgressRequest(now, foregroundGate, backgroundGate, foreground, background, foregroundStreak)
		if request != nil {
			if fromBackground {
				background = background[1:]
				foregroundStreak = 0
			} else {
				foreground = foreground[1:]
				foregroundStreak++
			}
			attemptAt := g.clock.Now()
			nextWriteAt = attemptAt.Add(g.interval)
			writeAttempts = append(writeAttempts, attemptAt)
			if fromBackground {
				backgroundAttempts = append(backgroundAttempts, attemptAt)
			}
			err := request.attempt()
			finishedAt := g.clock.Now()
			outcome := "success"
			if err != nil {
				outcome = "error"
			}
			if delay, ok := telegramRetryAfter(err); ok {
				outcome = "retry_after"
				retryAt := g.clock.Now().Add(delay)
				g.noteCooldown(retryAt)
				g.observeAttempt(request, attemptAt, finishedAt, outcome)
				if request.op.retry429 && request.ctx.Err() == nil {
					request.queuedAt = g.clock.Now()
					request.notBefore = retryAt
					if fromBackground {
						background = append([]*egressRequest{request}, background...)
					} else {
						foreground = append([]*egressRequest{request}, foreground...)
					}
					continue
				}
			} else {
				g.observeAttempt(request, attemptAt, finishedAt, outcome)
			}
			finishEgressRequest(request, err)
			continue
		}

		waitUntil, hasPending := nextEgressReadyAt(foregroundGate, backgroundGate, foreground, background)
		if !hasPending {
			select {
			case <-ctx.Done():
				finishPending(ctx.Err())
				return
			case request := <-g.incoming:
				if request.op.priority == egressBackground {
					background = append(background, request)
				} else {
					foreground = append(foreground, request)
				}
			case <-g.wake:
			}
			continue
		}

		delay := waitUntil.Sub(g.clock.Now())
		if delay < 0 {
			delay = 0
		}
		select {
		case <-ctx.Done():
			finishPending(ctx.Err())
			return
		case request := <-g.incoming:
			if request.op.priority == egressBackground {
				background = append(background, request)
			} else {
				foreground = append(foreground, request)
			}
		case <-g.clock.After(delay):
		case <-g.wake:
		}
	}
}

func pruneEgressAttempts(attempts []time.Time, now time.Time, window time.Duration) []time.Time {
	firstActive := 0
	for firstActive < len(attempts) && !attempts[firstActive].Add(window).After(now) {
		firstActive++
	}
	return attempts[firstActive:]
}

func nextEgressWindowAt(attempts []time.Time, limit int, window time.Duration) time.Time {
	if limit <= 0 || len(attempts) < limit {
		return time.Time{}
	}
	return attempts[len(attempts)-limit].Add(window)
}

func selectEgressRequest(now, foregroundGate, backgroundGate time.Time, foreground, background []*egressRequest, foregroundStreak int) (*egressRequest, bool) {
	foregroundReady := len(foreground) > 0 && !now.Before(latestTime(foregroundGate, foreground[0].notBefore))
	backgroundReady := len(background) > 0 && !now.Before(latestTime(backgroundGate, background[0].notBefore))
	if backgroundReady && (!foregroundReady || foregroundStreak >= foregroundWriteBurst) {
		return background[0], true
	}
	if foregroundReady {
		return foreground[0], false
	}
	if backgroundReady {
		return background[0], true
	}
	return nil, false
}

func nextEgressReadyAt(foregroundGate, backgroundGate time.Time, foreground, background []*egressRequest) (time.Time, bool) {
	var next time.Time
	for index, queue := range [][]*egressRequest{foreground, background} {
		if len(queue) == 0 {
			continue
		}
		gate := foregroundGate
		if index == 1 {
			gate = backgroundGate
		}
		readyAt := latestTime(gate, queue[0].notBefore)
		if next.IsZero() || readyAt.Before(next) {
			next = readyAt
		}
	}
	return next, !next.IsZero()
}

func (g *egressGovernor) observeAttempt(request *egressRequest, startedAt, finishedAt time.Time, outcome string) {
	if g == nil || g.observe == nil || request == nil {
		return
	}
	priority := "foreground"
	if request.op.priority == egressBackground {
		priority = "background"
	}
	g.observe(egressObservation{
		Operation:   request.op.name,
		Priority:    priority,
		QueueWait:   maxDuration(0, startedAt.Sub(request.queuedAt)),
		APIDuration: maxDuration(0, finishedAt.Sub(startedAt)),
		Outcome:     outcome,
	})
}

func maxDuration(left, right time.Duration) time.Duration {
	if right > left {
		return right
	}
	return left
}

func pruneCanceledEgressRequests(queue []*egressRequest) []*egressRequest {
	for len(queue) > 0 && queue[0].ctx.Err() != nil {
		finishEgressRequest(queue[0], queue[0].ctx.Err())
		queue = queue[1:]
	}
	return queue
}

func finishEgressRequest(request *egressRequest, err error) {
	if request == nil {
		return
	}
	select {
	case request.result <- err:
	default:
	}
}

func (g *egressGovernor) noteCooldown(until time.Time) {
	if g == nil || until.IsZero() {
		return
	}
	g.mu.Lock()
	if until.After(g.cooldown) {
		g.cooldown = until
	}
	g.mu.Unlock()
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *egressGovernor) cooldownUntil() time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cooldown
}

func telegramRetryAfter(err error) (time.Duration, bool) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	if apiErr.Code != http.StatusTooManyRequests && apiErr.HTTPStatus != http.StatusTooManyRequests {
		return 0, false
	}
	if apiErr.RetryAfter <= 0 {
		return defaultTelegramRetryAfter, true
	}
	return time.Duration(apiErr.RetryAfter) * time.Second, true
}

func latestTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}
