package arcanumreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// PRSnapshot contains observation facts, not a claim that a model reviewed them.
type PRSnapshot struct {
	ID              int64       `json:"id"`
	Status          string      `json:"status"`
	Author          PRIdentity  `json:"author"`
	CommonStatus    string      `json:"common_status"`
	PublishedDiffID int64       `json:"published_diff_id"`
	HeadCommit      string      `json:"head_commit"`
	Comments        []PRComment `json:"comments"`
}
type PRIdentity struct {
	UID  string `json:"uid"`
	Name string `json:"name"`
}
type PRComment struct {
	ID              int64      `json:"id"`
	Author          PRIdentity `json:"author"`
	ReplyToID       int64      `json:"reply_to_id"`
	IsDraft         *bool      `json:"is_draft"`
	IssueStatus     string     `json:"issue_status"`
	DeletedAt       string     `json:"deleted_at"`
	CreatedAt       string     `json:"created_at"`
	StatusUpdatedAt string     `json:"status_updated_at"`
}

func (c PRComment) Published() bool {
	return c.ID > 0 && c.IsDraft != nil && !*c.IsDraft && c.DeletedAt == ""
}

func (c *CLIClient) readJSON(ctx context.Context, result any, args ...string) error {
	if c == nil || c.runner == nil || c.yaBin == "" {
		return errors.New("Arcanum client requires ya binary")
	}
	callCtx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	data, err := c.runner.Run(callCtx, c.yaBin, append([]string{"tool", "gena-arcanum-cli", "--json"}, args...)...)
	if err != nil {
		return fmt.Errorf("Arcanum read failed: %w", err)
	}
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("Arcanum returned unknown data")
	}
	if err = json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode Arcanum read: %w", err)
	}
	return nil
}

// ReadPRSnapshot uses both API versions exposed by gena. v2 has comments and
// lifecycle; only v1 search exposes common_status and the published source head.
func (c *CLIClient) ReadPRSnapshot(ctx context.Context, id int64) (PRSnapshot, error) {
	var snapshot PRSnapshot
	if id <= 0 {
		return snapshot, errors.New("positive PR id required")
	}
	key := strconv.FormatInt(id, 10)
	if err := c.readJSON(ctx, &snapshot, "pr", "get", key, "--fields", "id,status,author(uid,name)"); err != nil {
		return snapshot, err
	}
	if snapshot.ID != id || snapshot.Status == "" || !loginPattern.MatchString(snapshot.Author.Name) {
		return snapshot, errors.New("incomplete PR metadata")
	}
	var search struct {
		ReviewRequests []struct {
			ID           int64  `json:"id"`
			CommonStatus string `json:"common_status"`
			Active       struct {
				ID    int64 `json:"id"`
				Heads struct {
					FromID string `json:"from_id"`
				} `json:"arc_branch_heads"`
			} `json:"active_diff_set"`
			Diffs []struct {
				ID     int64  `json:"id"`
				Status string `json:"status"`
			} `json:"diff_sets"`
		} `json:"review_requests"`
	}
	err := c.readJSON(ctx, &search, "pr", "search", "--query", "author("+snapshot.Author.Name+")", "--limit", "100", "--all", "--order=-updated_at", "--fields", "review_requests(id,common_status,active_diff_set(id,arc_branch_heads(from_id)),diff_sets(id,status)),total_count")
	if err != nil {
		return snapshot, err
	}
	found := false
	for _, pr := range search.ReviewRequests {
		if pr.ID != id {
			continue
		}
		found = true
		snapshot.CommonStatus = pr.CommonStatus
		for _, diff := range pr.Diffs {
			if diff.ID == pr.Active.ID && diff.Status == "published" {
				snapshot.PublishedDiffID = pr.Active.ID
				snapshot.HeadCommit = pr.Active.Heads.FromID
				break
			}
		}
		break
	}
	if !found || snapshot.CommonStatus == "" {
		return snapshot, errors.New("PR search did not return current status")
	}
	// An unpublished active diff is deliberately not exposed as a new commit.
	if err := c.readJSON(ctx, &snapshot.Comments, "pr", "comments", key, "--with-dropped", "--fields", "id,issue_status,is_draft,author(uid,name),reply_to_id,deleted_at,created_at,status_updated_at,status_updated_by(uid,name)"); err != nil {
		return snapshot, err
	}
	if snapshot.Comments == nil {
		return snapshot, errors.New("PR comments unavailable")
	}
	return snapshot, nil
}
