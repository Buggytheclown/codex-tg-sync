package arcanumreview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const cliTimeout = 30 * time.Second

var loginPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type PullRequest struct {
	ID      int64
	Author  string
	Summary string
}

type AssignedClient interface {
	ListAssigned(ctx context.Context, login string) ([]PullRequest, error)
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = commandEnvironment(os.Environ(), name)
	return command.Output()
}

func commandEnvironment(environ []string, executable string) []string {
	result := append([]string(nil), environ...)
	if !filepath.IsAbs(executable) {
		return result
	}
	directory := filepath.Dir(executable)
	for index, entry := range result {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, "PATH") {
			result[index] = key + "=" + directory + string(os.PathListSeparator) + value
			return result
		}
	}
	return append(result, "PATH="+directory)
}

type CLIClient struct {
	yaBin  string
	runner CommandRunner
}

func NewCLIClient(yaBin string, runner CommandRunner) *CLIClient {
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &CLIClient{yaBin: strings.TrimSpace(yaBin), runner: runner}
}

func (c *CLIClient) ListAssigned(ctx context.Context, login string) ([]PullRequest, error) {
	login = strings.TrimSpace(login)
	if c == nil || c.runner == nil || c.yaBin == "" || !loginPattern.MatchString(login) {
		return nil, errors.New("Arcanum review client requires ya binary and a valid login")
	}
	query := "open(true);published(true);assignee(" + login + ")"
	args := []string{
		"tool", "gena-arcanum-cli", "--json", "pr", "search",
		"--query", query,
		"--limit", "100", "--all", "--order=-updated_at",
		"--fields", "review_requests(id,url,author(name),summary),total_count",
	}
	callCtx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	data, err := c.runner.Run(callCtx, c.yaBin, args...)
	if err != nil {
		if callCtx.Err() != nil {
			return nil, fmt.Errorf("Arcanum review search failed: %w", callCtx.Err())
		}
		return nil, fmt.Errorf("Arcanum review search failed: %w", err)
	}
	var payload struct {
		ReviewRequests []struct {
			ID      int64  `json:"id"`
			Summary string `json:"summary"`
			Author  struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"review_requests"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode Arcanum review search: %w", err)
	}
	requests := make([]PullRequest, 0, len(payload.ReviewRequests))
	for _, raw := range payload.ReviewRequests {
		author := strings.TrimSpace(raw.Author.Name)
		summary := strings.TrimSpace(raw.Summary)
		if raw.ID <= 0 || author == "" || summary == "" {
			return nil, fmt.Errorf("Arcanum review search returned incomplete PR metadata for id %d", raw.ID)
		}
		requests = append(requests, PullRequest{ID: raw.ID, Author: author, Summary: summary})
	}
	return requests, nil
}
