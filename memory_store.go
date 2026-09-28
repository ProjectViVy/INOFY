package inofy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/ProjectViVy/inofy/internal/definition"
)

// MemoryRunStore is the in-memory RunStore adapter for tests and
// explicit ephemeral embedding (§6). It implements identical Commit
// semantics — atomic transitions, CommitID idempotency, epoch fencing —
// and advertises no restart recovery: Load only sees this process.
type MemoryRunStore struct {
	mu   sync.Mutex
	runs map[string]*memRun
	seq  uint64
}

type memRun struct {
	ref        ExecutionRef
	status     RunStatus
	commits    map[string]memCommit
	unresolved map[string]OperationRef
	events     []Event
	results    int
	usage      Usage
}

type memCommit struct {
	body    string
	receipt Receipt
}

// NewMemoryRunStore returns a process-local ephemeral store.
func NewMemoryRunStore() *MemoryRunStore {
	return &MemoryRunStore{runs: map[string]*memRun{}}
}

// Commit applies one atomic change. Replaying the same CommitID with
// the same canonical body returns the original receipt; a changed body
// is idempotency_conflict. Stale epochs and wrong transition
// expectations fail before any write.
func (m *MemoryRunStore) Commit(ctx context.Context, ref ExecutionRef, change RunCommit) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	body, err := commitBody(change)
	if err != nil {
		return Receipt{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[ref.RunID]
	if !ok {
		if change.Transition.Expected != "" {
			return Receipt{}, &Error{Code: ErrRevisionConflict, Path: ref.RunID,
				Message: "run is not admitted"}
		}
		run = &memRun{
			ref:        ref,
			commits:    map[string]memCommit{},
			unresolved: map[string]OperationRef{},
		}
		m.runs[ref.RunID] = run
	}
	if prev, ok := run.commits[change.CommitID]; ok {
		if prev.body == body {
			return prev.receipt, nil
		}
		return Receipt{}, &Error{Code: ErrIdempotencyConflict, Path: ref.RunID,
			Message: "commit id reused with a different body"}
	}
	if run != nil {
		if run.ref.Epoch != ref.Epoch {
			return Receipt{}, &Error{Code: ErrStaleWriter, Path: ref.RunID,
				Message: "writer epoch mismatch"}
		}
		if run.ref.ProgramDigest != ref.ProgramDigest {
			return Receipt{}, &Error{Code: ErrCheckpointIncompatible, Path: ref.RunID,
				Message: "program digest mismatch"}
		}
		if run.status != change.Transition.Expected {
			return Receipt{}, &Error{Code: ErrRevisionConflict, Path: ref.RunID,
				Message: fmt.Sprintf("run is %q, commit expects %q",
					run.status, change.Transition.Expected)}
		}
	}
	run.status = change.Transition.Target
	for _, ev := range change.Events {
		evCopy := ev
		if len(evCopy.Data) > 0 {
			evCopy.Data = append([]byte(nil), evCopy.Data...)
		}
		run.events = append(run.events, evCopy)
		key := opKeyFor(ev)
		switch ev.Kind {
		case EventNodeAttempt:
			run.unresolved[key] = OperationRef{
				OperationKey: ref.RunID + "/" + ev.Path,
				Path:         ev.Path,
				Attempt:      ev.Attempt,
			}
		case EventNodeCompleted, EventNodeFailed, EventNodeDegraded,
			EventNodeWait, EventNodeStarted:
			if ev.Kind != EventNodeStarted {
				delete(run.unresolved, key)
			}
		}
		run.usage.Attempts++
	}
	for _, r := range change.Results {
		run.unresolved[fmt.Sprintf("%s/%d", r.Path, r.Attempt)] = OperationRef{
			OperationKey: ref.RunID + "/" + r.Path, Path: r.Path, Attempt: r.Attempt,
		}
		delete(run.unresolved, fmt.Sprintf("%s/%d", r.Path, r.Attempt))
		run.results++
		run.usage.CompletedOutputBytes += int64(len(r.Output))
	}
	m.seq += uint64(len(change.Events))
	if m.seq == 0 {
		m.seq = 1
	}
	rcpt := Receipt{FirstSequence: m.seq, LastSequence: m.seq}
	run.commits[change.CommitID] = memCommit{body: body, receipt: rcpt}
	return rcpt, nil
}

// Load returns the recorded recovery state for one admitted run.
func (m *MemoryRunStore) Load(ctx context.Context, runID string) (RecoveryState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return RecoveryState{}, &Error{Code: ErrRevisionConflict, Path: runID,
			Message: "unknown run"}
	}
	ops := make([]OperationRef, 0, len(run.unresolved))
	for _, op := range run.unresolved {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].OperationKey < ops[j].OperationKey })
	return RecoveryState{
		Ref:                  run.ref,
		Status:               run.status,
		Usage:                run.usage,
		UnresolvedOperations: ops,
	}, nil
}

// Events exposes the committed event projection for tests and SSE.
func (m *MemoryRunStore) Events(runID string) []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[runID]
	if !ok {
		return nil
	}
	out := make([]Event, len(run.events))
	copy(out, run.events)
	return out
}

func opKeyFor(ev Event) string { return fmt.Sprintf("%s/%d", ev.Path, ev.Attempt) }

// commitBody canonicalizes the semantic body (CommitID excluded) for
// idempotency comparison via the JSON wire form.
func commitBody(c RunCommit) (string, error) {
	raw, err := json.Marshal(map[string]any{
		"events":     c.Events,
		"results":    c.Results,
		"checkpoint": c.Checkpoint,
		"transition": c.Transition,
	})
	if err != nil {
		return "", &Error{Code: ErrInvalidDefinition, Err: err,
			Message: "commit body cannot marshal"}
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", &Error{Code: ErrInvalidDefinition, Err: err}
	}
	b, err := definition.Canonical(doc)
	if err != nil {
		return "", &Error{Code: ErrInvalidDefinition, Err: err,
			Message: "commit body is not canonicalizable"}
	}
	return string(b), nil
}
