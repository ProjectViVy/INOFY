# S03 ledger — Eino DAG, Switch and Select Compiler

## Task 1 (d6d3273) — DAG compile + single run
- Tests: TestProgramDAG (einoruntime) + TestProgramDAGPublic (root) + consumer example now runs the library end to end.
- Ruling: START input travels as packetOf(userInput) so every node maps "input" uniformly; roots take AddInput(START), gated nodes take MapFields("input","input")+WithNoDirectDependency.
- Ruling: ToField(from) (whole packet) is the data-only mapping for port edges and dominating-ancestor region entries; MapFields(from,from) was wrong (reads key "from" inside the packet).
- Ruling: select joins take normal AddInput(candidate) — Eino treats skipped predecessors as absent keys, so AndInput works and multi-arrival is impossible post-S02.
- Ruling: ProgramDigest = inofy-normal-v1 digest of {compiler_version=inofy/v0.1, eino_build=<linked module version via debug.ReadBuildInfo>, definition_digest, catalog_digest}.
- Ruling: Program.Run rejects resume (S06), wait replies (S06), missing executor, mismatched ProgramDigest, and run limits wider than the compile ceiling.
- Ruling: repeat nodes compile → unsupported_feature until S04.
- Amendment: topology now rejects outputs bindings not naming exits (output_not_exit) — closing the deferred S02 gap.
- minor (deferred): RunStore commits (attempt before execute, settle receipts) land in S05; S03 keeps the boundary signature only.

## Task 2 (b6a557d) — structured branches
- Tests: TestStructuredBranch (7 subtests) + TestStructuredBranchPublic.
- Ruling: skipped diagnostics derive from the recorded switch decision log + compile-time region index, never arrival order; code node_skipped, path /graph/nodes/<id>.
- Ruling: exists predicate resolves its pointer against the switch's bound input object; comparison operands bind packets as usual.
- Ruling: JSON Pointer "/" means the empty-string key (RFC 6901) — whole-value bindings use "".
- minor (deferred): numeric predicates coerce int/float64/json.Number to float64 — 64-bit integer precision limits apply (spec already bounds unsafe integers at normalize time).
