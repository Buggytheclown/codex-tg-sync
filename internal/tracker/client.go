package tracker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var keyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`)

type Issue struct {
	Key     string `json:"key"`
	Summary string `json:"summary"`
	URL     string `json:"url"`
}

func Key(input string) (string, error) {
	value := strings.TrimSpace(input)
	if u, err := url.Parse(value); err == nil && u.Scheme != "" {
		if u.Scheme != "https" || u.Host != "st.yandex-team.ru" || u.User != nil {
			return "", errors.New("use a Tracker issue key or st.yandex-team.ru issue URL")
		}
		value = strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), "/")
	}
	value = strings.ToUpper(value)
	if !keyPattern.MatchString(value) {
		return "", errors.New("invalid Tracker issue key")
	}
	return value, nil
}
func Lookup(ctx context.Context, executable, input string) (Issue, error) {
	key, err := Key(input)
	if err != nil {
		return Issue{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if executable == "" {
		executable = "ya"
	}
	cmd := exec.CommandContext(ctx, executable, "tool", "mcp", "connect", "tracker", "--tool", "get_issue", "--arg", "issue:"+key, "--arg", `fields:["key","summary"]`, "--arg", "commentsLimit:0", "--raw")
	cmd.Env = os.Environ()
	if filepath.IsAbs(executable) {
		cmd.Env = append(cmd.Env, "PATH="+filepath.Dir(executable)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Issue{}, errors.New("Tracker unavailable")
	}
	if err = cmd.Start(); err != nil {
		return Issue{}, errors.New("Tracker unavailable; enter a title or retry")
	}
	raw, readErr := io.ReadAll(io.LimitReader(stdout, 1<<20+1))
	if readErr != nil || len(raw) > 1<<20 {
		_ = cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	if readErr != nil || waitErr != nil || len(raw) > 1<<20 {
		return Issue{}, errors.New("Tracker lookup failed; enter a title or retry")
	}
	return decode(raw, key)
}
func decode(raw []byte, key string) (Issue, error) {
	var envelope struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError    bool            `json:"isError"`
			Structured json.RawMessage `json:"structuredContent"`
			Content    []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &envelope) != nil || (len(envelope.Error) > 0 && !bytes.Equal(envelope.Error, []byte("null"))) || envelope.Result.IsError {
		return Issue{}, errors.New("Tracker lookup failed")
	}
	candidates := []json.RawMessage{envelope.Result.Structured}
	for _, c := range envelope.Result.Content {
		if c.Type == "text" {
			candidates = append(candidates, json.RawMessage(c.Text))
		}
	}
	for _, data := range candidates {
		var i Issue
		if json.Unmarshal(data, &i) == nil && i.Key == key && strings.TrimSpace(i.Summary) != "" {
			i.URL = "https://st.yandex-team.ru/" + key
			return i, nil
		}
	}
	return Issue{}, errors.New("Tracker returned no matching title")
}
