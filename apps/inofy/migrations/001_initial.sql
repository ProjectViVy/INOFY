-- 001_initial: App authority schema (architecture §11.2).
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS workflows (
    workflow_id TEXT PRIMARY KEY,
    draft_artifact BLOB NOT NULL,
    draft_etag TEXT NOT NULL,
    definition_digest TEXT NOT NULL,
    artifact_digest TEXT NOT NULL,
    next_revision INTEGER NOT NULL DEFAULT 1,
    archived INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE IF NOT EXISTS workflow_revisions (
    workflow_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    artifact BLOB NOT NULL,
    definition_digest TEXT NOT NULL,
    artifact_digest TEXT NOT NULL,
    used_catalog_digest TEXT NOT NULL,
    used_implementations BLOB NOT NULL,
    published_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (workflow_id, revision),
    UNIQUE (workflow_id, artifact_digest, used_catalog_digest),
    FOREIGN KEY (workflow_id) REFERENCES workflows(workflow_id)
);

CREATE TABLE IF NOT EXISTS runs (
    run_id TEXT PRIMARY KEY,
    workflow_id TEXT,
    principal TEXT,
    revision INTEGER,
    program_digest TEXT,
    status TEXT NOT NULL,
    writer_epoch INTEGER NOT NULL DEFAULT 0,
    generation INTEGER NOT NULL DEFAULT 0,
    input_digest TEXT,
    limits_json BLOB,
    usage_json BLOB,
    waits_json BLOB,
    interrupts_json BLOB,
    gates_json BLOB,
    resume_key TEXT,
    resume_answers_digest TEXT,
    checkpoint_json BLOB,
    checkpoint_payload BLOB,
    checkpoint_checksum TEXT,
    admission_key TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS runs_admission_key ON runs(principal, workflow_id, admission_key)
    WHERE admission_key IS NOT NULL;

CREATE TABLE IF NOT EXISTS run_commits (
    run_id TEXT NOT NULL,
    commit_id TEXT NOT NULL,
    body_digest TEXT NOT NULL,
    first_sequence INTEGER NOT NULL,
    last_sequence INTEGER NOT NULL,
    PRIMARY KEY (run_id, commit_id),
    FOREIGN KEY (run_id) REFERENCES runs(run_id)
);

CREATE TABLE IF NOT EXISTS run_events (
    run_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    kind TEXT NOT NULL,
    path TEXT,
    attempt INTEGER,
    data BLOB,
    PRIMARY KEY (run_id, seq),
    FOREIGN KEY (run_id) REFERENCES runs(run_id)
);

CREATE TABLE IF NOT EXISTS node_executions (
    run_id TEXT NOT NULL,
    node_path TEXT NOT NULL,
    attempt INTEGER NOT NULL,
    operation_key TEXT NOT NULL,
    unresolved INTEGER NOT NULL DEFAULT 0,
    output_digest TEXT,
    protected_output BLOB,
    PRIMARY KEY (run_id, node_path, attempt),
    FOREIGN KEY (run_id) REFERENCES runs(run_id)
);
