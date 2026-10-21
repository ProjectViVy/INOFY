package inofy

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ProjectViVy/inofy/internal/definition"
	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

// Program is an immutable compiled artifact safe for concurrent Run
// invocations with different run IDs. It holds no run state, live
// credentials, host globals or per-run closures.
type Program struct {
	meta            ProgramMeta
	rt              *einoruntime.Program
	compileLimits   Limits
	catalogContract json.RawMessage
}

// ProgramMeta reports the immutable identity allocated by the definition
// owner and the compiler/build that produced this in-memory program.
type ProgramMeta struct {
	ProgramID       string `json:"program_id,omitempty"`
	CompilerVersion string `json:"compiler_version"`
	EinoBuild       string `json:"eino_build"`
}

// Meta reports the frozen program identity.
func (p *Program) Meta() ProgramMeta { return p.meta }

// Run executes one admitted run. Identity and limits are rechecked
// against the compiled program before any work; a mismatch or a limit
// wider than the compile ceiling is rejected. Resume is a S06 contract
// and reports unsupported_feature until then.
func (p *Program) Run(ctx context.Context, request RunRequest, bindings Bindings) (RunResult, error) {
	if p == nil || p.rt == nil || p.meta.ProgramID == "" || request.Ref.ProgramID != p.meta.ProgramID {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrCheckpointIncompatible,
			Path:    request.Ref.RunID,
			Message: "run request does not match the owner-allocated program identity",
		}
	}
	if request.Resume != nil {
		return p.resume(ctx, request, bindings)
	}
	input, err := NormalizeJSONValue(request.Input)
	if err != nil {
		return RunResult{Status: RunFailed}, &Error{Code: ErrInvalidDefinition, Path: request.Ref.RunID,
			Err: err, Message: "run input is not a valid JSON value"}
	}
	request.Input = input
	if bindings.Nodes == nil {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrBindingMissing,
			Path:    request.Ref.RunID,
			Message: "Bindings.Nodes executor is required",
		}
	}
	lim := effectiveLimits(request.Limits)
	if err := checkRunCeiling(lim, p.compileLimits); err != nil {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrBudgetExceeded,
			Path:    request.Ref.RunID,
			Message: err.Error(),
		}
	}
	j := newRunJournal(request.Ref, bindings.Runs, lim)
	// A prior record classifies this run before any new work (§8.5):
	// running on reopen means the process lost settlement; terminal
	// or waiting runs cannot be re-admitted by a fresh Run.
	if bindings.Runs != nil {
		if st, lerr := bindings.Runs.Load(ctx, request.Ref.RunID); lerr == nil {
			if st.Ref.ProgramID != request.Ref.ProgramID ||
				((st.Status != "queued" && st.Status != "claimed") && st.Ref.HostBindingID != request.Ref.HostBindingID) ||
				!sameJSON(st.AdmittedInput, request.Input) {
				return RunResult{Status: RunFailed}, &Error{Code: ErrIdempotencyConflict,
					Path: request.Ref.RunID, Message: "run ID was admitted with different program or input values"}
			}
			switch st.Status {
			case RunRunning:
				// Uncommitted effects may exist; only the ledger can
				// prove safety, so the honest state is
				// recovery_required (§8.5).
				if cerr := j.commit(ctx, request.Ref.RunID, 0,
					StateTransition{Expected: RunRunning, Target: RunRecoveryRequired},
					[]Event{{Kind: EventRunRecoveryRequired}}, nil); cerr != nil {
					return RunResult{Status: RunFailed}, cerr
				}
				return RunResult{
						Status: RunRecoveryRequired,
						Diagnostics: []Diagnostic{{
							Path:    request.Ref.RunID,
							Code:    string(ErrOutcomeUnknown),
							Message: "run record recovered running on reopen; unresolved effects require host reconciliation",
						}},
					}, &Error{Code: ErrOutcomeUnknown, Path: request.Ref.RunID,
						Message: "running run recovered on reopen"}
			case RunWaiting:
				return RunResult{Status: RunFailed}, &Error{
					Code:    ErrRevisionConflict,
					Path:    request.Ref.RunID,
					Message: "run is durably waiting; resume it instead",
				}
			case RunAdmitted:
				// Admitted but never started: continue normally —
				// the admission commit below replays idempotently.
			case "queued", "claimed":
				// App admission placeholder rows (§11.2): the first
				// Expected="" commit adopts them atomically.
			default:
				return RunResult{Status: st.Status}, &Error{
					Code:    ErrRevisionConflict,
					Path:    request.Ref.RunID,
					Message: "terminal run cannot be re-admitted",
				}
			}
		} else if !isNotFound(lerr) {
			return RunResult{Status: RunFailed}, &Error{Code: ErrStorageFailed, Path: request.Ref.RunID,
				Err: lerr, Message: "run store load failed before admission"}
		}
	}
	// Admission and start are visible transitions, never silent
	// progress (§8.1): a failed admission commit cannot start work.
	// Limits are a public event projection; the exact admitted input is
	// stored privately by the existing RunStore owner.
	admitData, _ := json.Marshal(map[string]any{
		"limits": lim,
	})
	if err := j.commitAdmission(ctx, request.Ref.RunID, 0,
		StateTransition{Expected: "", Target: RunAdmitted},
		[]Event{{Kind: EventRunAdmitted, Data: admitData}}, request.Input); err != nil {
		return RunResult{Status: RunFailed}, err
	}
	if err := j.commit(ctx, request.Ref.RunID, 0,
		StateTransition{Expected: RunAdmitted, Target: RunRunning},
		[]Event{{Kind: EventRunStarted}}, nil); err != nil {
		return RunResult{Status: RunFailed}, err
	}
	exec := executorAdapter(j, bindings.Nodes)
	res, err := p.rt.Invoke(ctx, request.Input, exec,
		einoruntime.RunOptions{
			CheckPointID: request.Ref.RunID + "/0",
			Gate:         &j.suspending,
		})
	status := RunSucceeded
	var runErr error
	var diags []definition.Finding
	out := res.Output
	diags = res.Diags
	switch {
	case res.Suspend != nil:
		return p.suspend(ctx, request, j, res)
	case j.recovery.Load() || j.unknown.Load():
		status = RunRecoveryRequired
		runErr = err
	case err != nil && ctx.Err() != nil:
		status = RunCancelled
		runErr = err
	case err != nil:
		status = RunFailed
		runErr = err
	}
	// Terminal transition is best-effort durable evidence; an
	// uncommittable terminal state is reported, not swallowed.
	if err := j.commit(ctx, request.Ref.RunID, 0,
		StateTransition{Expected: RunRunning, Target: status},
		[]Event{{Kind: terminalEvent(status)}}, nil); err != nil {
		return RunResult{Status: RunRecoveryRequired, Diagnostics: toDiagnostics(diags)}, err
	}
	return RunResult{
		Status:      status,
		Outputs:     out,
		Diagnostics: toDiagnostics(diags),
	}, adaptError(runErr)
}

// suspend settles a quiescent run: the staged checkpoint bytes plus
// the outstanding waits and resume addresses are committed as one
// atomic boundary (§8.3 step 4). The commit makes the run waiting; a
// failed commit leaves it running for reconciliation.
func (p *Program) suspend(ctx context.Context, request RunRequest,
	j *runJournal, res einoruntime.InvokeResult) (RunResult, error) {
	cpID := request.Ref.RunID + "/" + itoa(j.gen)
	payload := p.rt.Staging().Staged(cpID)
	waits := j.outstandingWaits()
	// Resume addresses are durable metadata: each wait's request ID
	// binds to the Eino interrupt ID that BatchResumeWithData answers.
	interrupts := map[string]string{}
	gates := []string{}
	for _, pt := range res.Suspend.Points {
		switch pt.Kind {
		case einoruntime.InterruptWait:
			interrupts[pt.Ref] = pt.ID
		default:
			gates = append(gates, pt.ID)
		}
	}
	// A wait that never surfaced as a root-cause interrupt means the
	// suspension did not quiesce; nothing may commit waiting.
	if len(interrupts) != len(waits) {
		return RunResult{Status: RunRecoveryRequired}, &Error{
			Code:    ErrOutcomeUnknown,
			Path:    request.Ref.RunID,
			Message: "suspension did not quiesce: waits lack interrupt addresses",
		}
	}
	data, err := json.Marshal(map[string]any{
		"waits":      waits,
		"interrupts": interrupts,
		"gates":      gates,
	})
	if err != nil {
		return RunResult{Status: RunFailed}, &Error{Code: ErrInvalidDefinition, Err: err}
	}
	env := &CheckpointEnvelope{
		ContinuationGeneration: j.gen,
		RunID:                  request.Ref.RunID,
		ProgramID:              request.Ref.ProgramID,
		HostBindingID:          request.Ref.HostBindingID,
		CompilerVersion:        p.meta.CompilerVersion,
		EinoBuild:              p.meta.EinoBuild,
		CatalogContract:        bytes.Clone(p.catalogContract),
		SerializerVersion:      serializerVersion,
		Usage:                  j.usage(),
		EventBoundary:          j.ord.Load(),
		Payload:                payload,
	}
	j.clock.Pause()
	if err := j.commitEnvelope(ctx,
		StateTransition{Expected: RunRunning, Target: RunWaiting},
		[]Event{{Kind: EventRunWaiting, Data: data}}, env); err != nil {
		// The run is still running in the store: reconciliation
		// territory, never a durable wait (§8.3 step 4).
		return RunResult{Status: RunRecoveryRequired}, err
	}
	return RunResult{
		Status: RunWaiting,
		Waits:  waits,
	}, nil
}

func terminalEvent(s RunStatus) EventKind {
	switch s {
	case RunSucceeded:
		return EventRunSucceeded
	case RunCancelled:
		return EventRunCancelled
	case RunRecoveryRequired:
		return EventRunRecoveryRequired
	default:
		return EventRunFailed
	}
}

// executorAdapter converts the public NodeExecutor contract into the
// runtime's neutral function surface through the per-run journal's
// commit, permit and retry boundary (§7.3).
func executorAdapter(j *runJournal, nodes NodeExecutor) einoruntime.Executor {
	return func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
		return j.executeCall(ctx, nodes, c)
	}
}

// checkRunCeiling rejects run limits wider than the compile ceiling.
func checkRunCeiling(run, ceiling Limits) error {
	checks := []struct {
		name string
		v, c int
	}{
		{"max_nodes", run.MaxNodes, ceiling.MaxNodes},
		{"max_edges", run.MaxEdges, ceiling.MaxEdges},
		{"max_activations", run.MaxActivations, ceiling.MaxActivations},
		{"max_iterations", run.MaxIterations, ceiling.MaxIterations},
		{"max_attempts_per_call", run.MaxAttemptsPerCall, ceiling.MaxAttemptsPerCall},
		{"max_repeat_nesting", run.MaxRepeatNesting, ceiling.MaxRepeatNesting},
	}
	for _, ch := range checks {
		if ch.c > 0 && ch.v > ch.c {
			return &limitError{name: ch.name, v: int64(ch.v), c: int64(ch.c)}
		}
	}
	return nil
}

type limitError struct {
	name string
	v, c int64
}

func (e *limitError) Error() string {
	return "run limit " + e.name + " exceeds the compiled ceiling"
}

const serializerVersion = "inofy-eino-checkpoint-v1"

func itoa(i int) string {
	return fmt.Sprintf("%d", i)
}

// resume is the authorized continuation path (§8.4): the stored
// waiting snapshot must prove every identity before the run's writer
// epoch claims the next generation, all outstanding waits must be
// answered together and validated, and only then does Eino continue
// under the remaining budget from the committed checkpoint.
func (p *Program) resume(ctx context.Context, request RunRequest, bindings Bindings) (RunResult, error) {
	if bindings.Nodes == nil {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrBindingMissing,
			Path:    request.Ref.RunID,
			Message: "Bindings.Nodes executor is required",
		}
	}
	if bindings.Runs == nil {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrUnsupportedFeature,
			Path:    request.Ref.RunID,
			Message: "resume requires a durable RunStore",
		}
	}
	st, err := bindings.Runs.Load(ctx, request.Ref.RunID)
	if err != nil {
		return RunResult{Status: RunFailed}, err
	}
	if st.Ref.ProgramID != request.Ref.ProgramID || st.Ref.HostBindingID != request.Ref.HostBindingID {
		return RunResult{Status: RunFailed}, &Error{Code: ErrCheckpointIncompatible,
			Path: request.Ref.RunID, Message: "resume request does not match the admitted program or host binding identity"}
	}
	// Replaying an already-claimed resume: an identical authorized
	// answer set returns the recorded outcome instead of re-executing.
	if st.Status != RunWaiting {
		return resumeReplay(request, st, bindings.Runs)
	}
	env := st.LatestCheckpoint
	if env == nil {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrCheckpointIncompatible,
			Path:    request.Ref.RunID,
			Message: "waiting run has no committed checkpoint",
		}
	}
	if err := verifyEnvelope(env, st, p.meta, p.catalogContract); err != nil {
		return RunResult{Status: RunFailed}, err
	}
	if len(request.Input) > 0 && !sameJSON(request.Input, st.AdmittedInput) {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrCheckpointIncompatible,
			Path:    request.Ref.RunID,
			Message: "resume input does not match the admitted input value",
		}
	}
	input := st.AdmittedInput
	if len(input) == 0 {
		return RunResult{Status: RunFailed}, &Error{Code: ErrCheckpointIncompatible,
			Path: request.Ref.RunID, Message: "run store has no admitted input value"}
	}
	// Every outstanding wait must be answered together; unknown or
	// schema-violating answers reject the whole resume.
	answers := request.Resume.Answers
	if len(answers) != len(st.Waits) {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrRevisionConflict,
			Path:    request.Ref.RunID,
			Message: "resume must answer every outstanding wait",
		}
	}
	normalizedAnswers, err := normalizeAnswers(answers)
	if err != nil {
		return RunResult{Status: RunFailed}, &Error{Code: ErrSchemaMismatch, Path: request.Ref.RunID,
			Err: err, Message: "resume answers are not canonical JSON values"}
	}
	resumeData := map[string]any{}
	for _, w := range st.Waits {
		raw, ok := answers[w.RequestID]
		if !ok {
			return RunResult{Status: RunFailed}, &Error{
				Code:    ErrRevisionConflict,
				Path:    request.Ref.RunID,
				Message: "missing answer for wait " + w.RequestID,
			}
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return RunResult{Status: RunFailed}, &Error{
				Code:    ErrSchemaMismatch,
				Path:    request.Ref.RunID,
				Message: "answer for " + w.RequestID + " is not JSON",
			}
		}
		if len(w.AnswerSchema) > 0 {
			if err := definition.ValidateValueJSON(w.AnswerSchema, decoded); err != nil {
				return RunResult{Status: RunFailed}, &Error{
					Code:    ErrSchemaMismatch,
					Path:    request.Ref.RunID,
					Message: "answer for " + w.RequestID + " violates its answer schema",
				}
			}
		}
		addr, ok := st.Interrupts[w.RequestID]
		if !ok {
			return RunResult{Status: RunFailed}, &Error{
				Code:    ErrCheckpointIncompatible,
				Path:    request.Ref.RunID,
				Message: "wait " + w.RequestID + " has no committed resume address",
			}
		}
		resumeData[addr] = decoded
	}
	for _, g := range st.Gates {
		resumeData[g] = map[string]any{}
	}
	// Claim the next generation under the writer epoch before any
	// effect runs; a conflicting claim loses the CAS and never
	// executes a node.
	gen := env.ContinuationGeneration + 1
	lim := st.Limits
	if (lim == Limits{}) {
		lim = effectiveLimits(request.Limits)
	}
	// The claim commit carries a fresh nonce: two concurrent resumes
	// with identical answers encode to the same CommitID but different
	// bodies, so the loser is rejected by idempotency_conflict and
	// never runs an effect. A genuine retry of the SAME claim (same
	// caller intent) is matched on resumption via ResumeKey above.
	ref := request.Ref
	ref.Epoch = st.Ref.Epoch + 1
	j := newRunJournal(ref, bindings.Runs, lim)
	j.gen = gen
	j.seedFromUsage(env.Usage)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return RunResult{Status: RunFailed}, err
	}
	claim, _ := json.Marshal(map[string]any{
		"generation":  gen,
		"claim_nonce": hex.EncodeToString(nonce),
	})
	if err := j.commitResumeClaim(ctx, "resume", gen,
		StateTransition{Expected: RunWaiting, Target: RunRunning},
		[]Event{{Kind: EventRunResumed, Data: claim}}, request.Resume.IdempotencyKey, normalizedAnswers); err != nil {
		return RunResult{Status: RunFailed}, err
	}
	// Re-stage the committed checkpoint under the new generation's ID
	// so Eino reloads the exact payload and suspends again under the
	// same key the envelope records (cpID == RunID/j.gen).
	cpID := request.Ref.RunID + "/" + itoa(gen)
	p.rt.Staging().Stage(cpID, env.Payload)
	exec := executorAdapter(j, bindings.Nodes)
	res, err := p.rt.Invoke(ctx, input, exec,
		einoruntime.RunOptions{
			CheckPointID: cpID,
			Gate:         &j.suspending,
			ResumeData:   resumeData,
		})
	if res.Suspend != nil {
		return p.suspend(ctx, request, j, res)
	}
	status := RunSucceeded
	var runErr error
	out := res.Output
	switch {
	case j.recovery.Load() || j.unknown.Load():
		status = RunRecoveryRequired
		runErr = err
	case err != nil && ctx.Err() != nil:
		status = RunCancelled
		runErr = err
	case err != nil:
		status = RunFailed
		runErr = err
	}
	if err := j.commit(ctx, request.Ref.RunID, 0,
		StateTransition{Expected: RunRunning, Target: status},
		[]Event{{Kind: terminalEvent(status)}}, nil); err != nil {
		return RunResult{Status: RunRecoveryRequired}, err
	}
	return RunResult{
		Status:      status,
		Outputs:     out,
		Diagnostics: toDiagnostics(res.Diags),
	}, adaptError(runErr)
}

// resumeReplay handles a resume request against a run already past
// the waiting boundary: an identical authorized answer set returns
// the recorded outcome; anything else is a claim conflict.
func resumeReplay(request RunRequest, st RecoveryState, store RunStore) (RunResult, error) {
	if st.ResumeKey != "" &&
		st.ResumeKey == request.Resume.IdempotencyKey &&
		sameAnswers(st.ResumeAnswers, request.Resume.Answers) {
		return RunResult{Status: st.Status}, nil
	}
	return RunResult{Status: st.Status}, &Error{
		Code:    ErrRevisionConflict,
		Path:    request.Ref.RunID,
		Message: "run is not waiting",
	}
}

// verifyEnvelope proves the committed checkpoint binds the run's
// admitted identities before any resume (§8.4).
func verifyEnvelope(env *CheckpointEnvelope, st RecoveryState, meta ProgramMeta, catalogContract json.RawMessage) error {
	bad := func(msg string) error {
		return &Error{Code: ErrCheckpointIncompatible, Path: st.Ref.RunID, Message: msg}
	}
	switch {
	case env.RunID != st.Ref.RunID:
		return bad("checkpoint run identity mismatch")
	case env.ProgramID != meta.ProgramID || env.ProgramID != st.Ref.ProgramID:
		return bad("checkpoint program identity mismatch")
	case env.HostBindingID != st.Ref.HostBindingID:
		return bad("checkpoint host binding mismatch")
	case env.CompilerVersion != meta.CompilerVersion:
		return bad("checkpoint compiler version mismatch")
	case env.EinoBuild != meta.EinoBuild:
		return bad("checkpoint eino build mismatch")
	case !sameJSON(env.CatalogContract, catalogContract):
		return bad("checkpoint catalog contract mismatch")
	case env.SerializerVersion != serializerVersion:
		return bad("checkpoint serializer version mismatch")
	}
	return nil
}

func normalizeAnswers(answers map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(answers))
	for key, raw := range answers {
		norm, err := NormalizeJSONValue(raw)
		if err != nil {
			return nil, err
		}
		out[key] = norm
	}
	return out, nil
}

func sameJSON(a, b json.RawMessage) bool {
	if len(a) == 0 || len(b) == 0 {
		return len(a) == len(b)
	}
	na, errA := NormalizeJSONValue(a)
	nb, errB := NormalizeJSONValue(b)
	return errA == nil && errB == nil && bytes.Equal(na, nb)
}

func sameAnswers(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		other, ok := b[key]
		if !ok || !sameJSON(value, other) {
			return false
		}
	}
	return true
}

func isNotFound(err error) bool {
	var ie *Error
	return errors.As(err, &ie) && ie.Code == ErrRevisionConflict && ie.Message == "unknown run"
}
