package storage

import (
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
		artJSON, etag, defDig, artDig string
		archived                      int
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT draft_artifact, draft_etag, definition_digest, artifact_digest, archived
		 FROM workflows WHERE workflow_id = ?`, workflowID).
		Scan(&artJSON, &etag, &defDig, &artDig, &archived)
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
		WorkflowID:       workflowID,
		ETag:             etag,
		Artifact:         a,
		DefinitionDigest: defDig,
		ArtifactDigest:   artDig,
		Archived:         archived == 1,
	}, nil
}

func (s *Store) UpdateDraftCAS(ctx context.Context, workflowID, expectedETag string, a inofy.Artifact) (definitions.Draft, error) {
	artJSON, err := json.Marshal(a)
	if err != nil {
		return definitions.Draft{}, err
	}
	defDig, err := inofy.DefinitionDigest(a.Definition)
	if err != nil {
		return definitions.Draft{}, err
	}
	artDig, err := inofy.ArtifactDigest(a)
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
			`INSERT INTO workflows (workflow_id, draft_artifact, draft_etag, definition_digest, artifact_digest)
			 VALUES (?, ?, ?, ?, ?)`, workflowID, artJSON, newTag, defDig, artDig); err != nil {
			return definitions.Draft{}, err
		}
	} else {
		if _, err := tx.ExecContext(ctx,
			`UPDATE workflows SET draft_artifact = ?, draft_etag = ?, definition_digest = ?, artifact_digest = ?
			 WHERE workflow_id = ? AND draft_etag = ?`,
			artJSON, newTag, defDig, artDig, workflowID, expectedETag); err != nil {
			return definitions.Draft{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return definitions.Draft{}, err
	}
	return definitions.Draft{
		WorkflowID: workflowID, ETag: newTag, Artifact: a,
		DefinitionDigest: defDig, ArtifactDigest: artDig,
	}, nil
}

// SetDraftDigests is the optional hook Service.SaveDraft uses; the
// SQLite path already persists digests inside UpdateDraftCAS.
func (s *Store) SetDraftDigests(_ context.Context, _, _, _, _ string) error { return nil }

func (s *Store) PublishCAS(ctx context.Context, workflowID, expectedETag string, rev definitions.Revision) (definitions.Revision, error) {
	artJSON, err := json.Marshal(rev.Artifact)
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
	// Same-artifact republication is idempotent under the same used
	// catalog identity: the UNIQUE key makes the existing row the
	// dedup point.
	var existing definitions.Revision
	var exArt, exImpl []byte
	err = tx.QueryRowContext(ctx,
		`SELECT revision, artifact, used_implementations FROM workflow_revisions
		 WHERE workflow_id = ? AND artifact_digest = ? AND used_catalog_digest = ?`,
		workflowID, rev.ArtifactDigest, rev.UsedCatalogDigest).
		Scan(&existing.Revision, &exArt, &exImpl)
	if err == nil {
		json.Unmarshal(exArt, &existing.Artifact)
		json.Unmarshal(exImpl, &existing.UsedImplementations)
		existing.WorkflowID = workflowID
		existing.DefinitionDigest = rev.DefinitionDigest
		existing.ArtifactDigest = rev.ArtifactDigest
		existing.UsedCatalogDigest = rev.UsedCatalogDigest
		if err := tx.Commit(); err != nil {
			return definitions.Revision{}, err
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return definitions.Revision{}, err
	}
	var n int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(revision),0)+1 FROM workflow_revisions WHERE workflow_id = ?`,
		workflowID).Scan(&n); err != nil {
		return definitions.Revision{}, err
	}
	rev.Revision = uint64(n)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO workflow_revisions
		 (workflow_id, revision, artifact, definition_digest, artifact_digest, used_catalog_digest, used_implementations)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		workflowID, n, artJSON, rev.DefinitionDigest, rev.ArtifactDigest, rev.UsedCatalogDigest, implJSON); err != nil {
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
		artJSON, implJSON            []byte
		defDig, artDig, usedDig      string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT artifact, definition_digest, artifact_digest, used_catalog_digest, used_implementations
		 FROM workflow_revisions WHERE workflow_id = ? AND revision = ?`,
		workflowID, revision).Scan(&artJSON, &defDig, &artDig, &usedDig, &implJSON)
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
	rev.DefinitionDigest = defDig
	rev.ArtifactDigest = artDig
	rev.UsedCatalogDigest = usedDig
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
