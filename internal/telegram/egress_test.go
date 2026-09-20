package telegram

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type fakeEgressTimer struct {
	at time.Time
	ch chan time.Time
}

type fakeEgressClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []fakeEgressTimer
}

func newFakeEgressClock() *fakeEgressClock {
	return &fakeEgressClock{now: time.Unix(1_000, 0)}
}

func (c *fakeEgressClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeEgressClock) After(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	at := c.now.Add(delay)
	if delay <= 0 {
		ch <- c.now
		return ch
	}
	c.timers = append(c.timers, fakeEgressTimer{at: at, ch: ch})
	return ch
}

func (c *fakeEgressClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	now := c.now
	pending := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(now) {
			timer.ch <- now
			continue
		}
		pending = append(pending, timer)
	}
	c.timers = pending
	c.mu.Unlock()
}

func (c *fakeEgressClock) timerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

func TestEgressGovernorSpacesEveryRawGroupWrite(t *testing.T) {
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(defaultTelegramGroupWriteInterval, clock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	governor.Start(ctx)

	firstStarted := make(chan struct{}, 1)
	secondStarted := make(chan struct{}, 1)
	firstDone := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
		firstStarted <- struct{}{}
		return nil
	})
	receiveSignal(t, firstStarted)
	if err := receiveError(t, firstDone); err != nil {
		t.Fatal(err)
	}

	secondDone := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
		secondStarted <- struct{}{}
		return nil
	})
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })
	assertNoSignal(t, secondStarted)
	clock.Advance(defaultTelegramGroupWriteInterval)
	receiveSignal(t, secondStarted)
	if err := receiveError(t, secondDone); err != nil {
		t.Fatal(err)
	}
}

func TestEgressGovernorLimitsRawGroupWritesPerRollingMinute(t *testing.T) {
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(defaultTelegramGroupWriteInterval, clock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	governor.Start(ctx)

	for index := 0; index < defaultTelegramGroupWriteLimit; index++ {
		started := make(chan struct{}, 1)
		done := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
			started <- struct{}{}
			return nil
		})
		if index > 0 {
			waitForCondition(t, func() bool { return clock.timerCount() > 0 })
			assertNoSignal(t, started)
			clock.Advance(defaultTelegramGroupWriteInterval)
		}
		receiveSignal(t, started)
		if err := receiveError(t, done); err != nil {
			t.Fatal(err)
		}
	}

	twentyFirstStarted := make(chan struct{}, 1)
	twentyFirstDone := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
		twentyFirstStarted <- struct{}{}
		return nil
	})
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })
	assertNoSignal(t, twentyFirstStarted)

	waitRemaining := defaultTelegramGroupWriteWindow - time.Duration(defaultTelegramGroupWriteLimit-1)*defaultTelegramGroupWriteInterval
	clock.Advance(waitRemaining - time.Millisecond)
	assertNoSignal(t, twentyFirstStarted)
	clock.Advance(time.Millisecond)
	receiveSignal(t, twentyFirstStarted)
	if err := receiveError(t, twentyFirstDone); err != nil {
		t.Fatal(err)
	}
}

func TestEgressGovernorBackgroundCapDoesNotBlockForeground(t *testing.T) {
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(defaultTelegramGroupWriteInterval, clock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	governor.Start(ctx)

	for index := 0; index < defaultTelegramBackgroundWriteLimit; index++ {
		started := make(chan struct{}, 1)
		done := runEgressOperation(governor, ctx, egressOperation{priority: egressBackground, retry429: true}, func() error {
			started <- struct{}{}
			return nil
		})
		if index > 0 {
			waitForCondition(t, func() bool { return clock.timerCount() > 0 })
			clock.Advance(defaultTelegramGroupWriteInterval)
		}
		receiveSignal(t, started)
		if err := receiveError(t, done); err != nil {
			t.Fatal(err)
		}
	}

	backgroundStarted := make(chan struct{}, 1)
	backgroundDone := runEgressOperation(governor, ctx, egressOperation{priority: egressBackground, retry429: true}, func() error {
		backgroundStarted <- struct{}{}
		return nil
	})
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })

	foregroundStarted := make(chan struct{}, 1)
	foregroundDone := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
		foregroundStarted <- struct{}{}
		return nil
	})
	clock.Advance(defaultTelegramGroupWriteInterval)
	receiveSignal(t, foregroundStarted)
	assertNoSignal(t, backgroundStarted)
	if err := receiveError(t, foregroundDone); err != nil {
		t.Fatal(err)
	}

	remaining := defaultTelegramGroupWriteWindow - time.Duration(defaultTelegramBackgroundWriteLimit)*defaultTelegramGroupWriteInterval
	clock.Advance(remaining)
	receiveSignal(t, backgroundStarted)
	if err := receiveError(t, backgroundDone); err != nil {
		t.Fatal(err)
	}
}

func TestEgressGovernorObservesQueueWaitByOperation(t *testing.T) {
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(defaultTelegramGroupWriteInterval, clock)
	observed := make(chan egressObservation, 2)
	governor.observe = func(value egressObservation) { observed <- value }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	governor.Start(ctx)

	first := runEgressOperation(governor, ctx, egressOperation{name: egressOperationSendMessage, priority: egressForeground, retry429: true}, func() error { return nil })
	if err := receiveError(t, first); err != nil {
		t.Fatal(err)
	}
	<-observed

	second := runEgressOperation(governor, ctx, egressOperation{name: egressOperationDeleteForumTopic, priority: egressBackground, retry429: true}, func() error { return nil })
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })
	clock.Advance(defaultTelegramGroupWriteInterval)
	if err := receiveError(t, second); err != nil {
		t.Fatal(err)
	}
	got := <-observed
	if got.Operation != egressOperationDeleteForumTopic || got.Priority != "background" || got.QueueWait != defaultTelegramGroupWriteInterval {
		t.Fatalf("observation = %#v", got)
	}
}

func TestEgressGovernorBoundsForegroundPriority(t *testing.T) {
	governor := newEgressGovernor(0)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	governor.Start(ctx)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var mu sync.Mutex
	order := make([]string, 0, 6)
	blockerDone := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
		started <- struct{}{}
		<-release
		mu.Lock()
		order = append(order, "foreground-0")
		mu.Unlock()
		return nil
	})
	receiveSignal(t, started)

	done := make([]<-chan error, 0, 5)
	for index := 1; index <= 4; index++ {
		label := "foreground-" + string(rune('0'+index))
		done = append(done, runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
			mu.Lock()
			order = append(order, label)
			mu.Unlock()
			return nil
		}))
	}
	done = append(done, runEgressOperation(governor, ctx, egressOperation{priority: egressBackground, retry429: true}, func() error {
		mu.Lock()
		order = append(order, "background")
		mu.Unlock()
		return nil
	}))
	waitForCondition(t, func() bool { return len(governor.incoming) == 5 })
	close(release)
	if err := receiveError(t, blockerDone); err != nil {
		t.Fatal(err)
	}
	for _, result := range done {
		if err := receiveError(t, result); err != nil {
			t.Fatal(err)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	backgroundIndex := -1
	for index, label := range order {
		if label == "background" {
			backgroundIndex = index
			break
		}
	}
	if backgroundIndex != 4 {
		t.Fatalf("write order = %#v, want background after four foreground attempts", order)
	}
}

func TestEgressGovernorRetries429AndLetsCriticalControlBypassCooldown(t *testing.T) {
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(0, clock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	governor.Start(ctx)

	var attempts int
	firstAttempted := make(chan struct{}, 1)
	retried := make(chan struct{}, 1)
	groupDone := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
		attempts++
		if attempts == 1 {
			firstAttempted <- struct{}{}
			return &APIError{Method: "editMessageText", Code: 429, RetryAfter: 7}
		}
		retried <- struct{}{}
		return nil
	})
	receiveSignal(t, firstAttempted)
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })
	assertNoSignal(t, retried)

	criticalCalled := false
	if err := governor.DoCritical(ctx, func() error {
		criticalCalled = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !criticalCalled {
		t.Fatal("critical callback did not bypass group cooldown")
	}

	clock.Advance(7 * time.Second)
	receiveSignal(t, retried)
	if err := receiveError(t, groupDone); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestEgressGovernorReturnOn429KeepsCooldownForLaterWrites(t *testing.T) {
	clock := newFakeEgressClock()
	governor := newEgressGovernorWithClock(0, clock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	governor.Start(ctx)

	err := receiveError(t, runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: false}, func() error {
		return &APIError{Method: "createForumTopic", Code: 429, RetryAfter: 5}
	}))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RetryAfter != 5 {
		t.Fatalf("returned error = %v, want original 429", err)
	}

	laterStarted := make(chan struct{}, 1)
	laterDone := runEgressOperation(governor, ctx, egressOperation{priority: egressForeground, retry429: true}, func() error {
		laterStarted <- struct{}{}
		return nil
	})
	waitForCondition(t, func() bool { return clock.timerCount() > 0 })
	assertNoSignal(t, laterStarted)
	clock.Advance(5 * time.Second)
	receiveSignal(t, laterStarted)
	if err := receiveError(t, laterDone); err != nil {
		t.Fatal(err)
	}
}

func runEgressOperation(governor *egressGovernor, ctx context.Context, operation egressOperation, attempt func() error) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- governor.Do(ctx, operation, attempt)
	}()
	return done
}

func receiveSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for signal")
	}
}

func assertNoSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("unexpected signal")
	case <-time.After(20 * time.Millisecond):
	}
}

func receiveError(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for result")
		return nil
	}
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not met")
}
