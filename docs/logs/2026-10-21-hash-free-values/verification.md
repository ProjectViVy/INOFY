# Verification

- Root module `go test ./...`: passed, including resume rejection for compiler-version and used-catalog contract drift.
- App module `go test ./internal/storage`: passed, including revision-0 and NULL-revision legacy admission cases.
- App module `go test ./... -skip '^TestStorePersistReopen$'`: passed all other packages.
- App module `go test ./...`: failed only at the unchanged Windows `internal/conns` permission assertion (`TestStorePersistReopen`: `secrets.json mode 666`). All other App packages passed. `wsl --status` returned `E_ACCESSDENIED`, so Linux execution remains for upstream CI.
- Studio `pnpm test`: passed, 44 tests across 6 files.
- Studio `pnpm build`: passed; `TEMP` and `TMP` pointed to clone-local scratch after the default Windows temp location denied esbuild cleanup.
- `git diff --check`: passed.
- Checkpoint payload checksum removal is intentional under the complete hash-free contract; this envelope no longer provides application-level checkpoint corruption detection.
- Eino capability check: this repository pins Eino v0.9.13. The existing `internal/einoruntime` uses `compose.WithCheckPointStore`, `compose.WithCheckPointID`, and `compose.BatchResumeWithData`; this change keeps those primitives and the Eino import boundary. The owner `ProgramID` and canonical definition/catalog values remain INOFY business identity in the existing RunStore envelope because Eino's opaque checkpoint payload/store key does not supply that owner contract.
