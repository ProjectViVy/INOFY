package inofy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ProjectViVy/inofy/internal/definition"
	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

// Program is an immutable compiled artifact safe for concurrent Run
// invocations with different run IDs. It holds no run state, live
// credentials, host globals or per-run closures.
type Program struct {
	meta          ProgramMeta
	rt            *einoruntime.Program
	compileLimits Limits
}

// ProgramMeta binds the program to its inputs: definition and catalog
// digests, the INOFY compiler/format version and the exact Eino build
// identity (architecture §4.3).
type ProgramMeta struct {
	DefinitionDigest string `json:"definition_digest"`
	CatalogDigest    string `json:"catalog_digest"`
	ProgramDigest    string `json:"program_digest"`
	CompilerVersion  string `json:"compiler_version"`
	EinoBuild        string `json:"eino_build"`
}

// Meta reports the frozen program identity.
func (p *Program) Meta() ProgramMeta { return p.meta }

// Run executes one admitted run. Identity and limits are rechecked
// against the compiled program before any work; a mismatch or a limit
// wider than the compile ceiling is rejected. Resume is a S06 contract
// and reports unsupported_feature until then.
func (p *Program) Run(ctx context.Context, request RunRequest, bindings Bindings) (RunResult, error) {
	if p == nil || p.rt == nil || request.Ref.ProgramDigest != p.meta.ProgramDigest {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrCheckpointIncompatible,
			Path:    request.Ref.RunID,
			Message: "run request does not match the compiled program identity",
		}
	}
	if request.Resume != nil {
		return p.resume(ctx, request, bindings)
	}
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
	// Admission and start are visible transitions, never silent
	// progress (§8.1): a failed admission commit cannot start work.
	// The admitted input digest and effective limits ride in the
	// admission event so resume can re-verify both (§8.4).
	admitData, _ := json.Marshal(map[string]any{
		"input_digest": inputDigest(request.Input),
		"limits":       lim,
	})
	if err := j.commit(ctx, request.Ref.RunID, 0,
		StateTransition{Expected: "", Target: RunAdmitted},
		[]Event{{Kind: EventRunAdmitted, Data: admitData}}, nil); err != nil {
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
		DefinitionDigest:       p.meta.DefinitionDigest,
		ProgramDigest:          p.meta.ProgramDigest,
		EinoBuild:              p.meta.EinoBuild,
		SerializerVersion:      serializerVersion,
		InputDigest:            inputDigest(request.Input),
		Usage:                  j.usage(),
		EventBoundary:          j.ord.Load(),
		Payload:                payload,
		Checksum:               payloadChecksum(payload),
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

// inputDigest is the canonical digest of the admitted run input.
func inputDigest(input json.RawMessage) string {
	var doc any
	if err := json.Unmarshal(input, &doc); err != nil {
		return ""
	}
	norm, err := definition.Canonical(doc)
	if err != nil {
		return ""
	}
	return definition.DigestBytes(norm)
}

func payloadChecksum(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// resume is the authorized continuation path (§8.4): Task 2 wires
// identity checks, claim CAS and addressed answers.
func (p *Program) resume(ctx context.Context, request RunRequest, bindings Bindings) (RunResult, error) {
	return RunResult{Status: RunFailed}, &Error{
		Code:    ErrUnsupportedFeature,
		Path:    request.Ref.RunID,
		Message: "resume claim path lands in S06 task 2",
	}
}
