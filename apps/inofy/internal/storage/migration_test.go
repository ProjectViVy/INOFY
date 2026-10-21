package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
	appmigrations "github.com/ProjectViVy/inofy/apps/inofy/migrations"
)

func TestHashFreeMigrationPreservesValuesAndQuarantinesLegacyReceipts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, migration := range []string{"001_initial.sql", "002_run_source.sql"} {
		body, err := appmigrations.FS.ReadFile(migration)
		if err != nil {
			t.Fatal(err)
		}
		var version int
		if _, err := fmt.Sscanf(migration, "%d_", &version); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			t.Fatalf("apply %s: %v", migration, err)
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO schema_migrations(version) VALUES(?)", version); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO workflows(workflow_id, draft_artifact, draft_etag, definition_digest, artifact_digest)
		VALUES('wf', '{}', 'etag', 'legacy-definition', 'legacy-artifact');
		INSERT INTO workflow_revisions(workflow_id, revision, artifact, definition_digest, artifact_digest,
			used_catalog_digest, used_implementations)
		VALUES('wf', 1, '{"graph":{}}', 'legacy-definition', 'legacy-artifact', 'legacy-catalog', '{}');
		INSERT INTO runs(run_id, workflow_id, principal, revision, program_digest, status, input_digest,
			checkpoint_json, checkpoint_payload, checkpoint_checksum, source_json, input_json)
		VALUES('run-legacy', 'wf', 'owner', 1, 'legacy-program', 'waiting', 'legacy-input',
			'{"legacy":true}', 'old-checkpoint', 'legacy-checksum', '{"graph":{}}', '{"value":7}');
		INSERT INTO runs(run_id, program_digest, status) VALUES('run-ownerless', 'opaque-old-id', 'admitted');
		INSERT INTO runs(run_id, workflow_id, principal, revision, program_digest, status)
		VALUES('run-incomplete', 'wf', 'owner', 0, 'old-program', 'queued');
		INSERT INTO run_commits(run_id, commit_id, body_digest, first_sequence, last_sequence)
		VALUES('run-legacy', 'old-commit', 'legacy-body', 1, 1);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO runs(run_id, workflow_id, principal, revision, program_digest, status, source_json, input_json)
		 VALUES('run-draft', 'wf', 'owner', 0, 'legacy-draft-program', 'queued', ?, ?)`,
		[]byte(`{"draft":true}`), []byte(`{"value":9}`)); err != nil {
		t.Fatal(err)
	}

	s, err := storage.Open(ctx, path)
	if err != nil {
		t.Fatalf("apply hash-free migration: %v", err)
	}
	defer s.Close()
	state, err := s.Load(ctx, "run-legacy")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != inofy.RunRecoveryRequired || state.Ref.ProgramID != "wf/1" || string(state.AdmittedInput) != `{"value":7}` {
		t.Fatalf("legacy values not safely retained: %#v", state)
	}
	if state.LatestCheckpoint != nil {
		t.Fatal("legacy fingerprint-bound checkpoint remained resumable")
	}
	source, input, programID, workflowID, revision, err := s.RunSource(ctx, "run-draft")
	if err != nil {
		t.Fatal(err)
	}
	if string(source) != `{"draft":true}` || string(input) != `{"value":9}` ||
		programID != "wf/run/run-draft" || workflowID != "wf" || revision != 0 {
		t.Fatalf("draft admission lost its restart identity or values: source=%s input=%s program=%s workflow=%s revision=%d",
			source, input, programID, workflowID, revision)
	}
	incomplete, err := s.Load(ctx, "run-incomplete")
	if err != nil {
		t.Fatal(err)
	}
	if incomplete.Status != inofy.RunRecoveryRequired {
		t.Fatalf("legacy queued row without an exact source/input snapshot stayed dispatchable: %q", incomplete.Status)
	}
	_, err = s.Commit(ctx, state.Ref, inofy.RunCommit{CommitID: "old-commit"})
	var ie *inofy.Error
	if !errors.As(err, &ie) || ie.Code != inofy.ErrOutcomeUnknown {
		t.Fatalf("legacy digest receipt was replayed or misclassified: %v", err)
	}
	ownerless, err := s.Load(ctx, "run-ownerless")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Commit(ctx, inofy.ExecutionRef{RunID: "run-ownerless", ProgramID: "new-program"}, inofy.RunCommit{CommitID: "new-commit"})
	if !errors.As(err, &ie) || ie.Code != inofy.ErrCheckpointIncompatible || ownerless.Ref.ProgramID != "" {
		t.Fatalf("legacy opaque identity was accepted as authority: state=%#v err=%v", ownerless, err)
	}
}
