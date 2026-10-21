-- D11: owner identities and exact durable values replace content
-- fingerprints as execution and replay authority.
ALTER TABLE workflows DROP COLUMN definition_digest;
ALTER TABLE workflows DROP COLUMN artifact_digest;

CREATE TABLE workflow_revisions_new (
    workflow_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    artifact BLOB NOT NULL,
    used_implementations BLOB NOT NULL,
    published_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (workflow_id, revision),
    FOREIGN KEY (workflow_id) REFERENCES workflows(workflow_id)
);
INSERT INTO workflow_revisions_new (workflow_id, revision, artifact, used_implementations, published_at)
    SELECT workflow_id, revision, artifact, used_implementations, published_at FROM workflow_revisions;
DROP TABLE workflow_revisions;
ALTER TABLE workflow_revisions_new RENAME TO workflow_revisions;

ALTER TABLE runs ADD COLUMN program_id TEXT;
ALTER TABLE runs ADD COLUMN host_binding_id TEXT;
ALTER TABLE runs ADD COLUMN resume_answers_json BLOB;
UPDATE runs SET program_id = workflow_id || '/' || revision
    WHERE program_id IS NULL AND workflow_id IS NOT NULL AND revision IS NOT NULL AND revision > 0;
-- Draft admissions have revision 0 and no persisted draft ETag in the
-- legacy schema. Bind each immutable admitted snapshot to its existing
-- owner-allocated run ID so queued draft runs still rebuild after restart.
UPDATE runs SET program_id = workflow_id || '/run/' || run_id
    WHERE program_id IS NULL AND workflow_id IS NOT NULL AND (revision IS NULL OR revision = 0);
-- Old waiting checkpoints carry fingerprint-only identity and cannot
-- safely resume under the value contract. Keep their evidence, but stop
-- them from being treated as resumable.
UPDATE runs SET status = 'recovery_required', checkpoint_json = NULL, checkpoint_payload = NULL,
    waits_json = NULL, interrupts_json = NULL, gates_json = NULL
    WHERE status = 'waiting';
UPDATE runs SET status = 'recovery_required'
    WHERE status = 'running';
-- Earlier queued/admitted rows may predate migration 002 and have no
-- durable source/input snapshot. They cannot be safely rebuilt or replayed.
UPDATE runs SET status = 'recovery_required'
    WHERE status IN ('queued', 'claimed', 'admitted') AND (source_json IS NULL OR input_json IS NULL);
ALTER TABLE runs DROP COLUMN program_digest;
ALTER TABLE runs DROP COLUMN input_digest;
ALTER TABLE runs DROP COLUMN resume_answers_digest;
ALTER TABLE runs DROP COLUMN checkpoint_checksum;

CREATE TABLE run_commits_new (
    run_id TEXT NOT NULL,
    commit_id TEXT NOT NULL,
    body BLOB,
    first_sequence INTEGER NOT NULL,
    last_sequence INTEGER NOT NULL,
    PRIMARY KEY (run_id, commit_id),
    FOREIGN KEY (run_id) REFERENCES runs(run_id)
);
-- Historical digests cannot reconstruct the original request. Preserve
-- receipt ranges with NULL bodies so redelivery fails closed as unknown.
INSERT INTO run_commits_new (run_id, commit_id, body, first_sequence, last_sequence)
    SELECT run_id, commit_id, NULL, first_sequence, last_sequence FROM run_commits;
DROP TABLE run_commits;
ALTER TABLE run_commits_new RENAME TO run_commits;

ALTER TABLE node_executions DROP COLUMN output_digest;
