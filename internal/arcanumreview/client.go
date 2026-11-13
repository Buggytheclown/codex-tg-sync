package arcanumreview

import (
	"context"
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

type WaitingChangesClient interface {
	ListAuthoredWaitingForChanges(ctx context.Context, login string) ([]PullRequest, error)
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(commandEnvironment(os.Environ(), name), "ARCANUM_CLI_RETRY_MAX_ATTEMPTS=1")
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
	observe func(ReadEvent)
	yaBin   string
	runner  CommandRunner
}

func NewCLIClient(yaBin string, runner CommandRunner) *CLIClient {
	if runner == nil {
		runner = execCommandRunner{}
	}
	return &CLIClient{yaBin: strings.TrimSpace(yaBin), runner: runner}
}

func (c *CLIClient) ListAssigned(ctx context.Context, login string) ([]PullRequest, error) {
	return c.search(ctx, login, "open(true);published(true);assignee("+strings.TrimSpace(login)+")")
}

func (c *CLIClient) ListAuthoredWaitingForChanges(ctx context.Context, login string) ([]PullRequest, error) {
	return c.search(ctx, login, "open(true);published(true);author("+strings.TrimSpace(login)+");common_status(waiting_for_changes)")
}

func (c *CLIClient) ListAuthoredOpen(ctx context.Context, login string) ([]PullRequest, error) {
	return c.search(ctx, login, "open(true);published(true);author("+strings.TrimSpace(login)+")")
}

func (c *CLIClient) search(ctx context.Context, login, query string) ([]PullRequest, error) {
	login = strings.TrimSpace(login)
	if c == nil || c.runner == nil || c.yaBin == "" || !loginPattern.MatchString(login) {
		return nil, errors.New("Arcanum review client requires ya binary and a valid login")
	}
	result, err := c.Search(ctx, query, 100, nil)
	if err != nil {
		return nil, err
	}
	requests := make([]PullRequest, 0, len(result.Items))
	for _, p := range result.Items {
		if p.ID <= 0 || strings.TrimSpace(p.Author.Name) == "" || strings.TrimSpace(p.Summary) == "" {
			return nil, fmt.Errorf("Arcanum review search returned incomplete PR metadata for id %d", p.ID)
		}
		requests = append(requests, PullRequest{ID: p.ID, Author: p.Author.Name, Summary: p.Summary})
	}
	return requests, nil
}
