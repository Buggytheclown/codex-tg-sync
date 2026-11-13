package arcanumreview

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const SearchFields = "review_requests(id,url,summary,author(uid,name),status,full_status,common_status,active_diff_set(id,arc_branch_heads(from_id)),diff_sets(id,status,published_at)),total_count"

type SearchItem struct {
	ID           int64      `json:"id"`
	Summary      string     `json:"summary"`
	Author       PRIdentity `json:"author"`
	Status       string     `json:"status"`
	Lifecycle    string     `json:"full_status"`
	CommonStatus string     `json:"common_status"`
	Active       struct {
		ID    int64 `json:"id"`
		Heads struct {
			FromID string `json:"from_id"`
		} `json:"arc_branch_heads"`
	} `json:"active_diff_set"`
	Diffs []struct {
		ID          int64  `json:"id"`
		Status      string `json:"status"`
		PublishedAt string `json:"published_at"`
	} `json:"diff_sets"`
	Snapshot PRSnapshot `json:"-"`
}
type SearchResult struct {
	Items    []SearchItem
	Complete bool
}

func (p SearchItem) snapshot() PRSnapshot {
	s := PRSnapshot{RevisionKnown: p.Diffs != nil, ID: p.ID, Author: p.Author, Status: p.Lifecycle, CommonStatus: p.CommonStatus}
	if p.CommonStatus == "merged" && (s.Status == "" || s.Status == "merged") {
		s.Status = "merged"
	}
	if s.Status == "merged" {
		if p.CommonStatus != "" && p.CommonStatus != "merged" {
			s.Status = ""
		}
		return s
	}
	if s.Status != "open" && s.Status != "discarded" {
		s.Status = ""
	}
	if p.CommonStatus == "merged" {
		s.Status = ""
	}
	var latest time.Time
	for _, d := range p.Diffs {
		if d.Status != "published" {
			continue
		}
		if d.ID == p.Active.ID {
			s.PublishedDiffID = d.ID
			s.HeadCommit = p.Active.Heads.FromID
			return s
		}
		at, err := time.Parse(time.RFC3339Nano, d.PublishedAt)
		if err == nil && at.After(latest) {
			s.PublishedDiffID = d.ID
			latest = at
		}
	}
	return s
}

// Search retains positive rows on a later page failure. Complete is required
// for discovery; absence is never a lifecycle observation. wanted permits
// author lookups to stop as soon as all currently needed IDs have been found.
func (c *CLIClient) Search(ctx context.Context, query string, limit int, wanted map[int64]bool) (SearchResult, error) {
	result := SearchResult{}
	if limit <= 0 {
		limit = 100
	}
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	seen := map[int64]bool{}
	found := map[int64]bool{}
	total := -1
	for offset := 0; ; offset += limit {
		var page struct {
			Items []SearchItem `json:"review_requests"`
			Total *int         `json:"total_count"`
			Next  *bool        `json:"has_next"`
		}
		if err := c.readJSON(ctx, &page, "pr", "search", "--query", query, "--limit", strconv.Itoa(limit), "--offset", strconv.Itoa(offset), "--order=-updated_at", "--fields", SearchFields); err != nil {
			return result, err
		}
		if page.Items == nil {
			return result, errors.New("PR search page unavailable")
		}
		if page.Total != nil {
			if *page.Total < 0 || (total >= 0 && total != *page.Total) {
				return result, errors.New("PR search count changed")
			}
			total = *page.Total
		}
		for _, p := range page.Items {
			if p.ID <= 0 || seen[p.ID] {
				return result, errors.New("PR search repeated or invalid row")
			}
			seen[p.ID] = true
			p.Snapshot = p.snapshot()
			result.Items = append(result.Items, p)
			found[p.ID] = true
		}
		if total >= 0 && offset+len(page.Items) > total {
			return result, errors.New("PR search count exceeded")
		}
		if len(wanted) > 0 {
			all := true
			for id := range wanted {
				all = all && found[id]
			}
			if all {
				return result, nil
			}
		}
		if total >= 0 {
			if offset+len(page.Items) == total {
				result.Complete = true
				return result, nil
			}
			if len(page.Items) == 0 {
				return result, errors.New("PR search empty intermediate page")
			}
		} else if page.Next != nil {
			if !*page.Next && len(page.Items) < limit {
				result.Complete = true
				return result, nil
			}
			if len(page.Items) == 0 {
				return result, errors.New("PR search empty intermediate page")
			}
		} else if len(page.Items) < limit {
			result.Complete = true
			return result, nil
		}
		if len(page.Items) > limit {
			return result, fmt.Errorf("PR search page exceeds limit")
		}
	}
}

func (c *CLIClient) ReadPRInfo(ctx context.Context, id int64) (PRSnapshot, error) {
	var s PRSnapshot
	err := c.readJSON(ctx, &s, "pr", "get", strconv.FormatInt(id, 10), "--fields", "id,status,author(uid,name)")
	if err == nil && (s.ID != id || (s.Status != "open" && s.Status != "merged" && s.Status != "discarded")) {
		err = errors.New("incomplete PR lifecycle")
	}
	return s, err
}
func (c *CLIClient) ReadPRComments(ctx context.Context, id int64) ([]PRComment, error) {
	var comments []PRComment
	err := c.readJSON(ctx, &comments, "pr", "comments", strconv.FormatInt(id, 10), "--with-dropped", "--fields", "id,issue_status,is_draft,author(uid,name),reply_to_id,deleted_at,created_at,status_updated_at,status_updated_by(uid,name)")
	if err == nil && comments == nil {
		err = errors.New("PR comments unavailable")
	}
	return comments, err
}
