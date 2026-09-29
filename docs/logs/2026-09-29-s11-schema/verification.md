# Verification

- Baseline `go test ./...`: failed `TestEinoModuleVersionPinned` because the
  Go 1.26 test binary's build info had no dependency entries.
- New schema agreement test, before implementation: build failed with
  `undefined: inofy.DefinitionSchema`.
- Stability test, before ordering fix: failed `schema bytes must be stable
  across calls`.
- `go test ./... -run 'Definition|Schema' -count=1`: passed after export.
- `go test ./... -count=1`: passed all five root-module packages after the
  version-probe fallback and deterministic ordering.
- `git diff --check`: passed.
