package arcanumreview

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

type CheckKey struct {
	System string `json:"system"`
	Type   string `json:"type"`
}
type PRCheck struct {
	Key         CheckKey `json:"key"`
	Required    bool     `json:"required"`
	Status      string   `json:"status"`
	Description string   `json:"description,omitempty"`
	URI         string   `json:"uri,omitempty"`
}

func FailedMergeChecks(checks []PRCheck) []PRCheck {
	failed := []PRCheck{}
	for _, check := range checks {
		if check.Required && check.Status == "failure" && !(check.Key.System == "arcanum" && (check.Key.Type == "approved" || check.Key.Type == "comment_issues_closed")) {
			failed = append(failed, check)
		}
	}
	slices.SortFunc(failed, func(a, b PRCheck) int {
		if a.Key.System != b.Key.System {
			return strings.Compare(a.Key.System, b.Key.System)
		}
		return strings.Compare(a.Key.Type, b.Key.Type)
	})
	return failed
}
func (c *CLIClient) ReadPRChecks(ctx context.Context, diffID int64) ([]PRCheck, error) {
	if diffID <= 0 {
		return nil, fmt.Errorf("positive published diff id required")
	}
	checks := []PRCheck{}
	cursor := ""
	seen := map[string]bool{}
	for {
		var page struct {
			Checks []PRCheck `json:"checks"`
			Next   string    `json:"next_page_cursor"`
		}
		args := []string{"diff", "checks", strconv.FormatInt(diffID, 10), "--limit", "50"}
		if cursor != "" {
			args = append(args, "--cursor", cursor)
		}
		if err := c.readJSON(ctx, &page, args...); err != nil {
			return nil, err
		}
		if page.Checks == nil {
			return nil, fmt.Errorf("PR merge checks unavailable")
		}
		for _, check := range page.Checks {
			if check.Key.System == "" || check.Key.Type == "" || check.Status == "" {
				return nil, fmt.Errorf("incomplete PR merge check")
			}
		}
		checks = append(checks, page.Checks...)
		if page.Next == "" {
			return checks, nil
		}
		if seen[page.Next] {
			return nil, fmt.Errorf("PR merge checks cursor repeated")
		}
		seen[page.Next] = true
		cursor = page.Next
	}
}
