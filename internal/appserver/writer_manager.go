package appserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

var (
	ErrThreadClaimed    = errors.New("thread is claimed by another writer process")
	ErrWriterBusy       = errors.New("writer has unfinished thread work")
	ErrWriterClosing    = errors.New("writer process close is unresolved")
	ErrWriterDraining   = errors.New("writer is not accepting new work")
	ErrWriterThreadBusy = errors.New("thread already has unfinished writer work")
	ErrInvalidLease     = errors.New("writer lease is stale or invalid")
)

type ThreadClaim struct {
	Writer     string
	Generation uint64
}

type ThreadClaimError struct {
	ThreadID  string
	Current   ThreadClaim
	Requested ThreadClaim
}

func (e *ThreadClaimError) Error() string {
	return fmt.Sprintf("thread %q is claimed by writer %q generation %d", e.ThreadID, e.Current.Writer, e.Current.Generation)
}

func (e *ThreadClaimError) Unwrap() error { return ErrThreadClaimed }

// ThreadClaimRegistry coordinates process-level ownership between independent
// writer managers. A claim is deliberately released only after the owning
// process has closed; a terminal turn alone is not evidence that App Server has
// unloaded the thread.
type ThreadClaimRegistry struct {
	mu     sync.Mutex
	claims map[string]ThreadClaim
}

func NewThreadClaimRegistry() *ThreadClaimRegistry {
	return &ThreadClaimRegistry{claims: map[string]ThreadClaim{}}
}

func (r *ThreadClaimRegistry) Acquire(threadID string, claim ThreadClaim) error {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return errors.New("thread id is required")
	}
	if strings.TrimSpace(claim.Writer) == "" || claim.Generation == 0 {
		return errors.New("writer claim requires a name and generation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.claims[threadID]; ok && current != claim {
		return &ThreadClaimError{ThreadID: threadID, Current: current, Requested: claim}
	}
	r.claims[threadID] = claim
	return nil
}

func (r *ThreadClaimRegistry) Release(threadID string, claim ThreadClaim) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.claims[threadID]; ok && current == claim {
		delete(r.claims, threadID)
	}
}

func (r *ThreadClaimRegistry) ReleaseWriter(claim ThreadClaim) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for threadID, current := range r.claims {
		if current == claim {
			delete(r.claims, threadID)
		}
	}
}

func (r *ThreadClaimRegistry) Lookup(threadID string) (ThreadClaim, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claim, ok := r.claims[strings.TrimSpace(threadID)]
	return claim, ok
}

type WriterProcess interface {
	Start(context.Context) error
	Close() error
}

type WriterFactory[T WriterProcess] func() (T, error)

type WriterState string

const (
	WriterStopped  WriterState = "stopped"
	WriterStarting WriterState = "starting"
	WriterRunning  WriterState = "running"
	WriterClosing  WriterState = "closing"
)

type WriterThreadState string

const (
	WriterThreadStarting WriterThreadState = "starting"
	WriterThreadActive   WriterThreadState = "active"
	WriterThreadUnknown  WriterThreadState = "unknown"
)

type WriterLease[T WriterProcess] struct {
	Writer     string
	ThreadID   string
	Generation uint64
	Process    T
}

type WriterSnapshot struct {
	Name       string
	State      WriterState
	Accepting  bool
	Generation uint64
	Starting   int
	Active     int
	Unknown    int
}

// WriterManager owns one lazy App Server process at a time. It tracks only
// operations whose dispatch is not known to be terminal; ThreadClaimRegistry
// separately retains every thread loaded by the process until Close succeeds.
type WriterManager[T WriterProcess] struct {
	mu sync.Mutex

	name       string
	registry   *ThreadClaimRegistry
	factory    WriterFactory[T]
	state      WriterState
	accepting  bool
	generation uint64
	process    T
	startDone  chan struct{}
	startErr   error
	threads    map[string]WriterThreadState
}

func NewWriterManager[T WriterProcess](name string, registry *ThreadClaimRegistry, factory WriterFactory[T]) *WriterManager[T] {
	if registry == nil {
		registry = NewThreadClaimRegistry()
	}
	return &WriterManager[T]{
		name:      strings.TrimSpace(name),
		registry:  registry,
		factory:   factory,
		state:     WriterStopped,
		accepting: true,
		threads:   map[string]WriterThreadState{},
	}
}

func (m *WriterManager[T]) Reserve(ctx context.Context, threadID string) (WriterLease[T], error) {
	var zero WriterLease[T]
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return zero, errors.New("thread id is required")
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	m.mu.Lock()
	if !m.accepting {
		m.mu.Unlock()
		return zero, ErrWriterDraining
	}
	if m.state == WriterClosing {
		m.mu.Unlock()
		return zero, ErrWriterClosing
	}
	if _, exists := m.threads[threadID]; exists {
		m.mu.Unlock()
		return zero, ErrWriterThreadBusy
	}

	if m.state == WriterStopped {
		m.generation++
		claim := ThreadClaim{Writer: m.name, Generation: m.generation}
		if err := m.registry.Acquire(threadID, claim); err != nil {
			m.mu.Unlock()
			return zero, err
		}
		process, err := m.factory()
		if err != nil {
			m.registry.Release(threadID, claim)
			m.mu.Unlock()
			return zero, err
		}
		m.process = process
		m.state = WriterStarting
		m.startDone = make(chan struct{})
		m.startErr = nil
		m.threads[threadID] = WriterThreadStarting
		generation := m.generation
		m.mu.Unlock()

		err = process.Start(ctx)
		if err != nil {
			closeErr := process.Close()
			combined := errors.Join(err, closeErr)
			m.mu.Lock()
			if m.generation == generation && m.state == WriterStarting {
				m.startErr = combined
				m.threads = map[string]WriterThreadState{}
				if closeErr == nil {
					var empty T
					m.process = empty
					m.state = WriterStopped
				} else {
					m.state = WriterClosing
				}
				close(m.startDone)
			}
			m.mu.Unlock()
			if closeErr == nil {
				m.registry.ReleaseWriter(claim)
			}
			return zero, combined
		}

		m.mu.Lock()
		if m.generation != generation || m.state != WriterStarting {
			m.mu.Unlock()
			return zero, ErrInvalidLease
		}
		m.state = WriterRunning
		close(m.startDone)
		lease := WriterLease[T]{Writer: m.name, ThreadID: threadID, Generation: generation, Process: process}
		m.mu.Unlock()
		return lease, nil
	}

	if m.state == WriterStarting {
		claim := ThreadClaim{Writer: m.name, Generation: m.generation}
		if err := m.registry.Acquire(threadID, claim); err != nil {
			m.mu.Unlock()
			return zero, err
		}
		m.threads[threadID] = WriterThreadStarting
		generation := m.generation
		done := m.startDone
		m.mu.Unlock()

		select {
		case <-done:
		case <-ctx.Done():
			// Wait for the shared start result before returning so the reservation
			// cannot be abandoned while the process outcome is still ambiguous.
			<-done
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.generation != generation || m.startErr != nil {
			if m.startErr != nil {
				return zero, m.startErr
			}
			return zero, ErrInvalidLease
		}
		if m.state != WriterRunning {
			return zero, ErrWriterClosing
		}
		return WriterLease[T]{Writer: m.name, ThreadID: threadID, Generation: generation, Process: m.process}, nil
	}

	claim := ThreadClaim{Writer: m.name, Generation: m.generation}
	if err := m.registry.Acquire(threadID, claim); err != nil {
		m.mu.Unlock()
		return zero, err
	}
	m.threads[threadID] = WriterThreadStarting
	lease := WriterLease[T]{Writer: m.name, ThreadID: threadID, Generation: m.generation, Process: m.process}
	m.mu.Unlock()
	return lease, nil
}

func (m *WriterManager[T]) MarkActive(lease WriterLease[T]) error {
	return m.transition(lease, WriterThreadStarting, WriterThreadActive)
}

func (m *WriterManager[T]) MarkUnknown(lease WriterLease[T]) error {
	return m.transition(lease, WriterThreadStarting, WriterThreadUnknown)
}

func (m *WriterManager[T]) transition(lease WriterLease[T], from, to WriterThreadState) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.validLeaseLocked(lease) || m.threads[lease.ThreadID] != from {
		return ErrInvalidLease
	}
	m.threads[lease.ThreadID] = to
	return nil
}

func (m *WriterManager[T]) Abort(lease WriterLease[T]) error {
	m.mu.Lock()
	if !m.validLeaseLocked(lease) || m.threads[lease.ThreadID] != WriterThreadStarting {
		m.mu.Unlock()
		return ErrInvalidLease
	}
	delete(m.threads, lease.ThreadID)
	return m.closeIfIdleLocked()
}

func (m *WriterManager[T]) MarkTerminal(lease WriterLease[T]) error {
	m.mu.Lock()
	if !m.validLeaseLocked(lease) {
		m.mu.Unlock()
		return ErrInvalidLease
	}
	if _, ok := m.threads[lease.ThreadID]; !ok {
		m.mu.Unlock()
		return ErrInvalidLease
	}
	delete(m.threads, lease.ThreadID)
	return m.closeIfIdleLocked()
}

func (m *WriterManager[T]) BeginDrain() error {
	m.mu.Lock()
	m.accepting = false
	if len(m.threads) > 0 {
		m.mu.Unlock()
		return ErrWriterBusy
	}
	return m.closeIfIdleLocked()
}

func (m *WriterManager[T]) AcceptNewWork() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state == WriterClosing {
		return ErrWriterClosing
	}
	m.accepting = true
	return nil
}

func (m *WriterManager[T]) RetryClose() error {
	m.mu.Lock()
	if m.state != WriterClosing {
		m.mu.Unlock()
		return nil
	}
	process := m.process
	generation := m.generation
	m.mu.Unlock()
	return m.finishClose(process, generation)
}

func (m *WriterManager[T]) Snapshot() WriterSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := WriterSnapshot{Name: m.name, State: m.state, Accepting: m.accepting, Generation: m.generation}
	for _, state := range m.threads {
		switch state {
		case WriterThreadStarting:
			snapshot.Starting++
		case WriterThreadActive:
			snapshot.Active++
		case WriterThreadUnknown:
			snapshot.Unknown++
		}
	}
	return snapshot
}

func (m *WriterManager[T]) validLeaseLocked(lease WriterLease[T]) bool {
	return lease.Writer == m.name && lease.Generation == m.generation && strings.TrimSpace(lease.ThreadID) != ""
}

func (m *WriterManager[T]) closeIfIdleLocked() error {
	if len(m.threads) > 0 || m.state == WriterStopped {
		m.mu.Unlock()
		return nil
	}
	if m.state == WriterStarting {
		m.mu.Unlock()
		return ErrWriterBusy
	}
	if m.state == WriterClosing {
		m.mu.Unlock()
		return ErrWriterClosing
	}
	process := m.process
	generation := m.generation
	m.state = WriterClosing
	m.mu.Unlock()
	return m.finishClose(process, generation)
}

func (m *WriterManager[T]) finishClose(process T, generation uint64) error {
	if err := process.Close(); err != nil {
		return err
	}
	claim := ThreadClaim{Writer: m.name, Generation: generation}
	m.mu.Lock()
	if m.generation == generation && m.state == WriterClosing {
		var empty T
		m.process = empty
		m.state = WriterStopped
		m.startDone = nil
		m.startErr = nil
	}
	m.mu.Unlock()
	m.registry.ReleaseWriter(claim)
	return nil
}
