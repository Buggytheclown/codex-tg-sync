package arcanumreview

import (
	"context"
	"errors"
	"strconv"
	"time"
)

// ReadPRMetadata reads display facts without comments, checks or rule cursors.
func (c *CLIClient) ReadPRMetadata(ctx context.Context, id int64) (PRSnapshot, error) {
	var snapshot PRSnapshot
	if id <= 0 {
		return snapshot, errors.New("positive PR ID required")
	}
	if err := c.readJSON(ctx, &snapshot, "pr", "get", strconv.FormatInt(id, 10), "--fields", "id,status,author(uid,name)"); err != nil {
		return snapshot, err
	}
	if snapshot.ID != id || snapshot.Status == "" {
		return snapshot, errors.New("incomplete PR lifecycle")
	}
	if snapshot.Status == "merged" {
		return snapshot, nil
	}
	if !loginPattern.MatchString(snapshot.Author.Name) {
		return snapshot, errors.New("incomplete PR author")
	}
	var search struct {
		Items []struct {
			ID           int64  `json:"id"`
			CommonStatus string `json:"common_status"`
		} `json:"review_requests"`
	}
	if err := c.readJSON(ctx, &search, "pr", "search", "--query", "author("+snapshot.Author.Name+")", "--limit", "100", "--all", "--order=-updated_at", "--fields", "review_requests(id,common_status),total_count"); err != nil {
		return snapshot, err
	}
	for _, item := range search.Items {
		if item.ID == id && item.CommonStatus != "" {
			snapshot.CommonStatus = item.CommonStatus
			return snapshot, nil
		}
	}
	return snapshot, errors.New("PR status unavailable")
}

type PRMetadataStore interface {
	ListDuePRMetadata(context.Context, time.Time, time.Time, int) ([]string, error)
	RecordPRMetadata(context.Context, string, string, string, bool, time.Time) error
}
type PRMetadataReader interface {
	ReadPRMetadata(context.Context, int64) (PRSnapshot, error)
}

// RunPRMetadata has no action-polling or launch interface. A display read cannot
// cross the effectful polling boundary even when a card is outside Polling.
func RunPRMetadata(ctx context.Context, store PRMetadataStore, reader PRMetadataReader, presence *MetadataPresence, onError func(error)) {
	runPRMetadata(ctx, store, reader, presence, onError, func(ctx context.Context) bool {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		}
	})
}

func runPRMetadata(ctx context.Context, store PRMetadataStore, reader PRMetadataReader, presence *MetadataPresence, onError func(error), pause func(context.Context) bool) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	var observedActivation, refreshSince time.Time
	wait := func() bool {
		select {
		case <-ctx.Done():
			return false
		case <-presence.changed:
			return true
		case <-ticker.C:
			return true
		}
	}
	for ctx.Err() == nil {
		active, activation := presence.State(time.Now().UTC())
		if !active {
			if !wait() {
				return
			}
			continue
		}
		if !activation.Equal(observedActivation) {
			observedActivation = activation
			// Resume an unfinished pass without accumulating another full-board pass.
			if refreshSince.IsZero() {
				refreshSince = activation
			}
		}
		ids, err := store.ListDuePRMetadata(ctx, time.Now().UTC(), refreshSince, 10)
		if err == nil && len(ids) == 0 {
			refreshSince = time.Time{}
		}
		if err == nil {
			for _, id := range ids {
				if ctx.Err() != nil {
					return
				}
				if active, _ := presence.State(time.Now().UTC()); !active {
					break
				}
				number, parseErr := strconv.ParseInt(id, 10, 64)
				callCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
				var snapshot PRSnapshot
				readErr := parseErr
				if readErr == nil {
					snapshot, readErr = reader.ReadPRMetadata(callCtx, number)
				}
				cancel()
				if ctx.Err() != nil {
					return
				}
				err = store.RecordPRMetadata(ctx, id, snapshot.Status, snapshot.CommonStatus, readErr == nil, time.Now().UTC())
				if err != nil {
					break
				}
				if !pause(ctx) {
					return
				}
			}
		}
		if err != nil && onError != nil {
			onError(err)
		}
		if err == nil && len(ids) > 0 {
			continue
		}
		if !wait() {
			return
		}
	}
}
