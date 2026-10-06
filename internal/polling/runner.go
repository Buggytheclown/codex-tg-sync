package polling

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mideco-tech/codex-tg/internal/model"
)

type PollingStore interface {
	ListTasks(context.Context) ([]model.Task, error)
	ListDueTaskPolling(context.Context, time.Time, int) ([]model.TaskPolling, error)
	AcceptTaskPollingOutcome(context.Context, model.TaskPollingOutcome, int64) (bool, error)
}
type PollingChecker interface {
	Check(context.Context, model.TaskPolling, time.Time) (model.TaskPollingOutcome, error)
}
type PollingStatus interface {
	RegisterTaskPoller(context.Context, string, string, time.Duration, bool) error
	NoteExternalPollStarted(context.Context, string)
	NoteExternalPollResult(context.Context, string, error)
}

// Runner checks bounded batches serially. SQLite generation guards accept each
// outcome only while its original configuration still owns the polling task.
type Runner struct {
	store          PollingStore
	checker        PollingChecker
	status         PollingStatus
	topicID        int64
	now            func() time.Time
	lastFinishedAt time.Time
	cronReadyAt    time.Time
}

func NewRunner(store PollingStore, checker PollingChecker, topicID int64) *Runner {
	r := &Runner{store: store, checker: checker, topicID: topicID, now: time.Now}
	r.status, _ = store.(PollingStatus)
	return r
}
func sourceName(p model.TaskPolling) string { return "board.task." + p.TaskID }
func (r *Runner) PollOnce(ctx context.Context) error {
	now := r.now().UTC()
	if r.lastFinishedAt.IsZero() || now.Before(r.lastFinishedAt) || now.Sub(r.lastFinishedAt) > 90*time.Second {
		r.cronReadyAt = now.Add(4 * time.Minute)
	}
	defer func() { r.lastFinishedAt = r.now().UTC() }()
	if r.status != nil {
		tasks, err := r.store.ListTasks(ctx)
		if err != nil {
			return err
		}
		for _, task := range tasks {
			if task.Polling == nil {
				continue
			}
			interval := time.Minute
			if cfg, err := DecodeConfig(*task.Polling); err == nil {
				interval = time.Duration(cfg.IntervalSeconds) * time.Second
			}
			if err := r.status.RegisterTaskPoller(ctx, task.ID, task.Title, interval, task.Column == model.TaskPollingColumn); err != nil {
				return err
			}
		}
	}
	due, err := r.store.ListDueTaskPolling(ctx, now, 20)
	if err != nil {
		return err
	}
	var failures []error
	for _, p := range due {
		if ctx.Err() != nil {
			return errors.Join(append(failures, ctx.Err())...)
		}
		if p.Type == TypeCron && now.Before(r.cronReadyAt) {
			continue
		}
		source := sourceName(p)
		if r.status != nil {
			r.status.NoteExternalPollStarted(ctx, source)
		}
		checkCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		out, checkErr := r.checker.Check(checkCtx, p, now)
		cancel()
		out.TaskID, out.Generation, out.CheckedAt = p.TaskID, p.Generation, timestamp(now)
		if out.NextCheckAt == "" {
			out.NextCheckAt = timestamp(now.Add(time.Minute))
		}
		if checkErr != nil {
			// A partial network pass cannot consume events or advance a source cursor.
			out.Events, out.Cursor, out.Error = nil, p.Cursor, checkErr.Error()
		}
		_, acceptErr := r.store.AcceptTaskPollingOutcome(ctx, out, r.topicID)
		resultErr := errors.Join(checkErr, acceptErr)
		if r.status != nil {
			r.status.NoteExternalPollResult(ctx, source, resultErr)
		}
		if resultErr != nil {
			failures = append(failures, fmt.Errorf("%s: %w", source, resultErr))
		}
	}
	return errors.Join(failures...)
}
func (r *Runner) Run(ctx context.Context, onResult func(error)) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		err := r.PollOnce(ctx)
		if onResult != nil {
			onResult(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
