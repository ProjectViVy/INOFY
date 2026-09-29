# S10 — Reusable Visual Editor

## Verdict
Done (G10 browser acceptance partially deferred: ViVy host adapter is a seam until S11; App transport is real). `studio/` TypeScript package: transport-first React Flow editor rendering the artifact as a projection; App + ViVy `StudioTransport` adapters; draft/save/publish/run/wait surfaces.

## Evidence
- `pnpm vitest run` 17/17 green; `pnpm build` (tsc --noEmit + vite) emits `dist/` assets.
- graph.test.ts (7): full S02 fixture round-trip incl. switch ports/select candidates/repeat body; Unicode/9007199254740991/null bindings; unknown type flagged + preserved; layout-only edits never touch definition; membership edits map to semantic nodes/edges.
- flows.test.tsx (7): stale-ETag save keeps draft + surfaces conflict; publish uses exact ETag; run admits `draft_etag` snapshot; complete-answers-only resume; SSE replay dedupes by committed seq; en/zh; Ctrl+Z undo without server calls.
- Pinned deps all ≥1 year old: react 19.2.0, @xyflow/react 12.9.0, vitest 3.2.4, vite 7.1.0, typescript 5.9.3, jsdom 26.1.0.

## Rulings
- Canvas is a *view*: semantic fields React Flow can't represent live verbatim in `node.data.node`; `fromCanvas` rebuilds the artifact — semantic identity is never derived from canvas state.
- Repeat bodies render as one opaque node (no nested subflow); the body round-trips untouched inside node data.
- Layout write-back only when authored/moved/new — an untouched canvas produces byte-identical presentation (no drift).
- Node removal deletes incident edges; exits survive only while their node survives.
- SSE cursor = durable committed `seq`; stream failure falls back to polling from last delivered seq — UI never invents events.
- WaitPanel parses JSON-looking answers, else sends the raw string; submits only when every outstanding wait is answered.
- Editor owns canvas state during editing; App bumps `canvasKey` on undo to remount.

## minor (deferred)
- G10 real-browser E2E pending S13 packaging (plan allows fake-transport dev + real-adapter acceptance later).
- VivyTransport is the S11 seam (injected `HostBridge`); no live ViVy RPC until S11.
- Config form covers flat object schemas (string/number/integer/boolean); richer schemas fall back to a JSON textarea with explicit parse errors.
- Unsafe integers (>2^53) inside JSON textarea edits are only protected by server-side validation; the editor does not preserve their raw lexeme.
