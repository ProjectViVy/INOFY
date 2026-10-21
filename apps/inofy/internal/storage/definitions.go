package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/definitions"
)

// The same Store implements definitions.Repository: draft CAS,
// transactional publication, immutable revision reads and bounded
// listing — all inside SQLite transactions (§11.2).

func (s *Store) GetDraft(ctx context.Context, workflowID string) (definitions.Draft, error) {
	var (
		artJSON, etag string
		archived      int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT draft_artifact, draft_etag, archived
		 FROM workflows WHERE workflow_id = ?`, workflowID).
		Scan(&artJSON, &etag, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return definitions.Draft{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "workflow not found"}
	}
	if err != nil {
		return definitions.Draft{}, err
	}
	var a inofy.Artifact
	if err := json.Unmarshal([]byte(artJSON), &a); err != nil {
		return definitions.Draft{}, err
	}
	return definitions.Draft{
		WorkflowID: workflowID,
		ETag:       etag,
		Artifact:   a,
		Archived:   archived == 1,
	}, nil
}

func (s *Store) UpdateDraftCAS(ctx context.Context, workflowID, expectedETag string, a inofy.Artifact) (definitions.Draft, error) {
	artJSON, err := inofy.NormalizeArtifact(a)
	if err != nil {
		return definitions.Draft{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return definitions.Draft{}, err
	}
	defer tx.Rollback()
	var etag string
	err = tx.QueryRowContext(ctx,
		`SELECT draft_etag FROM workflows WHERE workflow_id = ?`, workflowID).Scan(&etag)
	notFoundErr := errors.Is(err, sql.ErrNoRows)
	if err != nil && !notFoundErr {
		return definitions.Draft{}, err
	}
	switch {
	case expectedETag == definitions.ETagAbsent && !notFoundErr:
		return definitions.Draft{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "draft already exists"}
	case expectedETag != definitions.ETagAbsent && notFoundErr:
		return definitions.Draft{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "workflow not found"}
	case expectedETag != definitions.ETagAbsent && etag != expectedETag:
		return definitions.Draft{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "stale etag"}
	}
	newTag := fmt.Sprintf("%x", nextETag())
	if notFoundErr {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO workflows (workflow_id, draft_artifact, draft_etag)
			 VALUES (?, ?, ?)`, workflowID, artJSON, newTag); err != nil {
			return definitions.Draft{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE workflows SET draft_artifact = ?, draft_etag = ?
			 WHERE workflow_id = ? AND draft_etag = ?`,
			artJSON, newTag, workflowID, expectedETag); err != nil {
			return definitions.Draft{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return definitions.Draft{}, err
	}
	return definitions.Draft{
		WorkflowID: workflowID, ETag: newTag, Artifact: a,
	}, nil
}

func (s *Store) PublishCAS(ctx context.Context, workflowID, expectedETag string, rev definitions.Revision) (definitions.Revision, error) {
	artJSON, err := inofy.NormalizeArtifact(rev.Artifact)
	if err != nil {
		return definitions.Revision{}, err
	}
	implJSON, err := json.Marshal(rev.UsedImplementations)
	if err != nil {
		return definitions.Revision{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return definitions.Revision{}, err
	}
	defer tx.Rollback()
	var etag string
	var archived int
	err = tx.QueryRowContext(ctx,
		`SELECT draft_etag, archived FROM workflows WHERE workflow_id = ?`, workflowID).
		Scan(&etag, &archived)
	if errors.Is(err, sql.ErrNoRows) {
		return definitions.Revision{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "workflow not found"}
	}
	if err != nil {
		return definitions.Revision{}, err
	}
	if archived == 1 {
		return definitions.Revision{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "archived workflow cannot publish"}
	}
	if etag != expectedETag {
		return definitions.Revision{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "stale etag"}
	}
	// Exact artifact bytes and implementation values own republication
	// identity. Scan this workflow's bounded history instead of storing a
	// content fingerprint as authority.
	rows, err := tx.QueryContext(ctx,
		`SELECT revision, artifact, used_implementations FROM workflow_revisions WHERE workflow_id = ?`, workflowID)
	if err != nil {
		return definitions.Revision{}, err
	}
	for rows.Next() {
		var existing definitions.Revision
		var exArt, exImpl []byte
		if err := rows.Scan(&existing.Revision, &exArt, &exImpl); err != nil {
			rows.Close()
			return definitions.Revision{}, err
		}
		var oldArtifact inofy.Artifact
		var oldImpl map[string]string
		if err := json.Unmarshal(exArt, &oldArtifact); err != nil {
			rows.Close()
			return definitions.Revision{}, err
		}
		if err := json.Unmarshal(exImpl, &oldImpl); err != nil {
			rows.Close()
			return definitions.Revision{}, err
		}
		oldCanonical, err := inofy.NormalizeArtifact(oldArtifact)
		if err != nil {
			rows.Close()
			return definitions.Revision{}, err
		}
		oldImplJSON, _ := json.Marshal(oldImpl)
		if bytes.Equal(oldCanonical, artJSON) && bytes.Equal(oldImplJSON, implJSON) {
			existing.WorkflowID = workflowID
			existing.ProgramID = definitions.ProgramID(workflowID, existing.Revision)
			existing.Artifact = oldArtifact
			existing.UsedImplementations = oldImpl
			rows.Close()
			if err := tx.Commit(); err != nil {
				return definitions.Revision{}, err
			}
			return existing, nil
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return definitions.Revision{}, err
	}
	if err := rows.Close(); err != nil {
		return definitions.Revision{}, err
	}
	var n int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision),0)+1 FROM workflow_revisions WHERE workflow_id = ?`,
		workflowID).Scan(&n); err != nil {
		return definitions.Revision{}, err
	}
	rev.Revision = uint64(n)
	rev.ProgramID = definitions.ProgramID(workflowID, rev.Revision)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workflow_revisions
		 (workflow_id, revision, artifact, used_implementations)
		 VALUES (?, ?, ?, ?)`,
		workflowID, n, artJSON, implJSON); err != nil {
		return definitions.Revision{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE workflows SET next_revision = ? WHERE workflow_id = ?`, n+1, workflowID); err != nil {
		return definitions.Revision{}, err
	}
	if err := tx.Commit(); err != nil {
		return definitions.Revision{}, err
	}
	return rev, nil
}

func (s *Store) GetRevision(ctx context.Context, workflowID string, revision uint64) (definitions.Revision, error) {
	var (
		artJSON, implJSON []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT artifact, used_implementations
		 FROM workflow_revisions WHERE workflow_id = ? AND revision = ?`,
		workflowID, revision).Scan(&artJSON, &implJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return definitions.Revision{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "revision not found"}
	}
	if err != nil {
		return definitions.Revision{}, err
	}
	var rev definitions.Revision
	rev.WorkflowID = workflowID
	rev.Revision = revision
	rev.ProgramID = definitions.ProgramID(workflowID, revision)
	if err := json.Unmarshal(artJSON, &rev.Artifact); err != nil {
		return definitions.Revision{}, err
	}
	if err := json.Unmarshal(implJSON, &rev.UsedImplementations); err != nil {
		return definitions.Revision{}, err
	}
	return rev, nil
}

func (s *Store) List(ctx context.Context, cursor string, limit int) (definitions.Page, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT workflow_id, revision FROM workflow_revisions
		 WHERE (workflow_id || ':' || revision) > ? ORDER BY workflow_id, revision LIMIT ?`,
		cursor, limit+1)
	if err != nil {
		return definitions.Page{}, err
	}
	defer rows.Close()
	page := definitions.Page{}
	for rows.Next() {
		var r definitions.Revision
		if err := rows.Scan(&r.WorkflowID, &r.Revision); err != nil {
			return definitions.Page{}, err
		}
		page.Revisions = append(page.Revisions, r)
	}
	if err := rows.Err(); err != nil {
		return definitions.Page{}, err
	}
	if len(page.Revisions) > limit {
		page.Revisions = page.Revisions[:limit]
		last := page.Revisions[len(page.Revisions)-1]
		page.NextCursor = fmt.Sprintf("%s:%d", last.WorkflowID, last.Revision)
	}
	return page, nil
}

// Archive marks the workflow archived; referenced revisions remain
// readable (no cascade delete — §11.2 referential retention).
func (s *Store) Archive(ctx context.Context, workflowID string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE workflows SET archived = 1 WHERE workflow_id = ?`, workflowID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "workflow not found"}
	}
	return nil
}
