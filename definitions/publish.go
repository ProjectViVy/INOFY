package definitions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	inofy "github.com/ProjectViVy/inofy"
)

// Publish atomically validates the draft ETag against the frozen
// catalog, binds the exact used type/impl identities, and delegates
// monotone revision allocation plus same-artifact dedup to the
// repository transaction. The published revision is immutable and
// keeps referencing its catalog snapshot even after the draft moves.
func (s *Service) Publish(ctx context.Context, workflowID, expectedETag string, c inofy.Catalog) (Revision, error) {

	d, err := s.repo.GetDraft(ctx, workflowID)
	if err != nil {
		return Revision{}, err
	}
	if expectedETag != d.ETag {
		return Revision{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "stale etag"}
	}
	if d.Archived {
		return Revision{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID,
			Message: "archived workflow cannot publish"}
	}
	// The catalog snapshot must compile the draft: an impl that no
	// longer satisfies the definition never reaches publication.
	prog, diags, err := inofy.Compile(ctx, d.Artifact.Definition, c, inofy.CompileOptions{})
	if err != nil {
		return Revision{}, err
	}
	if len(diags) > 0 {
		return Revision{}, &inofy.Error{Code: inofy.ErrInvalidDefinition, Path: workflowID,
			Message: "draft does not compile against catalog: " + diags[0].Message}
	}
	used, err := usedImplementations(d.Artifact.Definition, c)
	if err != nil {
		return Revision{}, err
	}
	_ = prog // compile is validation only; digests live in meta
	ucd, err := usedCatalogDigest(used)
	if err != nil {
		return Revision{}, err
	}
	rev := Revision{
		WorkflowID:          workflowID,
		Artifact:            d.Artifact,
		DefinitionDigest:    d.DefinitionDigest,
		ArtifactDigest:      d.ArtifactDigest,
		UsedCatalogDigest:   ucd,
		UsedImplementations: used,
	}
	return s.repo.PublishCAS(ctx, workflowID, expectedETag, rev)
}

// GetRevision reads an immutable published revision. Archived
// workflows still resolve their referenced revisions (S07 plan).
func (s *Service) GetRevision(ctx context.Context, workflowID string, revision uint64) (Revision, error) {
	return s.repo.GetRevision(ctx, workflowID, revision)
}

// List returns a bounded page of published revisions.
func (s *Service) List(ctx context.Context, cursor string, limit int) (Page, error) {
	return s.repo.List(ctx, cursor, limit)
}

// usedImplementations resolves the exact impl ID every call node type
// maps to under the frozen catalog, across nested bodies.
func usedImplementations(def inofy.Definition, c inofy.Catalog) (map[string]string, error) {
	used := map[string]string{}
	var walk func(g inofy.Graph) error
	walk = func(g inofy.Graph) error {
		for _, n := range g.Nodes {
			if n.Kind == inofy.NodeKindCall || n.Type != "" {
				if n.Type == "" {
					continue
				}
				d, ok := c.Lookup(n.Type)
				if !ok {
					return &inofy.Error{Code: inofy.ErrBindingMissing,
						Message: "used type " + n.Type + " absent from frozen catalog"}
				}
				used[n.Type] = d.ImplementationID
			}
			if n.Body != nil {
				if err := walk(*n.Body); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(def.Graph); err != nil {
		return nil, err
	}
	return used, nil
}

// usedCatalogDigest binds the exact used type→impl map, never the
// whole catalog — unused impl changes don't force republication.
func usedCatalogDigest(used map[string]string) (string, error) {
	b, err := json.Marshal(used)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
