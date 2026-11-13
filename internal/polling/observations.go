package polling

import (
	"context"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"slices"
	"strconv"
	"sync"
	"time"
)

type armState struct {
	generation int64
	at         time.Time
}
type armTimes struct {
	sync.Mutex
	values map[string]armState
}

func (c *Checker) armAt(p model.TaskPolling, cfg Config, now time.Time) time.Time {
	c.arms.Lock()
	defer c.arms.Unlock()
	if c.arms.values == nil {
		c.arms.values = map[string]armState{}
	}
	key := p.TaskID + ":" + cfg.PRID
	a, ok := c.arms.values[key]
	if !ok || a.generation != p.Generation {
		a = armState{p.Generation, now}
		c.arms.values[key] = a
	}
	return a.at
}
func (c *Checker) Changes() <-chan struct{} {
	if r, ok := c.pr.(*arcanumreview.SharedReader); ok {
		return r.Changed()
	}
	return nil
}

// applyCoverage is pure comparison scheduling. It cannot start network reads.
func (c *Checker) applyCoverage(ctx context.Context, p model.TaskPolling, cfg Config, old *PRCursor, s *arcanumreview.PRSnapshot, now time.Time, out *model.TaskPollingOutcome) bool {
	if s.Coverage == nil {
		return true
	}
	if s.Status == "merged" {
		out.Terminal = true
		out.NextCheckAt = ""
		return true
	}
	cov := s.Coverage
	commentGen := old.Generation
	if old.CommentGeneration != nil {
		commentGen = *old.CommentGeneration
	}
	if commentGen != p.Generation && cov.CommentsAt.Before(c.armAt(p, cfg, now)) {
		cov.Comments = false
	}
	groups := cfg.ConditionGroups
	if len(groups) == 0 {
		for _, term := range conditions(cfg) {
			groups = append(groups, []string{term})
		}
	}
	ready := false
	at := time.Time{}
	next := now.Add(arcanumreview.InfoInterval)
	cov.Groups = map[string]bool{}
	for _, g := range groups {
		covered := true
		for _, term := range g {
			if (term == ConditionAnswered || term == ConditionResolved) && !cov.Comments {
				covered = false
			}
			if term == ConditionMergeChecks && !cov.Checks {
				covered = false
			}
		}
		when := observationTime(g, *s)
		key := groupKey(g)
		observed, _ := time.Parse(time.RFC3339Nano, old.Observations[key])
		fresh := covered && !when.IsZero() && when.After(observed)
		cov.Groups[key] = fresh
		if fresh {
			ready = true
			if when.After(at) {
				at = when
			}
		}
		period := arcanumreview.SearchInterval
		if slices.Contains(g, ConditionAnswered) || slices.Contains(g, ConditionResolved) || slices.Contains(g, ConditionMergeChecks) {
			period = arcanumreview.SlowInterval
		}
		due := when.Add(period)
		if when.IsZero() || due.Before(now) {
			due = now.Add(period)
		}
		if due.Before(next) {
			next = due
		}
	}
	if reader, ok := c.pr.(*arcanumreview.SharedReader); ok {
		id, _ := strconv.ParseInt(cfg.PRID, 10, 64)
		if due, err := reader.NextSlowAt(ctx, id); err == nil && !due.IsZero() {
			searchOnly := false
			for _, g := range groups {
				if !slices.Contains(g, ConditionAnswered) && !slices.Contains(g, ConditionResolved) && !slices.Contains(g, ConditionMergeChecks) {
					searchOnly = true
				}
			}
			if !searchOnly {
				next = due
			}
		}
	}
	out.NextCheckAt = timestamp(next)
	if old.ObservedErrors == nil {
		old.ObservedErrors = map[string]string{}
	}
	recordError := func(resource, problem string, at time.Time) {
		if problem == "" || at.IsZero() {
			return
		}
		stamp := at.UTC().Format(time.RFC3339Nano)
		if old.ObservedErrors[resource] == stamp {
			return
		}
		old.ObservedErrors[resource] = stamp
		out.DiagnosticNew = true
		out.DiagnosticError = problem
		out.ObservationAt = timestamp(at)
	}
	if cov.CommentsError != "" {
		out.DiagnosticError = cov.CommentsError
	}
	if cov.ChecksError != "" {
		out.DiagnosticError = cov.ChecksError
	}
	recordError("comments", cov.CommentsError, cov.CommentsAt)
	recordError("checks", cov.ChecksError, cov.ChecksAt)
	if out.DiagnosticNew {
		out.Cursor = jsonBytes(old)
	}

	if !ready {
		out.Waiting = true
		return false
	}
	if at.After(parseObservationTime(out.ObservationAt)) {
		out.ObservationAt = timestamp(at)
	}

	return true
}

func pendingOutcome(out model.TaskPollingOutcome, err error, now time.Time) (model.TaskPollingOutcome, error) {
	if pending, ok := err.(*arcanumreview.PendingRead); ok {
		out.Waiting = true
		if pending.Cause != nil {
			out.DiagnosticNew = true
			out.DiagnosticError = pending.Cause.Error()
			out.ObservationAt = timestamp(pending.AttemptAt)
		}
		next := pending.NextAt
		if next.IsZero() {
			next = now.Add(arcanumreview.SearchInterval)
		}
		out.NextCheckAt = timestamp(next)
		return out, nil
	}
	return out, err
}
func childNeedsComments(p model.TaskPolling) bool {
	b, _ := json.Marshal(p.Action.ChildDefinition)
	var v any
	_ = json.Unmarshal(b, &v)
	return containsCommentCondition(v)
}
func containsCommentCondition(v any) bool {
	switch x := v.(type) {
	case string:
		return x == ConditionAnswered || x == ConditionResolved
	case []any:
		for _, v := range x {
			if containsCommentCondition(v) {
				return true
			}
		}
	case map[string]any:
		for _, v := range x {
			if containsCommentCondition(v) {
				return true
			}
		}
	}
	return false
}

// observationPending is used for required Source baselines, never a hidden read.
func observationPending(ctx context.Context, s arcanumreview.PRSnapshot, comments, checks bool) bool {
	_ = ctx
	return s.Coverage != nil && ((comments && !s.Coverage.Comments) || (checks && !s.Coverage.Checks))
}

func parseObservationTime(value model.TimeString) time.Time {
	at, _ := time.Parse(time.RFC3339Nano, string(value))
	return at
}

// Persist error watermarks separately from facts and accepted group matches.
// Re-evaluating an unchanged failed observation is not another IO attempt.
func finishObservation(p model.TaskPolling, out model.TaskPollingOutcome) model.TaskPollingOutcome {
	if out.DiagnosticError == "" || out.ObservationAt == "" || p.Type == model.PollingRules {
		return out
	}
	var cursor map[string]json.RawMessage
	if json.Unmarshal(out.Cursor, &cursor) != nil || cursor == nil {
		return out
	}
	var previous string
	_ = json.Unmarshal(cursor["read_error_at"], &previous)
	out.DiagnosticNew = previous != string(out.ObservationAt)
	if out.DiagnosticNew {
		cursor["read_error_at"] = jsonBytes(out.ObservationAt)
		out.Cursor = jsonBytes(cursor)
	}
	return out
}
