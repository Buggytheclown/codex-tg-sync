package polling

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"slices"
	"time"
)

// Cache lives for one task check; same-PR rules observe the same response.
type rulePRClient struct {
	PRClient
	snapshots map[int64]arcanumreview.PRSnapshot
	checks    map[int64][]arcanumreview.PRCheck
}

func (c *rulePRClient) ReadPRSnapshot(ctx context.Context, id int64) (arcanumreview.PRSnapshot, error) {
	if s, ok := c.snapshots[id]; ok {
		return s, nil
	}
	s, err := c.PRClient.ReadPRSnapshot(ctx, id)
	if err == nil {
		c.snapshots[id] = s
	}
	return s, err
}
func (c *rulePRClient) ReadPRChecks(ctx context.Context, id int64) ([]arcanumreview.PRCheck, error) {
	if s, ok := c.checks[id]; ok {
		return s, nil
	}
	s, err := c.PRClient.ReadPRChecks(ctx, id)
	if err == nil {
		c.checks[id] = s
	}
	return s, err
}
func (c *Checker) checkRules(ctx context.Context, p model.TaskPolling, now time.Time) (model.TaskPollingOutcome, error) {
	out := model.TaskPollingOutcome{TaskID: p.TaskID, Generation: p.Generation, Cursor: p.Cursor, CheckedAt: timestamp(now)}
	var cfg model.PollingRulesConfig
	if err := json.Unmarshal(p.Config, &cfg); err != nil {
		return out, err
	}
	state := model.PollingRulesCursor{Rules: map[string]model.PollingRuleState{}}
	if len(p.Cursor) > 0 {
		if err := json.Unmarshal(p.Cursor, &state); err != nil {
			return out, err
		}
		if state.Rules == nil {
			state.Rules = map[string]model.PollingRuleState{}
		}
	}
	client := &Checker{}
	if c != nil && c.pr != nil {
		client.pr = &rulePRClient{PRClient: c.pr, snapshots: map[int64]arcanumreview.PRSnapshot{}, checks: map[int64][]arcanumreview.PRCheck{}}
	}
	// Done wins ties. Stable sorting preserves the user's order otherwise.
	slices.SortStableFunc(cfg.Rules, func(a, b model.PollingRule) int {
		if (a.Action.Column == model.TaskDone) == (b.Action.Column == model.TaskDone) {
			return 0
		}
		if a.Action.Column == model.TaskDone {
			return -1
		}
		return 1
	})
	next := now.Add(24 * time.Hour)
	for _, r := range cfg.Rules {
		runtime := state.Rules[r.ID]
		// Re-entry increments generation, so old deadlines must not delay a new arm.
		due, err := time.Parse(time.RFC3339Nano, string(runtime.NextCheckAt))
		if state.Generation == p.Generation && err == nil && due.After(now) {
			if due.Before(next) {
				next = due
			}
			continue
		}
		leaf := r.Polling(p)
		leaf.Cursor = runtime.Cursor
		result, err := client.Check(ctx, leaf, now)
		if err != nil {
			return out, fmt.Errorf("rule %s: %w", r.ID, err)
		}
		state.Rules[r.ID] = model.PollingRuleState{Cursor: result.Cursor, NextCheckAt: result.NextCheckAt}
		if n, err := time.Parse(time.RFC3339Nano, string(result.NextCheckAt)); err == nil && n.Before(next) {
			next = n
		}
		if len(result.Events) > 0 {
			for i := range result.Events {
				result.Events[i].Key = "rule:" + r.ID + ":" + result.Events[i].Key
			}
			out.RuleID, out.Events = r.ID, result.Events
			break
		}
	}
	// Prune deleted rule state, and persist one generation fence for all deadlines.
	keep := map[string]bool{}
	for _, r := range cfg.Rules {
		keep[r.ID] = true
	}
	for id := range state.Rules {
		if !keep[id] {
			delete(state.Rules, id)
		}
	}
	state.Generation = p.Generation
	out.Cursor = jsonBytes(state)
	out.NextCheckAt = timestamp(next)
	return out, nil
}
