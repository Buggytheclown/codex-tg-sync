package appserver

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

func TestRequestReleasesOwnerLockOnlyAfterFrameWrite(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	c := NewClient("", "", "", time.Second)
	c.started = true
	c.stdin = w
	var owner sync.Mutex
	ctx := WithRequestWaitHooks(context.Background(), RequestWaitHooks{BeforeWait: owner.Unlock, AfterWait: owner.Lock})
	done := make(chan error, 1)
	go func() { owner.Lock(); _, err := c.Request(ctx, "turn/start", nil); owner.Unlock(); done <- err }()
	frame := make([]byte, 1024)
	if _, err := r.Read(frame); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan struct{})
	go func() { owner.Lock(); close(acquired); owner.Unlock() }()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("owner lock held while waiting for remote reply")
	}
	c.mu.Lock()
	reply := c.pending[1]
	c.mu.Unlock()
	reply <- rpcResponse{Result: map[string]any{}}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
