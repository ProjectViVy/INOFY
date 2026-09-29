// Package dispatch is the App's bounded in-memory wakeup surface:
// Admit persists an immutable admission row before Enqueue; Start
// reconciles durable rows on boot (running→recovery_required,
// waiting stays quiescent, admitted dispatches once) and drains the
// pending queue onto bounded workers. SQLite is the sole authority;
// the queue only wakes work the store already committed.
package dispatch

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
)

// Options bound the dispatch surface (§11.2 defaults).
type Options struct {
	MaxActive  int
	MaxPending int
}

// ProgramFactory rebuilds the compiled program + bindings for a
// durable run row: the store never holds live executors.
type ProgramFactory func(runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error)

// Service owns worker lifecycle; Stop drains without faking
// settlement of unknown effects.
type Service struct {
	store   *storage.Store
	opts    Options
	factory ProgramFactory

	mu       sync.Mutex
	wake     chan struct{}
	stopped  chan struct{}
	cancel   context.CancelFunc
	running  bool
	inflight sync.WaitGroup
	executed atomic.Int64
	lifelines map[string]context.CancelFunc
}

// New binds a dispatcher to its storage authority.
func New(store *storage.Store, opts Options, factory ProgramFactory) *Service {
	if opts.MaxActive <= 0 {
		opts.MaxActive = 4
	}
	if opts.MaxPending <= 0 {
		opts.MaxPending = 32
	}
	return &Service{
		store:   store,
		opts:    opts,
		factory: factory,
		wake:      make(chan struct{}, 1),
		stopped:   make(chan struct{}),
		lifelines: map[string]context.CancelFunc{},
	}
}

// Admit persists the immutable admission row — source snapshot
// (revision artifact or draft-ETag artifact), input and idempotency
// key — then wakes the queue. The commit happens BEFORE dispatch:
// a crash after Admit returns leaves an inspectable row the next
// boot dispatches exactly once.
func (s *Service) Admit(ctx context.Context, principal, workflowID string, revision uint64, source, input json.RawMessage, key string) (string, error) {
	pending, err := s.store.PendingCount(ctx)
	if err != nil {
		return "", err
	}
	if pending >= s.opts.MaxPending {
		return "", &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "pending admission bound reached"}
	}
	runID, err := s.store.Admit(ctx, principal, workflowID, revision, source, input, key)
	if err != nil {
		return "", err
	}
	s.signal()
	return runID, nil
}

// Start reconciles durable state and starts the wakeup loop.
func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	// Crash classification before any dispatch: rows left 'running'
	// by a dead process are recovery_required, never replayed.
	if running, err := s.store.RunningRuns(ctx); err == nil {
		for _, id := range running {
			_ = s.store.MarkRecoveryRequired(ctx, id, 0)
		}
	}
	// Dead claims provably never committed: requeue them.
	_ = s.store.RequeueClaims(ctx)
	s.inflight.Add(1)
	go s.loop(ctx)
	return nil
}

// Stop stops new admissions from being consumed and waits for the
// loop to exit. In-flight runs are cancelled by context; their
// durable rows stay honest (running → classified at next Start).
func (s *Service) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.cancel()
	s.mu.Unlock()
	s.inflight.Wait()
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// loop drains the dispatchable backlog one slot at a time; the queue
// never holds hidden work — every runnable row is durable first.
func (s *Service) loop(ctx context.Context) {
	defer s.inflight.Done()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		worked := s.drainOnce(ctx)
		if !worked {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
			}
		}
	}
}

func (s *Service) drainOnce(ctx context.Context) bool {
	active, err := s.store.ActiveCount(ctx)
	if err != nil {
		return false
	}
	free := s.opts.MaxActive - active
	if free <= 0 {
		return false
	}
	ids, err := s.store.Dispatchable(ctx, free)
	if err != nil || len(ids) == 0 {
		return false
	}
	for _, id := range ids {
		select {
		case <-ctx.Done():
			return true
		default:
		}
		s.inflight.Add(1)
		go s.runOne(ctx, id)
	}
	return true
}

// runOne claims the durable row and executes exactly once per
// process. A 'queued' placeholder first commits its admission event;
// a durable 'admitted' row resumes from the committed record. Any
// other status is a reconciliation no-op.
func (s *Service) runOne(ctx context.Context, runID string) {
	defer s.inflight.Done()
	// Claim before any effect: only the dispatcher that flips the
	// durable row to 'claimed' may execute; concurrent drains lose
	// the CAS and run zero effects.
	ok, err := s.store.Claim(ctx, runID)
	if err != nil || !ok {
		return
	}
	prog, bindings, req, err := s.factory(runID)
	if err != nil {
		s.store.ReleaseClaim(ctx, runID)
		return
	}
	// Register the run's lifeline so POST /cancel can reach the
	// in-flight execution; unregistered when the run settles.
	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.lifelines[runID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.lifelines, runID)
		s.mu.Unlock()
	}()
	s.executed.Add(1)
	// Run() performs admission/start commits idempotently — the
	// 'claimed' placeholder is adopted by the first commit's
	// Expected="" transition (same notAdmitted path as 'queued'),
	// and a durable 'admitted' row continues from its record.
	_, _ = prog.Run(runCtx, req, bindings)
}

// Cancel requests idempotent run cancellation (§11.4): live
// executions get their context cancelled (the engine commits
// run_cancelled); quiescent rows transition durably without ever
// having had an executor.
func (s *Service) Cancel(ctx context.Context, runID string) error {
	s.mu.Lock()
	cancel, live := s.lifelines[runID]
	s.mu.Unlock()
	if live {
		cancel()
		return nil
	}
	ok, err := s.store.CancelRun(ctx, runID)
	if err != nil {
		return err
	}
	if !ok {
		// Already terminal — cancellation is idempotent.
		if _, err := s.store.StatusOf(ctx, runID); err != nil {
			return err
		}
	}
	return nil
}

// ProgramFor rebuilds the compiled program, bindings and admitted
// request for a run — the resume path needs the live executor to
// finish nodes the claim unblocks and the original input to keep
// checkpoint identity checks intact.
func (s *Service) ProgramFor(_ context.Context, runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error) {
	return s.factory(runID)
}
