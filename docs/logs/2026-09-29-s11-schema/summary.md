# S11-A canonical Definition schema

Exported `DefinitionSchema()` from the root library. Its shape uses the strict
decoder's allowed-field and node-kind tables, with deterministic ordering and
defensive bytes. No App, editor, or host capability was added. The export is
an authoring aid; executable admission still uses the strict decoder and
semantic/catalog checks.

The existing Eino pin probe also gained a resolved-module fallback because
the Go 1.26 test binary in this environment did not expose dependencies in
`debug.ReadBuildInfo`.
