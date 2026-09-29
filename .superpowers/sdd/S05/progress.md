# S05 — Bounded Execution and Durable Events

## Status: Done

## Rulings
- The plan's `internal/execution` package would import-cycle on root
  inofy types (NodeCall/RunStore/etc.); the effect boundary and the
  memory adapter therefore live in the root package
  (`execution.go`, `memory_store.go`) — same contract, lawful imports.
- Retry, per-attempt timeout, fallback materialization and replay
  class moved OUT of the einoruntime call lambda into the run-scoped
  journal wrapper (`einoruntime.Call` now carries resolved
  Replay/MaxAttempts/DelayMS/TimeoutMS/OnError/OutputSchema). The
  lambda keeps binding, input-schema and output-schema validation.
- CommitID = RunID/generation/path/attempt/transition-ordinal with an
  atomic run-wide ordinal; OperationKey = RunID + "/" + logical path
  (iterations differ, retries identical).
- All commits use a bounded detached persistence context
  (context.WithoutCancel + 5s) — cancellation must not prevent
  recording a known outcome.
- MemoryRunStore dedupes CommitID before transition/epoch checks
  (redelivery returns the original receipt), tracks unresolved
  operations per path+attempt, and keeps events/results immutable.
- Eino cyclic subgraphs need a step bound even though dag mode
  rejects WithMaxRunSteps: repeat subgraphs compile with
  `WithGraphCompileOptions(WithMaxRunSteps(maxIter*4+8))` — a
  mechanical ceiling on top of the semantic iteration cap.
- Nil RunStore = documented no-journal embedding (backward
  compatible); NewMemoryRunStore is the explicit ephemeral choice.
- Compile-time validation already rejects retry on non-replayable
  types (replay_forbidden) and schema-violating fallbacks; the run
  boundary keeps the same defenses (attempt clamp, fallback
  validation) so violations can't be smuggled in.
- on_error marshals as {mode,value} — the runtime reads `value`
  (was `literal`).

## Deferred (minor)
- runClock.Pause/Resume exists and is tested but nothing calls it
  yet — wait replies still return unsupported_feature (S06 wires
  staged waits and clock freezing).
- Sibling in-flight outcome tracking for cancel is executor-level;
  RunStore unresolved-ops list is populated for S06 reconciliation.

## Evidence
- go test ./... -count=3 green; go test -race clean; go vet clean.
- TestEffectCommitBoundaries: start-commit failure → zero
  invocations; result-commit failure → recovery_required, no
  downstream; terminal-commit failure reported; CommitID idempotent
  replay vs idempotency_conflict; stale epoch → stale_writer; unknown
  sibling outcome dominates to recovery_required.
- TestEphemeralStoreRestartUnavailable: fresh instance sees nothing.
- TestRunBounds: permit caps concurrent leaves at 4; activation 257
  rejected budget_exceeded; retry max 3 incl first with stable
  OperationKey; non-replayable never retried (admission + clamp);
  unknown outcome stops retry immediately; retry delay honors cancel;
  fallback compile-validated + degraded event; output size cap.
- TestRunClockFreezesDuringWait: wait time excluded, resume keeps
  spent budget.
