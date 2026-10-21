package inofy

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
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
	ref           ExecutionRef
	status        RunStatus
	commits       map[string]memCommit
	unresolved    map[string]OperationRef
	events        []Event
	results       int
	usage         Usage
	limits        Limits
	input         json.RawMessage
	checkpoint    *CheckpointEnvelope
	waits         []WaitRequest
	interrupts    map[string]string
	gates         []string
	resumeKey     string
	resumeAnswers map[string]json.RawMessage
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
	var admittedInput json.RawMessage
	if len(change.AdmittedInput) > 0 {
		admittedInput, err = NormalizeJSONValue(change.AdmittedInput)
		if err != nil {
			return Receipt{}, &Error{Code: ErrInvalidDefinition, Path: ref.RunID,
				Err: err, Message: "admitted input is not a valid JSON value"}
		}
	}
	var resumeAnswers map[string]json.RawMessage
	if change.ResumeKey != "" {
		resumeAnswers, err = normalizeAnswers(change.ResumeAnswers)
		if err != nil {
			return Receipt{}, &Error{Code: ErrInvalidDefinition, Path: ref.RunID,
				Err: err, Message: "resume claim contains invalid values"}
		}
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
	}
	if ok && (run.ref.ProgramID != ref.ProgramID || run.ref.HostBindingID != ref.HostBindingID) {
		return Receipt{}, &Error{Code: ErrCheckpointIncompatible, Path: ref.RunID,
			Message: "program or host binding identity mismatch"}
	}
	if prev, ok := run.commits[change.CommitID]; ok {
		if prev.body == body {
			return prev.receipt, nil
		}
		return Receipt{}, &Error{Code: ErrIdempotencyConflict, Path: ref.RunID,
			Message: "commit id reused with a different body"}
	}
	if run != nil {
		// Writer epoch CAS: a newer epoch claims the run; an older
		// one is a stale writer. Equal epochs continue the same claim.
		if ref.Epoch < run.ref.Epoch {
			return Receipt{}, &Error{Code: ErrStaleWriter, Path: ref.RunID,
				Message: "writer epoch mismatch"}
		}
		if run.status != change.Transition.Expected {
			return Receipt{}, &Error{Code: ErrRevisionConflict, Path: ref.RunID,
				Message: fmt.Sprintf("run is %q, commit expects %q",
					run.status, change.Transition.Expected)}
		}
	}
	if len(admittedInput) > 0 && len(run.input) > 0 && !sameJSON(run.input, admittedInput) {
		return Receipt{}, &Error{Code: ErrIdempotencyConflict, Path: ref.RunID,
			Message: "run was admitted with different input values"}
	}
	if !ok {
		m.runs[ref.RunID] = run
	}
	if ref.Epoch > run.ref.Epoch {
		run.ref.Epoch = ref.Epoch
	}
	run.status = change.Transition.Target
	if change.Checkpoint != nil {
		cp := *change.Checkpoint
		cp.Payload = append([]byte(nil), change.Checkpoint.Payload...)
		run.checkpoint = &cp
	}
	for _, ev := range change.Events {
		evCopy := ev
		if len(evCopy.Data) > 0 {
			evCopy.Data = append([]byte(nil), evCopy.Data...)
		}
		run.events = append(run.events, evCopy)
		if ev.Kind == EventRunAdmitted && len(ev.Data) > 0 {
			var meta struct {
				Limits Limits `json:"limits"`
			}
			if json.Unmarshal(ev.Data, &meta) == nil {
				run.limits = meta.Limits
			}
		}
		if ev.Kind == EventRunWaiting && len(ev.Data) > 0 {
			var meta struct {
				Waits      []WaitRequest     `json:"waits"`
				Interrupts map[string]string `json:"interrupts"`
				Gates      []string          `json:"gates"`
			}
			if json.Unmarshal(ev.Data, &meta) == nil {
				run.waits = meta.Waits
				run.interrupts = meta.Interrupts
				run.gates = meta.Gates
			}
		}
		if ev.Kind == EventRunResumed && len(ev.Data) > 0 {
			run.waits = nil
			run.interrupts = nil
			run.gates = nil
		}
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
	if len(admittedInput) > 0 {
		run.input = append(json.RawMessage(nil), admittedInput...)
	}
	if change.ResumeKey != "" {
		run.resumeKey = change.ResumeKey
		run.resumeAnswers = cloneRawMap(resumeAnswers)
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
	var cp *CheckpointEnvelope
	if run.checkpoint != nil {
		v := *run.checkpoint
		v.Payload = append([]byte(nil), run.checkpoint.Payload...)
		cp = &v
	}
	waits := make([]WaitRequest, len(run.waits))
	copy(waits, run.waits)
	ints := map[string]string{}
	for k, v := range run.interrupts {
		ints[k] = v
	}
	return RecoveryState{
		Ref:                  run.ref,
		Status:               run.status,
		AdmittedInput:        append(json.RawMessage(nil), run.input...),
		Limits:               run.limits,
		Usage:                run.usage,
		LatestCheckpoint:     cp,
		Waits:                waits,
		Interrupts:           ints,
		Gates:                append([]string(nil), run.gates...),
		ResumeKey:            run.resumeKey,
		ResumeAnswers:        cloneRawMap(run.resumeAnswers),
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
		"events":         c.Events,
		"results":        c.Results,
		"checkpoint":     c.Checkpoint,
		"transition":     c.Transition,
		"admitted_input": c.AdmittedInput,
		"resume_key":     c.ResumeKey,
		"resume_answers": c.ResumeAnswers,
	})
	if err != nil {
		return "", &Error{Code: ErrInvalidDefinition, Err: err,
			Message: "commit body cannot marshal"}
	}
	b, err := NormalizeJSONValue(raw)
	if err != nil {
		return "", &Error{Code: ErrInvalidDefinition, Err: err,
			Message: "commit body is not canonicalizable"}
	}
	return string(b), nil
}

func cloneRawMap(src map[string]json.RawMessage) map[string]json.RawMessage {
	if src == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(src))
	for key, value := range src {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
