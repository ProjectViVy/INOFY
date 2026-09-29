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
	defDigest, err := inofy.DefinitionDigest(a.Definition)
	if err != nil {
		return Draft{}, err
	}
	artDigest, err := inofy.ArtifactDigest(a)
	if err != nil {
		return Draft{}, err
	}
	d, err := s.repo.UpdateDraftCAS(ctx, workflowID, expectedETag, a)
	if err != nil {
		return Draft{}, err
	}
	d.DefinitionDigest = defDigest
	d.ArtifactDigest = artDigest
	// The durable draft must carry the digests the service computed;
	// persist them through one more CAS hop only when the repository
	// returned a draft that lacks them (host repositories store the
	// whole Draft row and may ignore this re-save).
	if dd, ok := s.repo.(interface {
		SetDraftDigests(ctx context.Context, workflowID, etag, defDigest, artDigest string) error
	}); ok {
		if err := dd.SetDraftDigests(ctx, workflowID, d.ETag, defDigest, artDigest); err != nil {
			return Draft{}, err
		}
	}
	return d, nil
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
