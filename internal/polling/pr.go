package polling

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/mideco-tech/codex-tg/internal/arcanumreview"
	"slices"
	"time"
)

// Cursor carries the last accepted instruction's head/status and consumed
// replies. Arming refreshes tracked open issues, but never overwrites the head
// before comparing: changes while the user reviewed results must stay visible.
type PRCursor struct {
	Matches         map[string]string        `json:"matches,omitempty"`
	HeadCommit      string                   `json:"head_commit"`
	CommonStatus    string                   `json:"common_status"`
	Generation      int64                    `json:"generation"`
	Tracked         []int64                  `json:"tracked"`
	ConsumedReplies []int64                  `json:"consumed_replies"`
	Since           string                   `json:"since,omitempty"`
	ChecksDiffID    int64                    `json:"checks_diff_id,omitempty"`
	FailedChecks    []arcanumreview.CheckKey `json:"failed_checks,omitempty"`
}

func ComparePR(cfg Config, old PRCursor, current arcanumreview.PRSnapshot, generation int64) (PRCursor, []string) {
	grouped := len(cfg.ConditionGroups) > 0
	cfg.Conditions = conditions(cfg)
	next := old
	comments := map[int64]arcanumreview.PRComment{}
	for _, c := range current.Comments {
		comments[c.ID] = c
	}
	// At each explicit arm capture current issues, including ones created during
	// the preceding LLM pass. Keep an already tracked issue until the wait ends.
	if old.Generation != generation {
		next.Tracked = nil
		for _, c := range current.Comments {
			if !c.Published() || (c.IssueStatus != "open" && !(c.IssueStatus == "resolved" && createdSince(c.CreatedAt, old.Since))) {
				continue
			}
			if slices.Contains(cfg.Conditions, ConditionAnswered) && c.Author.Name != cfg.Login {
				continue
			}
			next.Tracked = append(next.Tracked, c.ID)
		}
		// A response or resolution may arrive before rearming; previously known
		// tracked IDs still belong to this cycle unless already consumed.
		next.Tracked = appendUnique(next.Tracked, old.Tracked...)
	}
	next.Generation = generation
	reasons := []string{}
	checksChanged := false
	if slices.Contains(cfg.Conditions, ConditionMergeChecks) {
		var failed bool
		next, failed = compareMergeChecks(next, current)
		checksChanged = failed
		if (grouped && len(arcanumreview.FailedMergeChecks(current.Checks)) > 0) || (!grouped && failed) {
			reasons = append(reasons, ConditionMergeChecks)
		}
	}

	if slices.Contains(cfg.Conditions, ConditionCommit) && old.HeadCommit != "" && current.HeadCommit != "" && current.HeadCommit != old.HeadCommit {
		reasons = append(reasons, ConditionCommit)
	}
	if slices.Contains(cfg.Conditions, ConditionChanges) && (grouped || (old.CommonStatus != "" && old.CommonStatus != "waiting_for_changes")) && current.CommonStatus == "waiting_for_changes" {
		reasons = append(reasons, ConditionChanges)
	}
	answered, resolved := len(next.Tracked) > 0, len(next.Tracked) > 0
	qualifying := []int64{}
	for _, id := range next.Tracked {
		issue, exists := comments[id]
		if !exists || !issue.Published() {
			answered = false
			resolved = false
			continue
		}
		if issue.IssueStatus != "resolved" {
			resolved = false
		}
		hasReply := false
		for _, reply := range current.Comments {
			if !reply.Published() || sameAuthor(reply.Author, issue.Author) || slices.Contains(old.ConsumedReplies, reply.ID) {
				continue
			}
			if descendsFrom(reply, id, comments) {
				hasReply = true
				qualifying = appendUnique(qualifying, reply.ID)
			}
		}
		if !hasReply {
			answered = false
		}
	}
	if slices.Contains(cfg.Conditions, ConditionAnswered) && answered {
		reasons = append(reasons, ConditionAnswered)
	}
	if slices.Contains(cfg.Conditions, ConditionResolved) && resolved {
		reasons = append(reasons, ConditionResolved)
	}
	if grouped {
		if current.Status == "merged" && slices.Contains(cfg.Conditions, ConditionMerged) {
			reasons = append(reasons, ConditionMerged)
		}
		next.Matches = map[string]string{}
		matched := []string{}
		changed := false
		for _, group := range cfg.ConditionGroups {
			if !groupsMatch([][]string{group}, reasons) {
				continue
			}
			groupID, _ := json.Marshal(group)
			evidence := []any{current.HeadCommit, current.PublishedDiffID}
			fresh := current.HeadCommit != "" && current.HeadCommit != old.HeadCommit
			if slices.Contains(group, ConditionChanges) {
				fresh = fresh || current.CommonStatus != old.CommonStatus
			}
			if slices.Contains(group, ConditionAnswered) {
				evidence = append(evidence, qualifying)
				fresh = fresh || len(qualifying) > 0
			}
			if slices.Contains(group, ConditionMergeChecks) {
				keys := []arcanumreview.CheckKey{}
				for _, check := range arcanumreview.FailedMergeChecks(current.Checks) {
					keys = append(keys, check.Key)
				}
				evidence = append(evidence, keys)
				fresh = fresh || checksChanged
			}
			data, _ := json.Marshal(evidence)
			key := fmt.Sprintf("%x", sha256.Sum256(data))
			previous, exists := old.Matches[string(groupID)]
			// Remember each group's observation independently: one OR branch recovering
			// must not replay another branch that remains true.
			next.Matches[string(groupID)] = key
			changed = changed || !exists || (previous != key && fresh)
			for _, term := range group {
				if !slices.Contains(matched, term) {
					matched = append(matched, term)
				}
			}
		}
		if changed {
			reasons = matched
		} else {
			reasons = nil
		}
	}
	if len(reasons) > 0 {
		if current.HeadCommit != "" {
			next.HeadCommit = current.HeadCommit
		}
		next.CommonStatus = current.CommonStatus
		next.ConsumedReplies = appendUnique(next.ConsumedReplies, qualifying...)
		next.Tracked = nil // next explicit arm starts a fresh set, no vacuous repeats
	} else {
		// Seed unknown metadata; thereafter keep the accepted head until an event.
		if next.HeadCommit == "" {
			next.HeadCommit = current.HeadCommit
		}
		// Status is a transition observation, so remember intervening non-trigger states.
		next.CommonStatus = current.CommonStatus
	}
	return next, reasons
}
func createdSince(created, since string) bool {
	c, err := time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return false
	}
	s, err := time.Parse(time.RFC3339Nano, since)
	return err == nil && !c.Before(s)
}
func appendUnique(dst []int64, values ...int64) []int64 {
	for _, v := range values {
		if !slices.Contains(dst, v) {
			dst = append(dst, v)
		}
	}
	return dst
}
func sameAuthor(a, b arcanumreview.PRIdentity) bool {
	if a.UID != "" && b.UID != "" {
		return a.UID == b.UID
	}
	return a.Name == "" || b.Name == "" || a.Name == b.Name
}
func descendsFrom(reply arcanumreview.PRComment, id int64, comments map[int64]arcanumreview.PRComment) bool {
	seen := map[int64]bool{reply.ID: true}
	parent := reply.ReplyToID
	for parent != 0 && !seen[parent] {
		if parent == id {
			return true
		}
		seen[parent] = true
		c, ok := comments[parent]
		if !ok {
			return false
		}
		parent = c.ReplyToID
	}
	return false
}

func compareMergeChecks(old PRCursor, current arcanumreview.PRSnapshot) (PRCursor, bool) {
	if current.PublishedDiffID == 0 || current.Checks == nil {
		return old, false
	}
	next := old
	next.ChecksDiffID = current.PublishedDiffID
	next.FailedChecks = nil
	changed := false
	for _, check := range arcanumreview.FailedMergeChecks(current.Checks) {
		if old.ChecksDiffID != current.PublishedDiffID || !slices.Contains(old.FailedChecks, check.Key) {
			changed = true
		}
		next.FailedChecks = append(next.FailedChecks, check.Key)
	}
	// shortcut: retries entirely between polls are invisible; add run identity if the API provides it.
	return next, changed
}
