# INOFY — Post-cutover enhancement proposal

Date: 2026-09-30

Status: Proposal for owner review. Not approved; no implementation authorized.

Delivery target: not set — candidates for a v0.2 line after S11 cutover stabilizes.

Evidence level: live integration evidence. Every item cites an observed path in the shipped ViVy integration (`agent-vivy` branch `docs/issue23-inofy-cutover-plan`, pin `v0.0.0-20260929225707-4def2ae6185f`) or an INOFY-side behavior verified by unit test or browser smoke.

## 1. Context

S11 replaced ViVy's task-graph path with the embedded INOFY engine as the sole executable graph engine. Each graph node executes as a governed ViVy child Run; ViVy stays authoritative for Run/session/policy/budget/Journal/storage/UI auth. The cutover validated the library-first contract end to end — draft → validate → publish → run → outputs → cancel — in a real browser smoke, and surfaced a set of enhancement candidates on both sides of the seam.

This document records candidates scoped to **INOFY itself** (engine, schema, host contract, editor model layer). ViVy-side items (e.g. which Run `inofy.startRun` binds as parent) are ViVy product decisions and are out of scope here except where they require an INOFY contract change.

## 2. Candidate enhancements

### E1 — Recovery UX surface is engine-only (real gap)

**Evidence.** `inofy.resumeRun` exists in the RPC surface, the typed client, and the engine (`ResumeKey`, resume-answers digest, `EventRunResumed`); `TestINOFYWorkflowResumesAdmittedAfterCrash` proves crash-between-admission-and-launch recovery. But `engine_status:"recovery_required"` is only observable — the ViVy module renders it as a badge with no resume affordance, and nothing in the product contract documents who is allowed to resume a stuck run.

**Proposal.** Keep engine semantics unchanged; define the resume contract's *consumers*:

- Decide whether `ResumeRun` is a host-privileged operation only (parent-run / operator) or also end-user reachable. Encode that in `inofy.capabilities` (`wait_resume` flag exists; a `resume` capability bit would let UIs render honestly instead of guessing).
- Specify `answers` semantics for node-level waits vs whole-run resume in the App §11 contract — today the parameter shape is accepted but underdocumented.

### E2 — No sub-workflow composition (structural limit, needs a decision)

**Evidence.** `validateWorkflowDepth` rejects a workflow Run as `parent_run_id` — workflows cannot parent workflows. S11-F shipped reusable definitions (draft CAS + immutable published revisions), but no node type can *invoke* a published definition; `vivy.child-task@1` is the only admitted type in ViVy and the trusted catalog has no graph-referencing node. Reusability therefore stops at "edit a copy of a published revision," never composition.

**Proposal.** Evaluate a `workflow.call` node type in a future schema revision:

- Node config references `{workflow, revision}` or a digest; the engine admits it only under the host's depth ceiling (ViVy already enforces depth; make the ceiling a declared limit instead of a hard reject at admission).
- Semantics to pin down before any code: budget ceiling inheritance, cancel propagation into the nested run, output projection (`{source: node, pointer}` over the nested run's declared outputs), idempotency/operation-key naming for retry, and whether a nested run is a governed child of the workflow run (consistent with current node=child-run model).
- Failure semantics: nested `recovery_required` must map onto the parent's node state, not leak a second engine surface.

This is the largest candidate; it deserves its own spec before implementation.

### E3 — Descriptor-driven node configuration (editor model layer)

**Evidence.** ViVy's redesigned editor hardcodes `vivy.child-task@1` config fields (task / tool_names / inputs / timeout_ms). The vendored model layer (`schema.ts`/`edit.ts`) already carries the node-type descriptor vocabulary (`inofy.nodeTypes` returns per-type config schema), but nothing generates a form from it. Today there is exactly one trusted type so nothing is missing in practice; the moment the catalog gains a second type every consumer reimplements config UI.

**Proposal.** In the editor model layer, add a small descriptor→fields projection (field kind → text/json/select/checkbox widget spec), so consumers render config panels from `nodeTypes` output instead of per-type code. Kept in the shared model layer, not in any host's chrome.

### E4 — Static topology pre-checks in the model layer

**Evidence.** Backend validation reports `[topology] /graph/nodes/<id>: node cannot reach a declared exit` — but only after a validate RPC round-trip. The shared `graph.ts`/`edit.ts` already maintain the full semantic graph in memory.

**Proposal.** Add a pure `lintArtifact(artifact)` to the shared model layer producing the same diagnostic codes the decoder emits (unreachable-exit, dangling-edge, unresolved-type). Hosts can then mark nodes live on the canvas and skip obviously-invalid validate calls. Must reuse, not duplicate, the decoder's rule set — a small exported rule list from the Go side (`ValidateDefinition` already centralizes it; mirror only the cheap topology half) to avoid a second source of truth.

### E5 — Node-level observability projection

**Evidence.** Run detail UIs today show node status transitions from journal events. The engine emits attempt lifecycle events, but per-node wall-time, attempt count, and result size are not projected in `workflow/get` node rows — hosts recompute nothing.

**Proposal.** Extend the node-state projection (`inofy_run_state` row per node) with `attempt_count`, `started_at`/`ended_at`, and `result_bytes`. Cheap: fields already exist in the journal event payloads; this is a projection change, not new machinery.

### E6 — `next_run` / requeue affordance for failed node runs

**Evidence.** Cancelling mid-node produces `node_failed` + `run_recovery_required` and the run is correct-but-stuck. Recovery machinery exists (E1) but nothing lets a caller say "retry just this node" — resume semantics today are whole-run.

**Proposal.** Evaluate a node-scoped retry in the engine: bounded by `limits` (retry budget per node), driven through `resumeRun` answers or a dedicated operation. Only worth doing if E1's contract decision makes resume a supported consumer path.

## 3. Explicitly out of scope

- Human-in-loop nodes inside a graph (`ask_user` in a node): ViVy policy mediates all human edges through the parent agent; children must not open user interaction directly. If a future product wants wait-nodes, they resume through parent-mediated `resumeRun`, not a node UI.
- Old-DAG / Eino `compose.NewWorkflow` compatibility shims: intentionally removed; no bridge back.
- INOFY App DB, standalone service, scheduler, cron/webhook triggers: unchanged non-goals from the architecture spec §1.2.
- Editor chrome: ViVy rejected the vendored studio UI; the model layer stays shared, chrome stays per-host.

## 4. Recommended order

| # | Item | Size | Depends on |
|---|---|---|---|
| 1 | E1 resume contract clarification + capability bit | Small | Owner decision on who may resume |
| 2 | E4 lintArtifact topology pre-checks | Small | None |
| 3 | E5 node observability projection | Small | None |
| 4 | E3 descriptor-driven config | Medium | None (needed when catalog grows) |
| 5 | E2 sub-workflow composition | Large | Own spec; depth-ceiling product call |
| 6 | E6 node-scoped retry | Medium | E1 contract |

## 5. Open questions for the owner

1. Should `resumeRun` ever be end-user reachable in a host UI, or host-privileged only?
2. Is sub-workflow composition (E2) wanted at all, or is depth-1 permanently sufficient for both consumers?
3. The ViVy cutover chain (`6acfcc6→98526b8→586f4b5→4def2ae`) is not on `main` — merge strategy before any regeneration?
