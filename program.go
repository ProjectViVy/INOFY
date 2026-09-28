package inofy

import (
	"context"
	"encoding/json"

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
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrUnsupportedFeature,
			Path:    request.Ref.RunID,
			Message: "resume is a S06 contract",
		}
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
	if err := j.commit(ctx, request.Ref.RunID, 0,
		StateTransition{Expected: "", Target: RunAdmitted},
		[]Event{{Kind: EventRunAdmitted}}, nil); err != nil {
		return RunResult{Status: RunFailed}, err
	}
	if err := j.commit(ctx, request.Ref.RunID, 0,
		StateTransition{Expected: RunAdmitted, Target: RunRunning},
		[]Event{{Kind: EventRunStarted}}, nil); err != nil {
		return RunResult{Status: RunFailed}, err
	}
	exec := executorAdapter(j, bindings.Nodes)
	out, diags, err := p.rt.Invoke(ctx, request.Input, exec)
	status := RunSucceeded
	var runErr error
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
