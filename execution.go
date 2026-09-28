package inofy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ProjectViVy/inofy/internal/definition"
	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

// persistenceTimeout bounds the detached commit context: cancellation
// of the run must not prevent recording a known outcome (§6), but a
// wedged store may not block settlement forever.
const persistenceTimeout = 5 * time.Second

// RetryableError wraps a host-classified failure that retry policy may
// attempt again (§5.5). Plain errors are never retried.
type RetryableError struct{ Err error }

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// UnknownOutcomeError marks an effect that may have happened remotely;
// it is never auto-retried and dominates the run result (§8.5).
type UnknownOutcomeError struct{ Err error }

func (e *UnknownOutcomeError) Error() string { return e.Err.Error() }
func (e *UnknownOutcomeError) Unwrap() error { return e.Err }

// runJournal is the per-run bounded accounting and commit boundary. It
// is shared by every node activation in one Run, including nested
// container bodies (§7.3 step 2 and §7.4).
type runJournal struct {
	ref   ExecutionRef
	store RunStore
	gen   int // continuation generation; 0 until S06 checkpoints

	ord atomic.Uint64 // commit ordinal counter

	permits  chan struct{} // run-shared leaf effect permits
	inLimit  int64         // MaxNodeInputBytes
	outLimit int64         // MaxNodeOutputBytes
	totLimit int64         // MaxOutputBytesTotal
	outBytes atomic.Int64
	acts     atomic.Int64
	actLimit int64
	nodeTO   time.Duration
	runTO    time.Duration

	unknown  atomic.Bool // an effect outcome is uncertain
	recovery atomic.Bool // a required commit failed mid-run

	suspending atomic.Bool // a wait committed; the leaf gate is closed

	attempts atomic.Int64 // attempt commits for the Usage snapshot
	waitsMu  sync.Mutex
	waits    []WaitRequest // outstanding waits in commit order

	clock *runClock
}

func newRunJournal(ref ExecutionRef, store RunStore, lim Limits) *runJournal {
	par := lim.Parallelism
	if par <= 0 {
		par = 4
	}
	return &runJournal{
		ref:      ref,
		store:    store,
		permits:  make(chan struct{}, par),
		inLimit:  lim.MaxNodeInputBytes,
		outLimit: lim.MaxNodeOutputBytes,
		totLimit: lim.MaxOutputBytesTotal,
		actLimit: int64(lim.MaxActivations),
		nodeTO:   time.Duration(lim.NodeTimeoutMS) * time.Millisecond,
		runTO:    time.Duration(lim.RunTimeoutMS) * time.Millisecond,
		clock:    newRunClock(),
	}
}

// commitID is stable across redelivery: run, generation, node path,
// attempt and a monotonically assigned transition ordinal (§8.2).
func (j *runJournal) commitID(path string, attempt int) string {
	ord := j.ord.Add(1)
	return fmt.Sprintf("%s/%d/%s/%d/%d", j.ref.RunID, j.gen, path, attempt, ord)
}

// commit sends one atomic change through the store on a bounded
// detached persistence context. A nil store is the documented
// no-journal embedding: progress is allowed without durable evidence.
func (j *runJournal) commit(ctx context.Context, path string, attempt int,
	tr StateTransition, events []Event, results []ProtectedResult) error {
	if j.store == nil {
		return nil
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancel()
	_, err := j.store.Commit(pctx, j.ref, RunCommit{
		CommitID:   j.commitID(path, attempt),
		Events:     events,
		Results:    results,
		Transition: tr,
	})
	if err != nil {
		var ie *Error
		if errors.As(err, &ie) {
			return err
		}
		return &Error{Code: ErrStorageFailed, Path: path, Err: err,
			Message: "run store commit failed"}
	}
	return nil
}

// commitEnvelope sends the one atomic suspension boundary: events plus
// the staged checkpoint envelope, under the waiting transition (§8.3
// step 4). A failure leaves the run running, never waiting.
func (j *runJournal) commitEnvelope(ctx context.Context, tr StateTransition,
	events []Event, env *CheckpointEnvelope) error {
	if j.store == nil {
		return nil
	}
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistenceTimeout)
	defer cancel()
	_, err := j.store.Commit(pctx, j.ref, RunCommit{
		CommitID:   j.commitID(j.ref.RunID, j.gen),
		Events:     events,
		Checkpoint: env,
		Transition: tr,
	})
	if err != nil {
		var ie *Error
		if errors.As(err, &ie) {
			return err
		}
		return &Error{Code: ErrStorageFailed, Err: err,
			Message: "run store commit failed"}
	}
	return nil
}

// usage snapshots the journal's spent budget for the checkpoint
// envelope; the clock is already frozen when the caller is quiescent.
func (j *runJournal) usage() Usage {
	return Usage{
		Activations:          int(j.acts.Load()),
		Attempts:             int(j.attempts.Load()),
		CompletedOutputBytes: j.outBytes.Load(),
		ActiveMS:             j.clock.ActiveMS(),
	}
}

// outstandingWaits returns the durable waits in commit order.
func (j *runJournal) outstandingWaits() []WaitRequest {
	j.waitsMu.Lock()
	defer j.waitsMu.Unlock()
	out := make([]WaitRequest, len(j.waits))
	copy(out, j.waits)
	return out
}

// executeCall runs one logical node activation under §7.3: bound-check,
// permit, commit-before-effect, per-attempt invoke, commit-before-
// propagation, bounded retry and typed fallback (§5.5).
func (j *runJournal) executeCall(ctx context.Context, exec NodeExecutor,
	c einoruntime.Call) (json.RawMessage, error) {
	// The suspension flag closes the effect gate before any
	// accounting: no new effect starts, no activation is spent, no
	// attempt is committed once a wait exists (§8.3 step 2).
	if j.suspending.Load() {
		return nil, &einoruntime.GateSuspend{Path: c.Path}
	}
	if j.inLimit > 0 && int64(len(c.Input)) > j.inLimit {
		return nil, &Error{Code: ErrBudgetExceeded, Path: c.Path,
			Message: "node input exceeds max_node_input_bytes"}
	}
	if j.actLimit > 0 && j.acts.Add(1) > j.actLimit {
		return nil, &Error{Code: ErrBudgetExceeded, Path: c.Path,
			Message: "activation bound exceeded"}
	}
	select {
	case j.permits <- struct{}{}:
		defer func() { <-j.permits }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	max := c.MaxAttempts
	if max <= 0 {
		max = 1
	}
	// Non-replayable effects never auto-retry (§5.5, S05 task 2).
	if c.Replay == string(ReplayNonReplayable) {
		max = 1
	}
	opKey := j.ref.RunID + "/" + c.Path
	var lastErr error
	for attempt := 1; attempt <= max; attempt++ {
		events := []Event{{Kind: EventNodeAttempt, Path: c.Path, Attempt: attempt}}
		if attempt == 1 {
			events = append([]Event{{Kind: EventNodeStarted, Path: c.Path}}, events...)
		}
		j.attempts.Add(1)
		if err := j.commit(ctx, c.Path, attempt,
			StateTransition{Expected: RunRunning, Target: RunRunning}, events, nil); err != nil {
			// No effect may be invoked when the start record fails.
			j.recovery.Store(true)
			return nil, err
		}
		actx := ctx
		var cancel context.CancelFunc
		to := j.nodeTO
		if c.TimeoutMS > 0 {
			to = time.Duration(c.TimeoutMS) * time.Millisecond
		}
		if to > 0 {
			actx, cancel = context.WithTimeout(ctx, to)
		}
		reply, callErr := exec.Execute(actx, NodeCall{
			Ref:              j.ref,
			Path:             c.Path,
			TypeID:           c.TypeID,
			ImplementationID: c.ImplementationID,
			Config:           c.Config,
			Input:            c.Input,
			OperationKey:     opKey,
			Attempt:          attempt,
			Continuation:     c.ResumeAnswer,
		})
		if cancel != nil {
			cancel()
		}
		if callErr == nil {
			if err := reply.Validate(); err != nil {
				lastErr = err
				break
			}
			if reply.Wait != nil {
				if !c.SupportsWait {
					lastErr = &Error{Code: ErrAuthorityDenied, Path: c.Path,
						Message: "node type does not declare supports_wait"}
					break
				}
				wr := reply.Wait
				data, merr := json.Marshal(wr)
				if merr != nil {
					lastErr = &Error{Code: ErrInvalidDefinition, Path: c.Path, Err: merr}
					break
				}
				// The wait prompt is durable metadata before the
				// interrupt exists; the gate closes first so no
				// further effect can start in this run (§8.3).
				j.suspending.Store(true)
				if err := j.commit(ctx, c.Path, attempt,
					StateTransition{Expected: RunRunning, Target: RunRunning},
					[]Event{{Kind: EventNodeWait, Path: c.Path, Attempt: attempt,
						Data: data}}, nil); err != nil {
					j.recovery.Store(true)
					return nil, err
				}
				j.waitsMu.Lock()
				j.waits = append(j.waits, *wr)
				j.waitsMu.Unlock()
				j.attempts.Add(1)
				return nil, &einoruntime.SuspendRequest{
					RequestID:       wr.RequestID,
					ContinuationRef: wr.ContinuationRef,
				}
			}
			out := reply.Output
			if j.outLimit > 0 && int64(len(out)) > j.outLimit {
				lastErr = &Error{Code: ErrBudgetExceeded, Path: c.Path,
					Message: "node output exceeds max_node_output_bytes"}
				break
			}
			if j.totLimit > 0 && j.outBytes.Add(int64(len(out))) > j.totLimit {
				lastErr = &Error{Code: ErrBudgetExceeded, Path: c.Path,
					Message: "cumulative outputs exceed max_output_bytes_total"}
				break
			}
			if err := j.commit(ctx, c.Path, attempt,
				StateTransition{Expected: RunRunning, Target: RunRunning},
				[]Event{{Kind: EventNodeCompleted, Path: c.Path, Attempt: attempt}},
				[]ProtectedResult{{Path: c.Path, Attempt: attempt, Output: out}}); err != nil {
				j.recovery.Store(true)
				return nil, err
			}
			return out, nil
		}
		var u *UnknownOutcomeError
		if errors.As(callErr, &u) {
			j.unknown.Store(true)
			lastErr = &Error{Code: ErrOutcomeUnknown, Path: c.Path, Err: callErr,
				Message: "effect outcome is unknown"}
			break
		}
		var r *RetryableError
		if !errors.As(callErr, &r) {
			lastErr = callErr
			break
		}
		lastErr = callErr
		if attempt < max && c.DelayMS > 0 {
			t := time.NewTimer(time.Duration(c.DelayMS) * time.Millisecond)
			select {
			case <-ctx.Done():
				t.Stop()
				return nil, ctx.Err()
			case <-t.C:
			}
		}
	}
	// All attempts spent or a non-retryable error.
	if len(c.OnError) > 0 {
		var lit any
		if err := json.Unmarshal(c.OnError, &lit); err == nil {
			if err := definition.ValidateValueJSON(c.OutputSchema, lit); err == nil {
				if err := j.commit(ctx, c.Path, 0,
					StateTransition{Expected: RunRunning, Target: RunRunning},
					[]Event{{Kind: EventNodeDegraded, Path: c.Path,
						Data: json.RawMessage(fmt.Sprintf(`{"cause":%q}`, errCode(lastErr)))}},
					[]ProtectedResult{{Path: c.Path, Output: c.OnError}}); err != nil {
					j.recovery.Store(true)
					return nil, err
				}
				return c.OnError, nil
			}
		}
	}
	_ = j.commit(ctx, c.Path, 0,
		StateTransition{Expected: RunRunning, Target: RunRunning},
		[]Event{{Kind: EventNodeFailed, Path: c.Path}}, nil)
	var ie *Error
	if errors.As(lastErr, &ie) {
		return nil, lastErr
	}
	return nil, &Error{Code: ErrNodeFailed, Path: c.Path, Err: lastErr,
		Message: "node execution failed"}
}

func errCode(err error) string {
	var ie *Error
	if errors.As(err, &ie) {
		return string(ie.Code)
	}
	return "node_failed"
}

// --- run-scoped clock -------------------------------------------------

// runClock tracks consumed active-run time (§7.4): the clock freezes
// while the run is staged waiting and never resets on resume.
type runClock struct {
	mu      sync.Mutex
	start   time.Time
	frozen  time.Duration
	paused  bool
	pauseAt time.Time
}

func newRunClock() *runClock { return &runClock{start: time.Now()} }

func (c *runClock) Pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.paused {
		c.paused = true
		c.pauseAt = time.Now()
	}
}

func (c *runClock) Resume() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.paused {
		c.frozen += time.Since(c.pauseAt)
		c.paused = false
	}
}

// ActiveMS reports consumed active milliseconds excluding paused spans.
func (c *runClock) ActiveMS() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	end := time.Now()
	if c.paused {
		end = c.pauseAt
	}
	return end.Sub(c.start).Milliseconds() - c.frozen.Milliseconds()
}
