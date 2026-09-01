package telegram

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

const (
	defaultTelegramGroupWriteInterval = time.Second
	defaultTelegramGroupWriteLimit    = 20
	defaultTelegramGroupWriteWindow   = 60*time.Second + 250*time.Millisecond
	defaultTelegramRetryAfter         = 5 * time.Second
	foregroundWriteBurst              = 4
)

type egressPriority uint8

const (
	egressForeground egressPriority = iota
	egressBackground
)

type egressOperation struct {
	priority egressPriority
	retry429 bool
}

type egressRequest struct {
	ctx       context.Context
	op        egressOperation
	attempt   func() error
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
	request := &egressRequest{ctx: ctx, op: operation, attempt: attempt, result: make(chan error, 1)}
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
		windowGate := nextEgressWindowAt(writeAttempts, defaultTelegramGroupWriteLimit, defaultTelegramGroupWriteWindow)
		gate := latestTime(latestTime(nextWriteAt, windowGate), g.cooldownUntil())
		request, fromBackground := selectEgressRequest(now, gate, foreground, background, foregroundStreak)
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
			err := request.attempt()
			if delay, ok := telegramRetryAfter(err); ok {
				retryAt := g.clock.Now().Add(delay)
				g.noteCooldown(retryAt)
				if request.op.retry429 && request.ctx.Err() == nil {
					request.notBefore = retryAt
					if fromBackground {
						background = append([]*egressRequest{request}, background...)
					} else {
						foreground = append([]*egressRequest{request}, foreground...)
					}
					continue
				}
			}
			finishEgressRequest(request, err)
			continue
		}

		waitUntil, hasPending := nextEgressReadyAt(gate, foreground, background)
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

func selectEgressRequest(now, gate time.Time, foreground, background []*egressRequest, foregroundStreak int) (*egressRequest, bool) {
	if now.Before(gate) {
		return nil, false
	}
	foregroundReady := len(foreground) > 0 && !now.Before(foreground[0].notBefore)
	backgroundReady := len(background) > 0 && !now.Before(background[0].notBefore)
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

func nextEgressReadyAt(gate time.Time, foreground, background []*egressRequest) (time.Time, bool) {
	var next time.Time
	for _, queue := range [][]*egressRequest{foreground, background} {
		if len(queue) == 0 {
			continue
		}
		readyAt := latestTime(gate, queue[0].notBefore)
		if next.IsZero() || readyAt.Before(next) {
			next = readyAt
		}
	}
	return next, !next.IsZero()
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
