package polling

import (
	"context"
	"encoding/json"
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/model"
	"slices"
	"strconv"
	"time"
)

func (c *Checker) Prepare(ctx context.Context, tasks []model.Task, footers map[int64]string) error {
	reader, ok := c.pr.(*arcanumreview.SharedReader)
	if !ok {
		return nil
	}
	d := arcanumreview.Demand{PRs: map[int64]arcanumreview.PRNeed{}}
	for id, author := range footers {
		d.PRs[id] = arcanumreview.PRNeed{Author: author}
	}
	add := func(id int64, author string, comments, checks bool) {
		n := d.PRs[id]
		if author != "" {
			n.Author = author
		}
		n.Comments = n.Comments || comments
		n.Checks = n.Checks || checks
		d.PRs[id] = n
	}
	var visit func(model.TaskPolling)
	visit = func(p model.TaskPolling) {
		if c.Login != nil {
			var err error
			p, err = WithLogin(p, *c.Login)
			if err != nil {
				return
			}
		}
		if p.Type == model.PollingRules {
			var cfg model.PollingRulesConfig
			if json.Unmarshal(p.Config, &cfg) != nil {
				return
			}
			for _, r := range cfg.Rules {
				visit(r.Polling(p))
			}
			return
		}
		cfg, err := DecodeConfig(p)
		if err != nil {
			return
		}
		terms := conditions(cfg)
		comments := slices.Contains(terms, ConditionAnswered) || slices.Contains(terms, ConditionResolved)
		checks := cfg.CheckMergeFailures || slices.Contains(terms, ConditionMergeChecks)
		switch p.Type {
		case TypePRWait:
			c.armAt(p, cfg, time.Now().UTC())
			id, _ := strconv.ParseInt(cfg.PRID, 10, 64)
			if id > 0 {
				add(id, cfg.Author, comments, checks)
			}
		case TypeReview, TypeChanges:
			var prs []arcanumreview.PullRequest
			q := arcanumreview.AssignedQuery(cfg.Login)
			if p.Type == TypeReview {
				prs, _ = reader.ListAssigned(ctx, cfg.Login)
			} else {
				q = arcanumreview.ChangesQuery(cfg.Login, checks)
				if checks {
					prs, _ = reader.ListAuthoredOpen(ctx, cfg.Login)
				} else {
					prs, _ = reader.ListAuthoredWaitingForChanges(ctx, cfg.Login)
				}
			}
			if !slices.Contains(d.Queries, q) {
				d.Queries = append(d.Queries, q)
			}
			var cursor struct {
				Seen []int64 `json:"seen"`
			}
			_ = json.Unmarshal(p.Cursor, &cursor)
			// Only a configured comment wait needs an initial comment baseline.
			childComments := childNeedsComments(p)
			for _, pr := range prs {
				if slices.Contains(cursor.Seen, pr.ID) || (p.Type == TypeReview && pr.Author == cfg.Login) {
					continue
				}
				add(pr.ID, pr.Author, childComments, checks)
			}
		}
	}
	for _, t := range tasks {
		if t.Polling != nil && t.Column == model.TaskPollingColumn {
			visit(*t.Polling)
		}
	}
	slices.Sort(d.Queries)
	return reader.SetDemand(ctx, d)
}
