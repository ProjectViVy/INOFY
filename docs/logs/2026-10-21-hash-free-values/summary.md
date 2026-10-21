# D11 hash-free values and owner identity

Replaced digest authority in the INOFY library and App with owner-allocated
`ProgramID` values and exact canonical JSON values. Definition publication
compares artifact and implementation values directly. Run admission, resume,
checkpoints, and commit replay bind the exact admitted source/input/answers and
the program/host identity. Existing Eino composition remains in place.

App migration `003_hash_free_values.sql` drops active fingerprint columns,
backfills stable IDs for revision and draft runs, moves old waiting checkpoints
and incomplete queued admissions to `recovery_required`, and preserves hash-only
receipts as unreplayable legacy receipts. The Studio no longer expects or
displays definition fingerprints.

This delivery changes INOFY only. Vivy’s dependency pin and integration are not
part of this change.
