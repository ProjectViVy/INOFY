# SDD ledger — plan: docs/superpowers/plans/issue-91/D11.md (upstream contract cut)

## Scope

Implement the coordinated hash-free INOFY contract at current upstream pin `71e2c9bbe47d5d095b27d756e38eaff565d93d36`, preserving definition CAS/publication, exact owner revisions, run admission input, resume claim conflicts, host binding, and unknown-outcome recovery. This is upstream only; Vivy integration and pin update remain separate.

## Findings

- `internal/definition/digest.go`, root `definition.go`, `compiler.go`, `program.go`, definitions service/repositories, standalone App storage, SQL schema, HTTP projections, and tests all consume digest authority.
- App admission already persists exact `source_json` and `input_json`; this is the natural authority for admitted program snapshot/input. The general RunStore does not retain exact admitted input or resume answers yet.
- App workflow publication stores artifact bytes and used type→implementation values, so digest deduplication can become exact artifact/catalog value comparison.
- App commit receipts store only `body_digest`; preserving same-ID conflict requires storing the canonical commit body. Legacy rows cannot be reconstructed from that digest, so migration must preserve receipts but make old redelivery refuse safely.
- App execution currently has `ProgramDigest` but no stable owner allocated `ProgramID`; the revision owner can allocate `workflowID/revision` and pass it to Compile.
- INOFY uses the pinned Eino compose workflow/compiler path in `internal/einoruntime`; this change does not replace scheduling or introduce a second runtime.

## Ruling

Use owner-allocated immutable `ProgramID` plus direct canonical values at existing owners. Keep canonical JSON normalization only as a value representation, not a fingerprint. Preserve the existing app's exact admitted source/input and revisions. Convert legacy hash-only commit receipts to an explicit unreplayable legacy receipt shape and reject ambiguous retry rather than treating a digest as proof.

## Tasks

1. Add public hash-free API/owner contract tests and observe RED.
2. Replace library program/draft/publication/checkpoint/input/resume digest authority with exact values and ProgramID.
3. Replace App persistence and API projections; add forward migration for existing stores and tests for legacy refusal.
4. Replace digest-specific specs/plans with the new contract; run focused and module checks.
5. Review status/residue, record precise upstream delivery and host integration needs.

## Progress

- Task 1: complete. Added coverage for exact program/input identity, owner
  publication identity, exact resume answers, foreign host binding, and
  semantic commit replay.
- Task 2: complete. Replaced program/draft/publication/checkpoint/input/resume
  fingerprints with owner IDs and direct canonical values. Existing Eino
  composition remains the runtime; the pinned Eino v0.9.13 API inspection found
  no need for a new execution mechanism.
- Task 3: complete. App SQLite stores exact admission values and canonical
  commit request bodies. Migration 003 removes active digest columns, assigns
  legacy revision and draft-run identities, quarantines old waits and queued
  rows without exact snapshots, and preserves old commit receipts as explicitly
  unreplayable.
- Task 4: complete. Updated the active INOFY contract and Studio API projection;
  retained historical migrations as immutable transition records.
- Task 5: implementation review found and fixed four boundary defects: legacy
  draft ProgramID backfill, canonical commit replay parity, identity checks
  before terminal resume replay, and strict SQLite host-binding equality.

## Verification

- Root module `go test ./...`: passed.
- App module affected packages `go test ./internal/auth ./internal/dispatch ./internal/httpapi ./internal/nodes ./internal/storage`: passed, including fresh install and legacy migration tests.
- App module `go test ./... -skip '^TestStorePersistReopen$'`: passed all packages other than the platform-incompatible Windows permission assertion below.
- App module `go test ./...`: all packages except the unchanged `internal/conns` test pass. `TestStorePersistReopen` fails on Windows because `secrets.json` reports mode `666`; the WSL check is denied in this environment, so Linux execution remains for upstream CI.
- Studio `pnpm test`: passed, 44 tests across 6 files.
- Studio `pnpm build`: passed with `TEMP`/`TMP` directed to the clone-local scratch folder.
- `git diff --check`: passed.

## Acceptance

Published revisions use `workflow/revision` ProgramIDs and draft admissions use
the owner ETag. Reusing an admission key with changed input returns a conflict.
Run recovery binds the exact admitted input, resume answers, program, host, and
checkpoint values. Legacy waiting checkpoints and queued admissions without
exact snapshots stop as recovery-required, while legacy digest-only commit
receipts return `outcome_unknown`; no old digest is treated as identity or
replay evidence. The host repository pin update and integration are separate
from this upstream delivery.
