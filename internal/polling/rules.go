package polling

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/model"
	"slices"
	"time"
)

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
	client := c
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
	out.Waiting = true
	out.Terminal = true
	var failures []error
	for _, r := range cfg.Rules {
		if r.Type == TypeCron {
			out.Terminal = false
		}
		runtime := state.Rules[r.ID]
		// Re-entry increments generation, so old deadlines must not delay a new arm.
		due, err := time.Parse(time.RFC3339Nano, string(runtime.NextCheckAt))
		if r.Type == TypeCron && state.Generation == p.Generation && err == nil && due.After(now) {
			if due.Before(next) {
				next = due
			}
			continue
		}
		leaf := r.Polling(p)
		leaf.Cursor = runtime.Cursor
		result, err := client.Check(ctx, leaf, now)
		if err != nil {
			out.Terminal = false
			failures = append(failures, fmt.Errorf("rule %s: %w", r.ID, err))
			continue
		}
		out.Terminal = out.Terminal && result.Terminal
		if !result.Waiting {
			out.Waiting = false
		}
		out.DiagnosticNew = out.DiagnosticNew || result.DiagnosticNew
		if result.DiagnosticError != "" {
			out.DiagnosticError = result.DiagnosticError
		}
		if result.ObservationAt > out.ObservationAt {
			out.ObservationAt = result.ObservationAt
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
	if out.Terminal {
		out.NextCheckAt = ""
	}
	if len(out.Events) == 0 && len(failures) > 0 {
		out.Cursor = p.Cursor
		return out, errors.Join(failures...)
	}
	return out, nil
}
