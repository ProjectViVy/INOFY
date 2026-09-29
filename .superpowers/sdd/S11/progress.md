# S11 — ViVy adapter and host surface (readiness proof only)

## Verdict
Readiness gate **verified** on baseline `83d6610545a8831937b2d351176353008881446b` (read-only). Executable integration is blocked by scope: every planned file lives under `agent-vivy/internal/…`, and Go `internal/` packages cannot be imported from INOFY — the adapter cannot compile outside the ViVy module. Per user constraint (no ViVy edits) this session delivers the contract proof + gap list; Tasks 1–3 stay planned, not started.

## Contract map (verified, baseline 83d66105)
- `orchestration.ValidatedDescriptor{Descriptor{SchemaVersion,StartNodes,Nodes[{Key,Task,ToolNames}],Edges[{From,To,InputKey}],Outputs}, CanonicalJSON, Digest, Topological, Layers}` — bounds MaxNodes 12 / MaxEdges 24 / MaxOutputBytes 8KiB already enforced host-side.
- `WorkflowNodeRunner func(context.Context, WorkflowNodeRequest) (string,error)` — bounded string outputs at the native boundary; maps onto `inofy.NodeExecutor.Execute` (one governed node op per call).
- `Service.executeWorkflowGraph` already compiles `compose.Workflow` + `NewEinoCheckpointAdapter(VersionedCheckpointStore)` — INOFY swap point exists and keeps `runTools[runID]` authority narrowing.
- `VersionedCheckpointStore` = Vivy envelope (engine version + SHA-256 + prompt identity) over `storage.BlobStore` generation-based Put/Get — semantically matches S06 `CheckpointEnvelope`.
- `storage.Journal.Append(commit)` = single tx: exactly-one-terminal check + contiguous seq insert (sqlite + postgres parity). `storage.Commit{RunID, Events}` only — no CommitID/op-key/epoch fields.
- `storage.SnapshotStore.Put(key,value,expectVersion)` = CAS — available for CommitID dedup/epoch fencing without schema work.

## Atomic manifest feasibility — PROVEN
S11's required order maps cleanly: `BlobStore.Put` (immutable generation, fail-closed verified read) **then** `Journal.Append` manifest event referencing blob id+generation+checksum **in one append tx**. If Append fails the blob is an unreferenced orphan (GC-able, never resumable); the manifest can never point at a missing blob because blob durability precedes manifest visibility. No new primitive needed for the blob↔journal two-phase rule.

## Gaps ViVy must close (in vivy, when permitted)
1. `Journal.Commit` has no idempotency/epoch fields — S05 `RunStore.Commit` needs CommitID dedup + writer-epoch fencing. Options: a manifest RunEvent carrying commit_id/epoch plus a unique index, or SnapshotStore CAS on `inofy/commit/<commit_id>` inside the same tx — the second needs the journal tx widened; flag for architecture if neither lands cleanly.
2. INOFY `RunStore.Load/Commit` surface ↔ vivy `Journal.Replay`/`BlobStore` — needs the in-vivy adapter (planned `internal/runtime/inofy_store.go`).
3. Descriptor→Definition translation (`inofy_adapter.go`) + `StudioTransport` host bridge (S10 seam ready).
4. `executeWorkflowGraph` routing swap must stay behind the existing admission/capability path — per plan, flip only after Task 2 fault tests pass on BOTH drivers.

## minor (deferred)
- vivy checkpoint envelope lacks `ContinuationGeneration`; vivy currently checkpoints by `checkpointIDFor(runID)` — INOFY resume generation must live in the manifest event, not the envelope.
