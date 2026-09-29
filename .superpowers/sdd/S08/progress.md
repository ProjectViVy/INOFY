# S08 — Standalone SQLite Authority and Dispatch

Status: Done. Branch `devin/s08-sqlite-app` (stacked on `devin/s07-draft-publish`).

## Evidence
- `TestSQLiteAtomicCommitAndCAS` — fresh install/reopen keeps evidence; same CommitID+body → original receipt; changed body → idempotency_conflict; lower epoch → stale_writer; failed strict commit → zero rows; waiting waits/interrupts/gates + checkpoint round-trip; concurrent draft CAS one winner; concurrent publish one revision; archive retains revision and blocks publish.
- `TestRestartClassificationAndQueueBounds` — 4 blocked workers + 32 pending → 33rd+ rejected, no hidden rows; admit-before-wakeup dispatches exactly once after process reopen; running-on-restart → recovery_required with zero replayed effects; waiting row stays waiting; same admission key returns same run ID.
- `CGO_ENABLED=0 go test ./... -count=3` green across both modules; `go vet` clean. Driver `modernc.org/sqlite v1.59.0` (published 2026-09-15, ≥7d).
- debug run recorded an over-dispatch bug (claimed rows invisible to ActiveCount → 5 > 4) and a pending-bound leak (claimed rows invisible to PendingCount) — both fixed by counting `claimed` in both queries.

## Rulings
- Separate Go module `apps/inofy` with `replace` to root; engine root keeps zero SQL drivers.
- 'queued' = admission placeholder (inspectable, zero commits); 'claimed' = dispatcher CAS slot holder; both adopted by the first `Expected=""` commit inside the same transaction — never an insert conflict.
- Claimed rows that never committed reset to 'queued' at Start (a claim alone proves nothing ran); rows with any commit are never 'claimed' at restart.
- Single-writer serialization: `SetMaxOpenConns(1)` + busy_timeout; transactions do the atomicity.
- Admission idempotency scoped `(principal, workflow_id, admission_key)` via partial UNIQUE index — same key replays the same run ID, no second row.
- sequences come from `MAX(seq)` on run_events inside the commit transaction.
- `program.go` classification accepts 'queued'/'claimed' placeholder statuses as not-admitted (App-internal, not public RunStatus constants).

## Minor (deferred)
- Migrations live in `apps/inofy/migrations/` behind a `migrations.FS` package (embed cannot cross package dirs); the plan's `apps/inofy/migrations/001_initial.sql` location is preserved.
- Dispatcher error surface is silent today (run failures land as durable failed/recovery_required status); a status query API is S09 HTTP.
- ETag generation uses a process-local counter (`wf:<n>`) — opaque by contract; host may swap.
