package polling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"github.com/mideco-tech/codex-tg/internal/cronpoller"
	"github.com/mideco-tech/codex-tg/internal/model"
)

type PRClient interface {
	arcanumreview.AssignedClient
	arcanumreview.WaitingChangesClient
	ListAuthoredOpen(context.Context, string) ([]arcanumreview.PullRequest, error)
	ReadPRChecks(context.Context, int64) ([]arcanumreview.PRCheck, error)
	ReadPRSnapshot(context.Context, int64) (arcanumreview.PRSnapshot, error)
}
type Checker struct {
	pr    PRClient
	Login *string
}

func NewChecker(pr PRClient) *Checker { return &Checker{pr: pr} }
func timestamp(t time.Time) model.TimeString {
	return model.TimeString(t.UTC().Format(time.RFC3339Nano))
}
func jsonBytes(value any) json.RawMessage { b, _ := json.Marshal(value); return b }

func (c *Checker) Check(ctx context.Context, p model.TaskPolling, now time.Time) (model.TaskPollingOutcome, error) {
	out := model.TaskPollingOutcome{TaskID: p.TaskID, Generation: p.Generation, Cursor: p.Cursor, CheckedAt: timestamp(now), NextCheckAt: timestamp(now.Add(time.Minute))}
	if c != nil && c.Login != nil {
		var err error
		p, err = WithLogin(p, *c.Login)
		if err != nil {
			return out, err
		}
	}
	if err := Validate(p); err != nil {
		return out, err
	}
	if p.Type == model.PollingRules {
		return c.checkRules(ctx, p, now)
	}
	cfg, _ := DecodeConfig(p)
	if p.Type == TypeChanges && slices.Contains(conditions(cfg), ConditionMergeChecks) {
		cfg.CheckMergeFailures = true
	}
	out.NextCheckAt = timestamp(now.Add(time.Duration(cfg.IntervalSeconds) * time.Second))
	switch p.Type {
	case TypeCron:
		slot, due, err := cronpoller.DueOccurrence(cfg.Cron, cfg.Timezone, cfg.MaxLateness, now)
		if err != nil {
			return out, err
		}
		if !due {
			return out, nil
		}
		id := cfg.LegacyID
		if id == "" {
			id = p.TaskID
		}
		key := "cron:" + id + ":" + slot.Format("2006-01-02")
		out.Events = []model.PollingEvent{{Key: key, SourceKey: key, Title: "Scheduled task", Payload: jsonBytes(map[string]string{"scheduled_at": slot.Format(time.RFC3339), "timezone": cfg.Timezone}), Variables: map[string]string{"date": slot.Format("2006-01-02"), "scheduled_at": slot.Format(time.RFC3339)}}}
	case TypeReview, TypeChanges:
		if c == nil || c.pr == nil {
			return out, fmt.Errorf("Arcanum client unavailable")
		}
		var prs []arcanumreview.PullRequest
		var err error
		prefix := "arcanum_review:"
		if p.Type == TypeReview {
			prs, err = c.pr.ListAssigned(ctx, cfg.Login)
		} else {
			prefix = "arcanum_changes:"
			if cfg.CheckMergeFailures {
				prs, err = c.pr.ListAuthoredOpen(ctx, cfg.Login)
			} else {
				prs, err = c.pr.ListAuthoredWaitingForChanges(ctx, cfg.Login)
			}
		}
		if err != nil {
			return out, err
		}
		var cursor struct {
			Seen     []int64 `json:"seen"`
			NextPRID int64   `json:"next_pr_id,omitempty"`
		}
		if len(p.Cursor) > 0 && string(p.Cursor) != "{}" {
			if err := json.Unmarshal(p.Cursor, &cursor); err != nil {
				return out, fmt.Errorf("decode source cursor: %w", err)
			}
		}
		seen := map[int64]bool{}
		for _, id := range cursor.Seen {
			seen[id] = true
		}
		attempts := 0
		var readErrors []error
		start := 0
		for i, pr := range prs {
			if pr.ID == cursor.NextPRID {
				start = i
				break
			}
		}
		for n := 0; n < len(prs); n++ {
			pr := prs[(start+n)%len(prs)]
			if seen[pr.ID] || (p.Type == TypeReview && pr.Author == cfg.Login) {
				continue
			}
			if attempts >= 5 {
				cursor.NextPRID = pr.ID
				break
			}
			attempts++
			snapshot, err := c.pr.ReadPRSnapshot(ctx, pr.ID)
			if err != nil {
				readErrors = append(readErrors, err)
				continue
			}
			if snapshot.Status == "merged" {
				cursor.Seen = append(cursor.Seen, pr.ID)
				continue
			}
			if cfg.CheckMergeFailures {
				if snapshot.Status != "open" {
					continue
				}
				if err := c.readChecks(ctx, &snapshot); err != nil {
					readErrors = append(readErrors, err)
					continue
				}
				if snapshot.CommonStatus != "waiting_for_changes" && len(arcanumreview.FailedMergeChecks(snapshot.Checks)) == 0 {
					continue
				}
			}
			if p.Type == TypeChanges && len(cfg.ConditionGroups) > 0 {
				facts := []string{}
				if snapshot.CommonStatus == "waiting_for_changes" {
					facts = append(facts, ConditionChanges)
				}
				if len(arcanumreview.FailedMergeChecks(snapshot.Checks)) > 0 {
					facts = append(facts, ConditionMergeChecks)
				}
				if !groupsMatch(cfg.ConditionGroups, facts) {
					continue
				}
			}
			author := strings.TrimSpace(snapshot.Author.Name)
			if author == "" {
				author = pr.Author
			}
			id := strconv.FormatInt(pr.ID, 10)
			key := prefix + id
			url := "https://a.yandex-team.ru/review/" + id
			baseline := PRCursor{HeadCommit: snapshot.HeadCommit, CommonStatus: snapshot.CommonStatus, Since: now.UTC().Format(time.RFC3339Nano)}
			if cfg.CheckMergeFailures {
				baseline, _ = compareMergeChecks(baseline, snapshot)
			}
			for _, comment := range snapshot.Comments {
				if comment.Published() && comment.ReplyToID != 0 {
					baseline.ConsumedReplies = append(baseline.ConsumedReplies, comment.ID)
				}
			}
			// Remember currently open issues; a later pass may add more at explicit arm.
			for _, comment := range snapshot.Comments {
				if comment.Published() && comment.IssueStatus == "open" && (p.Type == TypeChanges || comment.Author.Name == cfg.Login) {
					baseline.Tracked = append(baseline.Tracked, comment.ID)
				}
			}
			out.Events = append(out.Events, model.PollingEvent{Key: key, SourceKey: key, Title: pr.Summary, SourceURL: url, Cursor: seedChildRules(p.Action.ChildDefinition, cfg.Login, snapshot, baseline), Payload: jsonBytes(map[string]string{"pr_id": id, "author": author, "head_commit": snapshot.HeadCommit, "common_status": snapshot.CommonStatus}), Variables: map[string]string{"pr_id": id, "pr_title": pr.Summary, "pr_url": url, "author": author, "login": cfg.Login}})
			if cfg.CheckMergeFailures {
				event := &out.Events[len(out.Events)-1]
				reasons := []string{}
				if snapshot.CommonStatus == "waiting_for_changes" {
					reasons = append(reasons, ConditionChanges)
				}
				failed := arcanumreview.FailedMergeChecks(snapshot.Checks)
				if len(failed) > 0 {
					reasons = append(reasons, ConditionMergeChecks)
				}
				event.Payload = jsonBytes(map[string]any{"pr_id": id, "author": author, "head_commit": snapshot.HeadCommit, "published_diff_id": snapshot.PublishedDiffID, "reasons": reasons, "failed_checks": failed})
			}
			cursor.Seen = append(cursor.Seen, pr.ID)
		}
		out.Cursor = jsonBytes(cursor)
		if len(readErrors) > 0 && (cfg.CheckMergeFailures || len(out.Events) == 0) {
			return out, errors.Join(readErrors...)
		}
	case TypePRWait:
		if c == nil || c.pr == nil {
			return out, fmt.Errorf("Arcanum client unavailable")
		}
		id, _ := strconv.ParseInt(cfg.PRID, 10, 64)
		current, err := c.pr.ReadPRSnapshot(ctx, id)
		if err != nil {
			return out, err
		}
		if current.Status == "merged" && len(cfg.ConditionGroups) == 0 {
			out.PRStatus = "merged"
			return out, nil
		}
		if slices.Contains(conditions(cfg), ConditionMergeChecks) {
			if err := c.readChecks(ctx, &current); err != nil {
				return out, err
			}
		}
		var old PRCursor
		if len(p.Cursor) > 0 {
			if err := json.Unmarshal(p.Cursor, &old); err != nil {
				return out, fmt.Errorf("decode PR cursor: %w", err)
			}
		}
		next, reasons := ComparePR(cfg, old, current, p.Generation)
		if len(reasons) > 0 || next.Since == "" {
			next.Since = now.UTC().Format(time.RFC3339Nano)
		}
		out.Cursor = jsonBytes(next)
		if len(reasons) == 0 {
			return out, nil
		}
		payload := jsonBytes(map[string]any{"pr_id": cfg.PRID, "author": current.Author.Name, "reasons": reasons, "head_commit": current.HeadCommit, "common_status": current.CommonStatus})
		if slices.Contains(conditions(cfg), ConditionMergeChecks) {
			payload = jsonBytes(map[string]any{"pr_id": cfg.PRID, "author": current.Author.Name, "reasons": reasons, "head_commit": current.HeadCommit, "published_diff_id": current.PublishedDiffID, "failed_checks": arcanumreview.FailedMergeChecks(current.Checks)})
		}
		hash := sha256.Sum256(append(append([]byte{}, payload...), out.Cursor...))
		key := fmt.Sprintf("wait:%d:%s", p.Generation, hex.EncodeToString(hash[:12]))
		out.Events = []model.PollingEvent{{Key: key, SourceURL: "https://a.yandex-team.ru/review/" + cfg.PRID, Payload: payload, Variables: map[string]string{"pr_id": cfg.PRID, "pr_url": "https://a.yandex-team.ru/review/" + cfg.PRID, "reasons": strings.Join(reasons, ", "), "author": current.Author.Name}}}
	}
	return out, nil
}

func (c *Checker) readChecks(ctx context.Context, snapshot *arcanumreview.PRSnapshot) error {
	if snapshot.PublishedDiffID == 0 {
		return nil
	}
	checks, err := c.pr.ReadPRChecks(ctx, snapshot.PublishedDiffID)
	if err != nil {
		return err
	}
	snapshot.Checks = checks
	return nil
}

func groupsMatch(groups [][]string, facts []string) bool {
	for _, group := range groups {
		ok := len(group) > 0
		for _, term := range group {
			ok = ok && slices.Contains(facts, term)
		}
		if ok {
			return true
		}
	}
	return false
}

func seedChildRules(child *model.TaskDefinition, login string, snapshot arcanumreview.PRSnapshot, baseline PRCursor) json.RawMessage {
	if child == nil || child.Polling == nil || child.Polling.Type != model.PollingRules {
		return jsonBytes(baseline)
	}
	var cfg model.PollingRulesConfig
	_ = json.Unmarshal(child.Polling.Config, &cfg)
	cursor := model.PollingRulesCursor{Rules: map[string]model.PollingRuleState{}}
	for _, rule := range cfg.Rules {
		if rule.Type != TypePRWait {
			continue
		}
		leafCfg, err := DecodeConfig(rule.Polling(*child.Polling))
		if err != nil {
			continue
		}
		if leafCfg.PRID != "{{pr_id}}" && leafCfg.PRID != strconv.FormatInt(snapshot.ID, 10) {
			continue
		}
		leafCfg.Login = login
		accepted, _ := ComparePR(leafCfg, baseline, snapshot, 1)
		seeded := baseline
		seeded.Matches = accepted.Matches
		cursor.Rules[rule.ID] = model.PollingRuleState{Cursor: jsonBytes(seeded)}
	}
	return jsonBytes(cursor)
}
