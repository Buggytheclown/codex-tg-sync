package polling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	ReadPRSnapshot(context.Context, int64) (arcanumreview.PRSnapshot, error)
}
type Checker struct{ pr PRClient }

func NewChecker(pr PRClient) *Checker { return &Checker{pr: pr} }
func timestamp(t time.Time) model.TimeString {
	return model.TimeString(t.UTC().Format(time.RFC3339Nano))
}
func jsonBytes(value any) json.RawMessage { b, _ := json.Marshal(value); return b }

func (c *Checker) Check(ctx context.Context, p model.TaskPolling, now time.Time) (model.TaskPollingOutcome, error) {
	out := model.TaskPollingOutcome{TaskID: p.TaskID, Generation: p.Generation, Cursor: p.Cursor, CheckedAt: timestamp(now), NextCheckAt: timestamp(now.Add(time.Minute))}
	if err := Validate(p); err != nil {
		return out, err
	}
	cfg, _ := DecodeConfig(p)
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
			prs, err = c.pr.ListAuthoredWaitingForChanges(ctx, cfg.Login)
		}
		if err != nil {
			return out, err
		}
		var cursor struct {
			Seen []int64 `json:"seen"`
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
		for _, pr := range prs {
			if seen[pr.ID] || (p.Type == TypeReview && pr.Author == cfg.Login) {
				continue
			}
			if attempts >= 5 {
				break
			}
			attempts++
			snapshot, err := c.pr.ReadPRSnapshot(ctx, pr.ID)
			if err != nil {
				readErrors = append(readErrors, err)
				continue
			}
			author := strings.TrimSpace(snapshot.Author.Name)
			if author == "" {
				author = pr.Author
			}
			id := strconv.FormatInt(pr.ID, 10)
			key := prefix + id
			url := "https://a.yandex-team.ru/review/" + id
			baseline := PRCursor{HeadCommit: snapshot.HeadCommit, CommonStatus: snapshot.CommonStatus, Since: now.UTC().Format(time.RFC3339Nano)}
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
			out.Events = append(out.Events, model.PollingEvent{Key: key, SourceKey: key, Title: pr.Summary, SourceURL: url, Cursor: jsonBytes(baseline), Payload: jsonBytes(map[string]string{"pr_id": id, "author": author, "head_commit": snapshot.HeadCommit, "common_status": snapshot.CommonStatus}), Variables: map[string]string{"pr_id": id, "pr_title": pr.Summary, "pr_url": url, "author": author, "login": cfg.Login}})
			cursor.Seen = append(cursor.Seen, pr.ID)
		}
		out.Cursor = jsonBytes(cursor)
		if len(out.Events) == 0 && len(readErrors) > 0 {
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
		hash := sha256.Sum256(append(append([]byte{}, payload...), out.Cursor...))
		key := fmt.Sprintf("wait:%d:%s", p.Generation, hex.EncodeToString(hash[:12]))
		out.Events = []model.PollingEvent{{Key: key, SourceURL: "https://a.yandex-team.ru/review/" + cfg.PRID, Payload: payload, Variables: map[string]string{"pr_id": cfg.PRID, "pr_url": "https://a.yandex-team.ru/review/" + cfg.PRID, "reasons": strings.Join(reasons, ", "), "author": current.Author.Name}}}
	}
	return out, nil
}
