package arcanumreview

import (
	"context"
	"errors"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

var retryAfterPattern = regexp.MustCompile(`(?i)retry[-_ ]after["\s:=]+([0-9]+)`)

type readBackoffError struct {
	err   error
	delay time.Duration
}

func (e *readBackoffError) Error() string             { return e.err.Error() }
func (e *readBackoffError) Unwrap() error             { return e.err }
func (e *readBackoffError) RetryAfter() time.Duration { return e.delay }
func commandBackoff(err error) error {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return err
	}
	match := retryAfterPattern.FindSubmatch(exit.Stderr)
	if len(match) == 2 {
		seconds, parseErr := strconv.ParseInt(string(match[1]), 10, 32)
		if parseErr == nil && seconds > 0 {
			return &readBackoffError{err: err, delay: time.Duration(seconds) * time.Second}
		}
	}
	return err
}
func (r *SharedReader) backoff(ctx context.Context, key string, err error) error {
	var retry interface{ RetryAfter() time.Duration }
	if !errors.As(err, &retry) || retry.RetryAfter() <= 0 {
		return nil
	}
	until := r.now().Add(retry.RetryAfter()).UTC()
	previous, readErr := r.deadline(ctx, key)
	if readErr != nil {
		return readErr
	}
	if !until.After(previous) {
		return nil
	}
	return r.store.SetState(ctx, "arc.cooldown."+key, until.Format(time.RFC3339Nano))
}
