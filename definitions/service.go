package definitions

import (
	"context"
	"encoding/json"

	inofy "github.com/ProjectViVy/inofy"
)

// Service is the optional reusable draft and publication surface
// (architecture §4.4): it validates artifacts once per update against
// a frozen catalog, delegates every CAS/allocation decision to the
// repository transaction, and never invents storage authority.
type Service struct {
	repo Repository
}

// NewService binds a service to a host repository.
func NewService(repo Repository) *Service {
	return &Service{repo: repo}
}

// SaveDraft validates the artifact, then stores it under ETag CAS:
// expectedETag must equal the durable ETag from the previous save
// (ETagAbsent creates the workflow). Two editors racing one ETag
// produce exactly one success.
func (s *Service) SaveDraft(ctx context.Context, workflowID, expectedETag string, a inofy.Artifact) (Draft, error) {
	if workflowID == "" {
		return Draft{}, &inofy.Error{Code: inofy.ErrInvalidDefinition, Message: "workflow id required"}
	}
	// Structural validation happens once here, before CAS, so an
	// invalid artifact can never consume an ETag.
	if _, diags, err := inofy.DecodeArtifact(mustMarshal(a)); err != nil {
		return Draft{}, err
	} else if len(diags) > 0 {
		return Draft{}, &inofy.Error{Code: inofy.ErrInvalidDefinition, Message: diags[0].Message}
	}
	return s.repo.UpdateDraftCAS(ctx, workflowID, expectedETag, a)
}

// ValidateDraft compiles the current draft against the frozen
// catalog without saving anything.
func (s *Service) ValidateDraft(ctx context.Context, workflowID, expectedETag string, c inofy.Catalog) ([]inofy.Diagnostic, error) {
	d, err := s.repo.GetDraft(ctx, workflowID)
	if err != nil {
		return nil, err
	}
	if expectedETag != "" && expectedETag != d.ETag {
		return nil, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: workflowID, Message: "stale etag"}
	}
	_, diags, err := inofy.Compile(ctx, d.Artifact.Definition, c, inofy.CompileOptions{})
	return diags, err
}

func mustMarshal(a inofy.Artifact) []byte {
	b, err := json.Marshal(a)
	if err != nil {
		// Artifacts marshal deterministically; a failure here means
		// the caller handed in a non-JSON value inside Extra.
		return []byte(`{"definition":null,"presentation":{}}`)
	}
	return b
}
