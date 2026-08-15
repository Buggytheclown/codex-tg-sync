package appserver

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeWriterProcess struct {
	mu         sync.Mutex
	startCalls int
	closeCalls int
	startGate  <-chan struct{}
	startErr   error
	closeErr   error
}

func (p *fakeWriterProcess) Start(ctx context.Context) error {
	p.mu.Lock()
	p.startCalls++
	gate := p.startGate
	err := p.startErr
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return err
}

func (p *fakeWriterProcess) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeCalls++
	return p.closeErr
}

func (p *fakeWriterProcess) calls() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.startCalls, p.closeCalls
}

func TestThreadClaimRegistryRejectsCrossWriterOwnership(t *testing.T) {
	registry := NewThreadClaimRegistry()
	legacy := ThreadClaim{Writer: "legacy", Generation: 1}
	afc := ThreadClaim{Writer: "afc", Generation: 1}

	if err := registry.Acquire("thread-a", legacy); err != nil {
		t.Fatalf("Acquire legacy thread-a: %v", err)
	}
	if err := registry.Acquire("thread-b", afc); err != nil {
		t.Fatalf("Acquire afc thread-b: %v", err)
	}
	if err := registry.Acquire("thread-a", afc); !errors.Is(err, ErrThreadClaimed) {
		t.Fatalf("cross-writer Acquire error = %v, want ErrThreadClaimed", err)
	}
	if got, ok := registry.Lookup("thread-a"); !ok || got != legacy {
		t.Fatalf("Lookup(thread-a) = %#v, %t; want %#v, true", got, ok, legacy)
	}

	registry.ReleaseWriter(legacy)
	if err := registry.Acquire("thread-a", afc); err != nil {
		t.Fatalf("Acquire after process release: %v", err)
	}
}

func TestWriterManagerConcurrentReservationsShareOneStart(t *testing.T) {
	registry := NewThreadClaimRegistry()
	startGate := make(chan struct{})
	process := &fakeWriterProcess{startGate: startGate}
	manager := NewWriterManager("afc", registry, func() (*fakeWriterProcess, error) {
		return process, nil
	})

	type result struct {
		lease WriterLease[*fakeWriterProcess]
		err   error
	}
	results := make(chan result, 2)
	for _, threadID := range []string{"thread-a", "thread-b"} {
		go func(threadID string) {
			lease, err := manager.Reserve(context.Background(), threadID)
			results <- result{lease: lease, err: err}
		}(threadID)
	}
	close(startGate)

	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatalf("Reserve failed: %v", result.err)
		}
		if result.lease.Process != process {
			t.Fatalf("Reserve process = %p, want %p", result.lease.Process, process)
		}
		if result.lease.Generation != 1 {
			t.Fatalf("Reserve generation = %d, want 1", result.lease.Generation)
		}
	}
	if starts, _ := process.calls(); starts != 1 {
		t.Fatalf("Start calls = %d, want 1", starts)
	}
}

func TestWriterManagerKeepsClaimsUntilLastTerminalClosesProcess(t *testing.T) {
	registry := NewThreadClaimRegistry()
	process := &fakeWriterProcess{}
	manager := NewWriterManager("afc", registry, func() (*fakeWriterProcess, error) {
		return process, nil
	})

	leaseA, err := manager.Reserve(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	leaseB, err := manager.Reserve(context.Background(), "thread-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkActive(leaseA); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkActive(leaseB); err != nil {
		t.Fatal(err)
	}

	if err := manager.MarkTerminal(leaseA); err != nil {
		t.Fatal(err)
	}
	if _, closes := process.calls(); closes != 0 {
		t.Fatalf("Close calls after first terminal = %d, want 0", closes)
	}
	if _, ok := registry.Lookup("thread-a"); !ok {
		t.Fatal("thread-a claim released before owning process closed")
	}

	if err := manager.MarkTerminal(leaseB); err != nil {
		t.Fatal(err)
	}
	if _, closes := process.calls(); closes != 1 {
		t.Fatalf("Close calls after last terminal = %d, want 1", closes)
	}
	if _, ok := registry.Lookup("thread-a"); ok {
		t.Fatal("thread-a claim remains after owning process closed")
	}
	if _, ok := registry.Lookup("thread-b"); ok {
		t.Fatal("thread-b claim remains after owning process closed")
	}
}

func TestWriterManagerUnknownDispatchBlocksCloseAndReplay(t *testing.T) {
	registry := NewThreadClaimRegistry()
	process := &fakeWriterProcess{}
	manager := NewWriterManager("afc", registry, func() (*fakeWriterProcess, error) {
		return process, nil
	})
	lease, err := manager.Reserve(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkUnknown(lease); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Reserve(context.Background(), "thread-a"); !errors.Is(err, ErrWriterThreadBusy) {
		t.Fatalf("second Reserve error = %v, want ErrWriterThreadBusy", err)
	}
	if err := manager.BeginDrain(); !errors.Is(err, ErrWriterBusy) {
		t.Fatalf("BeginDrain error = %v, want ErrWriterBusy", err)
	}
	if _, closes := process.calls(); closes != 0 {
		t.Fatalf("Close calls with unknown dispatch = %d, want 0", closes)
	}
	if err := manager.MarkTerminal(lease); err != nil {
		t.Fatal(err)
	}
	if _, closes := process.calls(); closes != 1 {
		t.Fatalf("Close calls after terminal = %d, want 1", closes)
	}
}

func TestWriterManagerStartFailureReleasesGenerationClaims(t *testing.T) {
	registry := NewThreadClaimRegistry()
	process := &fakeWriterProcess{startErr: errors.New("start failed")}
	manager := NewWriterManager("legacy", registry, func() (*fakeWriterProcess, error) {
		return process, nil
	})
	if _, err := manager.Reserve(context.Background(), "thread-a"); err == nil {
		t.Fatal("Reserve succeeded, want start failure")
	}
	if _, ok := registry.Lookup("thread-a"); ok {
		t.Fatal("claim remains after process start failure")
	}
	if got := manager.Snapshot(); got.State != WriterStopped || got.Generation != 1 {
		t.Fatalf("Snapshot after start failure = %#v", got)
	}
}

func TestWriterManagerCloseFailureKeepsClaimsAndFailsClosed(t *testing.T) {
	registry := NewThreadClaimRegistry()
	process := &fakeWriterProcess{closeErr: errors.New("close failed")}
	manager := NewWriterManager("afc", registry, func() (*fakeWriterProcess, error) {
		return process, nil
	})
	lease, err := manager.Reserve(context.Background(), "thread-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkActive(lease); err != nil {
		t.Fatal(err)
	}
	if err := manager.MarkTerminal(lease); err == nil {
		t.Fatal("MarkTerminal succeeded, want close failure")
	}
	if _, ok := registry.Lookup("thread-a"); !ok {
		t.Fatal("claim released despite process close failure")
	}
	if got := manager.Snapshot(); got.State != WriterClosing {
		t.Fatalf("State = %q, want %q", got.State, WriterClosing)
	}
	if _, err := manager.Reserve(context.Background(), "thread-b"); !errors.Is(err, ErrWriterClosing) {
		t.Fatalf("Reserve while close unresolved = %v, want ErrWriterClosing", err)
	}
}
