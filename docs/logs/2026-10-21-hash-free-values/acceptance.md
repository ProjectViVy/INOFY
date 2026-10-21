# Acceptance

- A published workflow revision receives `workflow/revision` as its stable
  owner identity; a draft admission binds the owner ETag.
- The same admission key with different source or input returns
  `idempotency_conflict` and cannot create a second run.
- In-memory and SQLite RunStores accept semantically equal canonical JSON
  redelivery and reject changed request values under the same commit ID.
- Resume replay checks the admitted program and host identity before returning
  a recorded outcome.
- Existing revision-0 draft runs recover using their run ID as owner identity.
- Legacy queued admissions without their exact source/input values do not
  dispatch after migration.
- Existing waiting checkpoints move to `recovery_required`; their old commit
  receipts remain visible but return `outcome_unknown` on redelivery.
- A checkpoint cannot resume under a different compiler version or changed
  direct values for the catalog descriptors used to compile it, even when the
  owner ProgramID remains the same.
