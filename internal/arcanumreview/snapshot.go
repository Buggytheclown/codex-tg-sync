package arcanumreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// PRSnapshot contains observation facts, not a claim that a model reviewed them.
type PRSnapshot struct {
	RevisionKnown   bool        `json:"revision_known,omitempty"`
	Coverage        *Coverage   `json:"-"`
	ID              int64       `json:"id"`
	Status          string      `json:"status"`
	Author          PRIdentity  `json:"author"`
	CommonStatus    string      `json:"common_status"`
	PublishedDiffID int64       `json:"published_diff_id"`
	HeadCommit      string      `json:"head_commit"`
	Comments        []PRComment `json:"comments"`
	Checks          []PRCheck   `json:"checks,omitempty"`
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
	data, err := c.runObserved(callCtx, args)
	if err != nil {
		return fmt.Errorf("Arcanum read failed: %w", commandBackoff(err))
	}
	if strings.TrimSpace(string(data)) == "null" {
		return errors.New("Arcanum returned unknown data")
	}
	if err = json.Unmarshal(data, result); err != nil {
		return fmt.Errorf("decode Arcanum read: %w", err)
	}
	return nil
}
