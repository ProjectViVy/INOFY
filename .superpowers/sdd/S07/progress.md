# S07 — Reusable Draft and Publication Service

Status: Done. Branch `devin/s07-draft-publish` (stacked on `devin/s06-wait-resume`).

## Evidence
- `TestDraftCAS` — create/stale-ETag/4-way concurrent save (1 win, 3 conflicts)/invalid artifact/layout-vs-semantic digest split; ValidateDraft compiles without saving.
- `TestPublishIdentity` — same-artifact republication returns revision 1 (no new row); changed used impl allocates revision 2 with a different used-catalog digest; dual publish CAS has exactly one winner; archive keeps revisions readable and blocks publish; post-publish draft edits never move a revision.
- `go test ./... -count=3` green; `go vet` clean; stdlib only.

## Rulings
- `ETagAbsent = "\x00absent"` is the create-only sentinel — an empty expected ETag never means "any", it means not-found on update.
- Used-catalog digest binds the exact used `type→impl` map, not the whole catalog: unused impl changes don't force republication.
- Compile against the frozen catalog is the publication gate — an impl that no longer satisfies the draft never reaches a revision.
- `MemoryRepository` is a test/ephemeral reference implementation, explicitly not a production authority; hosts implement `Repository` transactions (S08 SQLite adapter, S11 ViVy Journal).
- Draft row stores computed digests via optional `SetDraftDigests` hook so host repos that persist whole rows stay authoritative.

## Minor (deferred)
- `List` cursor is opaque `workflow:revision`; page size default 50.
- Publish does not rotate the draft ETag — draft and latest revision may share identity until the next save.
