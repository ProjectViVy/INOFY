// Package definitions implements the reusable draft and publication
// service (architecture §4.4, plan S07): one mutable draft per
// workflow guarded by ETag CAS, immutable monotone published
// revisions, and a small transactional repository contract that host
// storage implements. Storage authority stays with each host.
package definitions

import (
	"context"
	"encoding/json"
	"sync"

	inofy "github.com/ProjectViVy/inofy"
)

// ETagAbsent creates a workflow draft only when none exists; an empty
// expected ETag on update means "any current draft" — CAS still
// compares it against the stored tag.
const ETagAbsent = "\x00absent"

// Draft is the mutable editing surface for a workflow.
type Draft struct {
	WorkflowID       string         `json:"workflow_id"`
	ETag             string         `json:"etag"`
	Artifact         inofy.Artifact `json:"artifact"`
	DefinitionDigest string         `json:"definition_digest"`
	ArtifactDigest   string         `json:"artifact_digest"`
	Archived         bool           `json:"archived"`
}

// Revision is an immutable published artifact bound to the exact
// used catalog identity at publication time.
type Revision struct {
	WorkflowID         string            `json:"workflow_id"`
	Revision           uint64            `json:"revision"`
	Artifact           inofy.Artifact    `json:"artifact"`
	DefinitionDigest   string            `json:"definition_digest"`
	ArtifactDigest     string            `json:"artifact_digest"`
	UsedCatalogDigest  string            `json:"used_catalog_digest"`
	UsedImplementations map[string]string `json:"used_implementations"`
}

// Page is a bounded listing result.
type Page struct {
	Revisions  []Revision `json:"revisions"`
	NextCursor string     `json:"next_cursor,omitempty"`
}

// Repository is the host storage contract: every method is one
// atomic transaction. The library supplies no DDL; hosts must
// implement the same CAS/allocation semantics.
type Repository interface {
	// GetDraft returns the current draft or ErrRevisionConflict-typed
	// not-found when the workflow does not exist.
	GetDraft(ctx context.Context, workflowID string) (Draft, error)
	// UpdateDraftCAS atomically stores the artifact when expectedETag
	// matches the stored ETag (ETagAbsent creates); it returns the
	// durable draft with a fresh ETag, or a revision_conflict error.
	UpdateDraftCAS(ctx context.Context, workflowID, expectedETag string, a inofy.Artifact) (Draft, error)
	// PublishCAS atomically validates expectedETag and allocates the
	// next monotone revision, deduplicating by artifact digest under
	// the same workflow: republishing the identical artifact returns
	// the existing revision without allocating a new one.
	PublishCAS(ctx context.Context, workflowID, expectedETag string, rev Revision) (Revision, error)
	// GetRevision reads an immutable published revision.
	GetRevision(ctx context.Context, workflowID string, revision uint64) (Revision, error)
	// List returns a bounded page of published revisions ordered by
	// (workflow_id, revision) after cursor.
	List(ctx context.Context, cursor string, limit int) (Page, error)
}

// MemoryRepository is an in-memory reference implementation for tests
// and ephemeral hosts — NOT a production authority (S07 plan).
type MemoryRepository struct {
	mu      sync.Mutex
	drafts  map[string]*memDraft
	revisions map[string]map[uint64]Revision
	order   []Revision // (workflow,revision) insertion order
	nextETag uint64
}

type memDraft struct {
	Draft
}

// NewMemoryRepository returns an empty in-memory repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		drafts:    map[string]*memDraft{},
		revisions: map[string]map[uint64]Revision{},
	}
}

func (m *MemoryRepository) GetDraft(_ context.Context, workflowID string) (Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[workflowID]
	if !ok {
		return Draft{}, notFound(workflowID)
	}
	return d.Draft, nil
}

func (m *MemoryRepository) UpdateDraftCAS(_ context.Context, workflowID, expectedETag string, a inofy.Artifact) (Draft, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[workflowID]
	switch {
	case expectedETag == ETagAbsent && ok:
		return Draft{}, conflict(workflowID, "draft already exists")
	case expectedETag != ETagAbsent && !ok:
		return Draft{}, notFound(workflowID)
	case expectedETag != ETagAbsent && d.ETag != expectedETag:
		return Draft{}, conflict(workflowID, "stale etag")
	}
	m.nextETag++
	etag := etagOf(workflowID, m.nextETag)
	// Caller recomputes digests; the repository stores what the
	// service already validated and digested.
	nd := Draft{WorkflowID: workflowID, ETag: etag, Artifact: a}
	m.drafts[workflowID] = &memDraft{Draft: nd}
	return nd, nil
}

func (m *MemoryRepository) PublishCAS(_ context.Context, workflowID, expectedETag string, rev Revision) (Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.drafts[workflowID]
	if !ok {
		return Revision{}, notFound(workflowID)
	}
	if d.Archived {
		return Revision{}, conflict(workflowID, "archived workflow cannot publish")
	}
	if d.ETag != expectedETag {
		return Revision{}, conflict(workflowID, "stale etag")
	}
	revs := m.revisions[workflowID]
	// Same-artifact republication is idempotent: return the existing
	// revision instead of allocating.
	for _, r := range revs {
		if r.ArtifactDigest == rev.ArtifactDigest &&
			r.UsedCatalogDigest == rev.UsedCatalogDigest {
			return r, nil
		}
	}
	var n uint64
	for k := range revs {
		if k > n {
			n = k
		}
	}
	n++
	rev.Revision = n
	if revs == nil {
		revs = map[uint64]Revision{}
		m.revisions[workflowID] = revs
	}
	revs[n] = rev
	m.order = append(m.order, rev)
	return rev, nil
}

func (m *MemoryRepository) GetRevision(_ context.Context, workflowID string, revision uint64) (Revision, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.revisions[workflowID][revision]
	if !ok {
		return Revision{}, notFound(workflowID)
	}
	return r, nil
}

func (m *MemoryRepository) List(_ context.Context, cursor string, limit int) (Page, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	start := 0
	if cursor != "" {
		for i, r := range m.order {
			if cursorOf(r) == cursor {
				start = i + 1
				break
			}
		}
	}
	if limit <= 0 {
		limit = 50
	}
	page := Page{}
	for i := start; i < len(m.order) && len(page.Revisions) < limit; i++ {
		page.Revisions = append(page.Revisions, m.order[i])
	}
	if start+len(page.Revisions) < len(m.order) && len(page.Revisions) > 0 {
		page.NextCursor = cursorOf(page.Revisions[len(page.Revisions)-1])
	}
	return page, nil
}

func etagOf(workflowID string, n uint64) string {
	return workflowID + ":" + itoa(n)
}

func cursorOf(r Revision) string {
	return r.WorkflowID + ":" + itoa(r.Revision)
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func conflict(id, msg string) error {
	return &inofy.Error{Code: inofy.ErrRevisionConflict, Path: id, Message: msg}
}

func notFound(id string) error {
	return &inofy.Error{Code: inofy.ErrRevisionConflict, Path: id, Message: "workflow not found"}
}

var _ = json.RawMessage{}
