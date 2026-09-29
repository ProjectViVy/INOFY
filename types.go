// Package inofy holds the public, data-only contracts of the INOFY
// workflow engine: the executable Definition schema, the frozen node
// Catalog, and the Compile/Program invocation boundary. No Eino type,
// SQL driver, HTTP client or listener crosses this package; all Eino
// imports live under internal/einoruntime.
//
// Canonical versions: schema "inofy.workflow/v1", normalizer
// "inofy-normal-v1" (architecture §4).
package inofy

import (
	"context"
	"encoding/json"
	"fmt"
)

const (
	// SchemaVersionV1 is the only executable document version of v0.1.
	SchemaVersionV1 = "inofy.workflow/v1"
	// NormalizerV1 names the digest input encoding (architecture §4.3).
	NormalizerV1 = "inofy-normal-v1"
)

// Error is the stable run/compile error contract (architecture §8.6):
// code, category, authored node path when applicable, safe message and
// wrapped internal cause.
type Error struct {
	Code     ErrorCode
	Category ErrorCategory
	Path     string
	Message  string
	Err      error
}

func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s at %s: %s", e.Code, e.Path, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

type ErrorCode string

const (
	ErrInvalidDefinition      ErrorCode = "invalid_definition"
	ErrUnknownNodeType        ErrorCode = "unknown_node_type"
	ErrUnsupportedFeature     ErrorCode = "unsupported_feature"
	ErrBindingMissing         ErrorCode = "binding_missing"
	ErrSchemaMismatch         ErrorCode = "schema_mismatch"
	ErrAuthorityDenied        ErrorCode = "authority_denied"
	ErrBudgetExceeded         ErrorCode = "budget_exceeded"
	ErrDeadlineExceeded       ErrorCode = "deadline_exceeded"
	ErrIterationLimit         ErrorCode = "iteration_limit"
	ErrNodeFailed             ErrorCode = "node_failed"
	ErrStorageFailed          ErrorCode = "storage_failed"
	ErrOutcomeUnknown         ErrorCode = "outcome_unknown"
	ErrCheckpointIncompatible ErrorCode = "checkpoint_incompatible"
	ErrRevisionConflict       ErrorCode = "revision_conflict"
	ErrIdempotencyConflict    ErrorCode = "idempotency_conflict"
	ErrStaleWriter            ErrorCode = "stale_writer"
)

// ErrorCategory is the coarse grouping above ErrorCode. The taxonomy is
// owned by the lifecycle stories (S05/S06); values are declared there.
type ErrorCategory string

// CheckKind classifies a Diagnostic (architecture §4.1). Only hosts can
// complete the authority check.
type CheckKind string

const (
	CheckSchema     CheckKind = "schema"
	CheckTopology   CheckKind = "topology"
	CheckCapability CheckKind = "capability"
	CheckBudget     CheckKind = "budget"
	CheckAuthority  CheckKind = "authority"
)

// Diagnostic is one non-fatal finding returned by Compile and carried in
// RunResult warnings.
type Diagnostic struct {
	Check   CheckKind `json:"check"`
	Path    string    `json:"path"`
	Code    string    `json:"code"`
	Message string    `json:"message"`
}

// --- Definition document (architecture §4.1) ---

// Definition is the sole executable schema. Presentation lives in the
// artifact envelope and is owned by S02's strict decoder; the public
// shape here follows the normative executable field vocabulary.
type Definition struct {
	SchemaVersion string          `json:"schema_version"`
	InputsSchema  json.RawMessage `json:"inputs_schema,omitempty"`
	OutputsSchema json.RawMessage `json:"outputs_schema,omitempty"`
	Graph         Graph           `json:"graph"`
	Limits        Limits          `json:"limits,omitempty"`
}

type Graph struct {
	Nodes   []Node             `json:"nodes"`
	Edges   []Edge             `json:"edges"`
	Exits   []string           `json:"exits"`
	Outputs map[string]Binding `json:"outputs,omitempty"`
}

// Edge is an order-only dependency unless `from` is a switch, in which
// case Port selects the branch.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Port string `json:"port,omitempty"`
}

type NodeKind string

const (
	NodeKindCall   NodeKind = "call"
	NodeKindSwitch NodeKind = "switch"
	NodeKindSelect NodeKind = "select"
	NodeKindRepeat NodeKind = "repeat"
)

// Node is the tagged union over the four node kinds. The flat field set
// mirrors the normative vocabulary; S02's strict decoder rejects fields
// belonging to another kind.
type Node struct {
	ID   string   `json:"id"`
	Kind NodeKind `json:"kind"`

	// call
	Type      string             `json:"type,omitempty"`
	Config    json.RawMessage    `json:"config,omitempty"`
	Inputs    map[string]Binding `json:"inputs,omitempty"`
	TimeoutMS int                `json:"timeout_ms,omitempty"`
	Retry     *Retry             `json:"retry,omitempty"`
	OnError   *ErrorPolicy       `json:"on_error,omitempty"`

	// switch
	Cases       []SwitchCase `json:"cases,omitempty"`
	DefaultPort string       `json:"default_port,omitempty"`
	Join        string       `json:"join,omitempty"`

	// select
	Switch     string            `json:"switch,omitempty"`
	Candidates []SelectCandidate `json:"candidates,omitempty"`
	Fallback   *Binding          `json:"fallback,omitempty"`

	// repeat
	Initial       map[string]Binding `json:"initial,omitempty"`
	StateSchema   json.RawMessage    `json:"state_schema,omitempty"`
	Body          *Graph             `json:"body,omitempty"`
	MaxIterations int                `json:"max_iterations,omitempty"`
	Until         *Predicate         `json:"until,omitempty"`
}

type SwitchCase struct {
	Port string    `json:"port"`
	When Predicate `json:"when"`
}

type SelectCandidate struct {
	Source  string `json:"source"`
	Pointer string `json:"pointer"`
}

type Retry struct {
	MaxAttempts int `json:"max_attempts"`
	DelayMS     int `json:"delay_ms"`
}

type ErrorMode string

const (
	ErrorModeFail     ErrorMode = "fail"
	ErrorModeFallback ErrorMode = "fallback"
)

type ErrorPolicy struct {
	Mode  ErrorMode       `json:"mode"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Binding is exactly one of a JSON literal or a source reference naming
// "input" or a node ID plus a JSON Pointer.
type Binding struct {
	Literal json.RawMessage `json:"literal,omitempty"`
	Source  string          `json:"source,omitempty"`
	Pointer string          `json:"pointer,omitempty"`
}

type PredicateOp string

const (
	OpEq     PredicateOp = "eq"
	OpNe     PredicateOp = "ne"
	OpLt     PredicateOp = "lt"
	OpLte    PredicateOp = "lte"
	OpGt     PredicateOp = "gt"
	OpGte    PredicateOp = "gte"
	OpExists PredicateOp = "exists"
	OpAll    PredicateOp = "all"
	OpAny    PredicateOp = "any"
	OpNot    PredicateOp = "not"
)

// Predicate is a tagged boolean expression: comparisons use Left/Right
// bindings, Exists uses Pointer, All/Any use Args, Not uses Arg.
type Predicate struct {
	Op      PredicateOp `json:"op"`
	Left    *Binding    `json:"left,omitempty"`
	Right   *Binding    `json:"right,omitempty"`
	Pointer string      `json:"pointer,omitempty"`
	Args    []Predicate `json:"args,omitempty"`
	Arg     *Predicate  `json:"arg,omitempty"`
}

// Limits bound one run (architecture §7.4). Effective limits are the
// minimum of definition requests and host ceilings; zero inherits.
type Limits struct {
	MaxNodes             int   `json:"max_nodes,omitempty"`
	MaxEdges             int   `json:"max_edges,omitempty"`
	Parallelism          int   `json:"parallelism,omitempty"`
	MaxRepeatNesting     int   `json:"max_repeat_nesting,omitempty"`
	MaxIterations        int   `json:"max_iterations,omitempty"`
	MaxActivations       int   `json:"max_activations,omitempty"`
	MaxAttemptsPerCall   int   `json:"max_attempts_per_call,omitempty"`
	NodeTimeoutMS        int64 `json:"node_timeout_ms,omitempty"`
	RunTimeoutMS         int64 `json:"run_timeout_ms,omitempty"`
	MaxDefinitionBytes   int64 `json:"max_definition_bytes,omitempty"`
	MaxNodeInputBytes    int64 `json:"max_node_input_bytes,omitempty"`
	MaxNodeOutputBytes   int64 `json:"max_node_output_bytes,omitempty"`
	MaxOutputBytesTotal  int64 `json:"max_output_bytes_total,omitempty"`
	MaxCheckpointBytes   int64 `json:"max_checkpoint_bytes,omitempty"`
	MaxPredicateDepth    int   `json:"max_predicate_depth,omitempty"`
	MaxConcurrentRuns    int   `json:"max_concurrent_runs,omitempty"`
	MaxPendingAdmissions int   `json:"max_pending_admissions,omitempty"`
}

// --- Catalog (architecture §5.2) ---

type ReplayClass string

const (
	ReplayPure          ReplayClass = "pure"
	ReplayIdempotent    ReplayClass = "idempotent"
	ReplayNonReplayable ReplayClass = "non_replayable"
)

// DisplayMeta is non-executable node presentation data.
type DisplayMeta struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// NodeDescriptor is a host-supplied, trusted registration of one node
// type such as "inofy.value@1". Schemas are JSON Schema 2020-12.
type NodeDescriptor struct {
	TypeID           string          `json:"type_id"`
	ImplementationID string          `json:"implementation_id"`
	ConfigSchema     json.RawMessage `json:"config_schema,omitempty"`
	InputSchema      json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema     json.RawMessage `json:"output_schema,omitempty"`
	Display          DisplayMeta     `json:"display,omitempty"`
	Capabilities     []string        `json:"capabilities,omitempty"`
	Replay           ReplayClass     `json:"replay,omitempty"`
	SupportsWait     bool            `json:"supports_wait,omitempty"`
}

// --- Invocation boundary (architecture §6) ---

// CompileOptions carries host-supported features and the maximum
// admissible graph limits. It grants no authority.
type CompileOptions struct {
	Features map[string]bool `json:"features,omitempty"`
	Limits   Limits          `json:"limits,omitempty"`
}

// ExecutionRef is the host-assigned run identity. Epoch is the exclusive
// writer fence; ProgramDigest must match the compiled program.
type ExecutionRef struct {
	RunID         string `json:"run_id"`
	Epoch         uint64 `json:"epoch"`
	ProgramDigest string `json:"program_digest"`
	HostBindingID string `json:"host_binding_id,omitempty"`
}

// RunRequest admits one execution. Resume is set only for an authorized
// resume of a durably waiting run.
type RunRequest struct {
	Ref    ExecutionRef    `json:"ref"`
	Input  json.RawMessage `json:"input"`
	Limits Limits          `json:"limits"`
	Resume *ResumeRequest  `json:"resume,omitempty"`
}

// ResumeRequest supplies answers keyed by outstanding wait RequestID.
type ResumeRequest struct {
	IdempotencyKey string                     `json:"idempotency_key,omitempty"`
	Answers        map[string]json.RawMessage `json:"answers"`
}

// NodeCall is one trusted executor invocation.
type NodeCall struct {
	Ref              ExecutionRef    `json:"ref"`
	Path             string          `json:"path"` // logical path incl. container/iteration
	TypeID           string          `json:"type_id"`
	ImplementationID string          `json:"implementation_id"`
	Config           json.RawMessage `json:"config"`
	Input            json.RawMessage `json:"input"`
	OperationKey     string          `json:"operation_key"`
	Attempt          int             `json:"attempt"`
	Continuation     json.RawMessage `json:"continuation,omitempty"`
}

// NodeReply is mutually exclusive: exactly one of Output or Wait.
type NodeReply struct {
	Output json.RawMessage `json:"output,omitempty"`
	Wait   *WaitRequest    `json:"wait,omitempty"`
}

// Validate enforces the mutual exclusion on the wire boundary.
func (r NodeReply) Validate() error {
	hasOut := len(r.Output) > 0
	hasWait := r.Wait != nil
	switch {
	case hasOut && hasWait:
		return &Error{Code: ErrInvalidDefinition, Message: "node reply carries both output and wait"}
	case !hasOut && !hasWait:
		return &Error{Code: ErrInvalidDefinition, Message: "node reply carries neither output nor wait"}
	}
	return nil
}

// WaitRequest is a durable, host-owned suspension point. Prompt is
// already redacted; ContinuationRef is opaque to INOFY.
type WaitRequest struct {
	RequestID       string          `json:"request_id"`
	Kind            string          `json:"kind"`
	Prompt          string          `json:"prompt,omitempty"`
	AnswerSchema    json.RawMessage `json:"answer_schema,omitempty"`
	ContinuationRef string          `json:"continuation_ref"`
}

type RunStatus string

const (
	RunAdmitted         RunStatus = "admitted"
	RunRunning          RunStatus = "running"
	RunWaiting          RunStatus = "waiting"
	RunSucceeded        RunStatus = "succeeded"
	RunFailed           RunStatus = "failed"
	RunCancelled        RunStatus = "cancelled"
	RunRecoveryRequired RunStatus = "recovery_required"
)

// NodeState is the committed per-node projection state (architecture §8.1).
type NodeState string

const (
	NodeNotStarted NodeState = "not_started"
	NodeRunning    NodeState = "running"
	NodeWaiting    NodeState = "waiting"
	NodeCompleted  NodeState = "completed"
	NodeDegraded   NodeState = "degraded"
	NodeFailed     NodeState = "failed"
	NodeCancelled  NodeState = "cancelled"
	NodeSkipped    NodeState = "skipped"
	NodeUnknown    NodeState = "unknown"
)

// EventKind enumerates the semantic events (architecture §8.2). Engine
// control nodes never emit authored-path events.
type EventKind string

const (
	EventRunAdmitted         EventKind = "run_admitted"
	EventRunStarted          EventKind = "run_started"
	EventRunWaiting          EventKind = "run_waiting"
	EventRunResumed          EventKind = "run_resumed"
	EventRunSucceeded        EventKind = "run_succeeded"
	EventRunFailed           EventKind = "run_failed"
	EventRunCancelled        EventKind = "run_cancelled"
	EventRunRecoveryRequired EventKind = "run_recovery_required"
	EventNodeAttempt         EventKind = "node_attempt"
	EventNodeStarted         EventKind = "node_started"
	EventNodeCompleted       EventKind = "node_completed"
	EventNodeDegraded        EventKind = "node_degraded"
	EventNodeWait            EventKind = "node_wait"
	EventNodeFailed          EventKind = "node_failed"
	EventSwitchDecision      EventKind = "switch_decision"
	EventRepeatIteration     EventKind = "repeat_iteration"
)

// Event payload identity excludes storage-assigned sequence/timestamp.
type Event struct {
	Kind    EventKind       `json:"kind"`
	Path    string          `json:"path,omitempty"`
	Attempt int             `json:"attempt,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ProtectedResult is a full node output stored apart from the redacted
// event projection.
type ProtectedResult struct {
	Path    string          `json:"path"`
	Attempt int             `json:"attempt,omitempty"`
	Output  json.RawMessage `json:"output"`
}

// Usage is the durable budget/accounting snapshot carried by commits,
// checkpoints and recovery state.
type Usage struct {
	Activations          int   `json:"activations"`
	Attempts             int   `json:"attempts"`
	CompletedOutputBytes int64 `json:"completed_output_bytes"`
	ActiveMS             int64 `json:"active_ms"`
}

// CheckpointEnvelope binds the opaque Eino payload to every identity
// needed to gate a resume (architecture §8.3).
type CheckpointEnvelope struct {
	ContinuationGeneration int    `json:"continuation_generation"`
	DefinitionDigest       string `json:"definition_digest"`
	ProgramDigest          string `json:"program_digest"`
	EinoBuild              string `json:"eino_build"`
	SerializerVersion      string `json:"serializer_version"`
	InputDigest            string `json:"input_digest"`
	Usage                  Usage  `json:"usage"`
	EventBoundary          uint64 `json:"event_boundary"`
	Payload                []byte `json:"payload"`
	Checksum               string `json:"checksum"`
}

// StateTransition is the CAS expectation in a commit: the commit is only
// valid if the run is currently in Expected.
type StateTransition struct {
	Expected RunStatus `json:"expected"`
	Target   RunStatus `json:"target"`
}

// RunCommit is the one atomic visibility boundary for a run
// (architecture §6). Repeating a CommitID with the same body returns the
// original receipt; a changed body is an idempotency_conflict.
type RunCommit struct {
	CommitID   string              `json:"commit_id"`
	Events     []Event             `json:"events,omitempty"`
	Results    []ProtectedResult   `json:"results,omitempty"`
	Checkpoint *CheckpointEnvelope `json:"checkpoint,omitempty"`
	Transition StateTransition     `json:"transition"`
}

// Receipt is the host-assigned durable acknowledgement of a commit.
type Receipt struct {
	FirstSequence      uint64 `json:"first_sequence"`
	LastSequence       uint64 `json:"last_sequence"`
	ProjectionRevision uint64 `json:"projection_revision"`
}

// OperationRef names a started-but-unresolved effect for reconciliation.
type OperationRef struct {
	OperationKey string `json:"operation_key"`
	Path         string `json:"path"`
	Attempt      int    `json:"attempt"`
}

// RecoveryState is what RunStore.Load returns: admitted identities, input
// digest and limits, current status, durable usage, the latest committed
// checkpoint, the outstanding wait projection and unresolved operations.
// Interrupts maps each outstanding wait RequestID to the Eino resume
// identity committed at the waiting boundary; Gates lists the internal
// gate interruptions resumed alongside authorized answers.
type RecoveryState struct {
	Ref                  ExecutionRef        `json:"ref"`
	Status               RunStatus           `json:"status"`
	InputDigest          string              `json:"input_digest"`
	Limits               Limits              `json:"limits"`
	Usage                Usage               `json:"usage"`
	LatestCheckpoint     *CheckpointEnvelope `json:"latest_checkpoint,omitempty"`
	Waits                []WaitRequest       `json:"waits,omitempty"`
	Interrupts           map[string]string   `json:"interrupts,omitempty"`
	Gates                []string            `json:"gates,omitempty"`
	// ResumeKey/ResumeAnswersDigest record the last committed resume
	// claim so an identical authorized repeat returns the recorded
	// outcome instead of re-executing (§8.4).
	ResumeKey            string              `json:"resume_key,omitempty"`
	ResumeAnswersDigest  string              `json:"resume_answers_digest,omitempty"`
	UnresolvedOperations []OperationRef      `json:"unresolved_operations,omitempty"`
}

// RunResult reports one settled or suspended run.
type RunResult struct {
	Status      RunStatus       `json:"status"`
	Outputs     json.RawMessage `json:"outputs,omitempty"`
	Waits       []WaitRequest   `json:"waits,omitempty"`
	Warnings    []Diagnostic    `json:"warnings,omitempty"`
	Diagnostics []Diagnostic    `json:"diagnostics,omitempty"`
}

// NodeExecutor is the host's trusted effect boundary. The host rechecks
// authority, reserves budgets and consults its effect ledger inside
// Execute; INOFY never calls it before committing the attempt record.
type NodeExecutor interface {
	Execute(ctx context.Context, call NodeCall) (NodeReply, error)
}

// RunStore is the host-controlled transactional adapter to authoritative
// execution evidence. Commit is atomic; Load returns recovery state.
type RunStore interface {
	Commit(ctx context.Context, ref ExecutionRef, change RunCommit) (Receipt, error)
	Load(ctx context.Context, runID string) (RecoveryState, error)
}

// Bindings wires the two host boundaries into Program.Run.
type Bindings struct {
	Nodes NodeExecutor
	Runs  RunStore
}
