package arcanumreview

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"
)

// ReadEvent describes a physical CLI call, not an HTTP-attempt count. Key is
// opaque: diagnostics never need query text, PR IDs, comments or credentials.
type ReadEvent struct {
	Phase     string
	Method    string
	Key       string
	StartedAt time.Time
	Duration  time.Duration
	Success   bool
}

// SetReadObserver is configured at composition, before concurrent reads start.
func (c *CLIClient) SetReadObserver(observer func(ReadEvent)) { c.observe = observer }
func (c *CLIClient) runObserved(ctx context.Context, args []string) ([]byte, error) {
	method, identity := "unknown", ""
	if len(args) >= 2 {
		method = args[0] + "." + args[1]
	}
	if method == "pr.search" {
		for i, arg := range args {
			if arg == "--query" && i+1 < len(args) {
				identity = args[i+1]
				break
			}
		}
	} else if len(args) > 2 {
		identity = args[2]
	}
	digest := sha256.Sum256([]byte(method + "\x00" + identity))
	event := ReadEvent{Phase: "start", Method: method, Key: fmt.Sprintf("%x", digest[:8]), StartedAt: time.Now().UTC()}
	if c.observe != nil {
		c.observe(event)
	}
	data, err := c.runner.Run(ctx, c.yaBin, append([]string{"tool", "gena-arcanum-cli", "--json"}, args...)...)
	event.Phase = "finish"
	event.Duration = time.Since(event.StartedAt)
	event.Success = err == nil
	if c.observe != nil {
		c.observe(event)
	}
	return data, err
}
