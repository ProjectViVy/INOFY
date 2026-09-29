# S06 — Wait, Resume and Crash Classification

Status: Done. Branch `devin/s06-wait-resume` (stacked on `devin/s05-bounded-execution`).

## Evidence
- `TestSuspensionBarrier` — two waits quiesce together, gated sibling never starts a new effect after the first wait commit, in-flight sibling result commits before waiting is visible; failed checkpoint commit leaves run running with no waiting projection.
- `TestResumeAndCrashMatrix` — authorized all-answers resume → success; partial set rejected (run stays waiting); cross-program digest denied; dual resume executes one continuation (claim nonce → idempotency_conflict on the same CommitID); running-on-reopen → recovery_required with no effect replay; identical authorized repeat is idempotent.
- `go test ./... -count=3` green; `go test -race` green; `go vet` clean; no new deps.

## Rulings
- Quiescence barrier: `j.suspending` closes the effect gate the moment a node_wait event commits; unstarted calls get `inofy_gate` interrupts keyed by node path, answered with a dummy value on resume.
- Interrupt identity: `InterruptCtx.ID` is minted per suspend — run_waiting event data binds requestID→address and carries the gate interrupt IDs; resume keys Eino `ResumeData` on those committed addresses.
- CheckpointID = `RunID/gen`; gen increments per continuation; `StagingStore.Stage` re-seeds committed payload bytes under the same ID for process reopen.
- Outstanding waits ride in the run_waiting event payload (waits+interrupts+gates), surfaced via `RecoveryState` — the checkpoint envelope stays opaque.
- Resume claim is a generation-bump commit under `writer epoch +1` with a fresh `claim_nonce`: identical concurrent claims hash to the same CommitID with different bodies → the loser gets `idempotency_conflict` and runs zero effects. A retry of the same authorized intent (same idempotency key + answers digest, recorded on the run_resumed event) returns the recorded status.
- running-on-reopen: Run() loads the record first; status running → commits `recovery_required` and returns `outcome_unknown` without executing. Terminal/waiting records are rejected by fresh Run.
- Resume restores spent usage (activations/attempts/output bytes/active ms) into the journal before continuing — the budget never resets.

## Minor (deferred)
- Answer validation happens against `WaitRequest.AnswerSchema`; node-level wait schema authoring lands with the editor story.
- `resumeReplay` reads the recorded claim from run_resumed event data; a busy run mid-resume returns status `running` to an identical repeat.
