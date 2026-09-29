package definitions_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/definitions"
)

// --- S07 task 2: immutable publication --------------------------------

func catalogWithImpl(impl string) inofy.Catalog {
	c, err := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.value@1", ImplementationID: impl},
	})
	if err != nil {
		panic(err)
	}
	return c
}

func TestPublishIdentity(t *testing.T) {
	ctx := context.Background()

	t.Run("republishing same artifact returns existing revision", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		r1, err := svc.Publish(ctx, "wf", d.ETag, catalogWithImpl("impl-a"))
		if err != nil {
			t.Fatalf("publish: %v", err)
		}
		if r1.Revision != 1 {
			t.Fatalf("first revision = %d", r1.Revision)
		}
		// Draft unchanged → same artifact: no new row.
		d2, _ := repo.GetDraft(ctx, "wf")
		r2, err := svc.Publish(ctx, "wf", d2.ETag, catalogWithImpl("impl-a"))
		if err != nil {
			t.Fatalf("republish: %v", err)
		}
		if r2.Revision != 1 {
			t.Fatalf("republish allocated revision %d", r2.Revision)
		}
	})

	t.Run("changed catalog impl requires new publication", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		r1, _ := svc.Publish(ctx, "wf", d.ETag, catalogWithImpl("impl-a"))
		// The draft did not change, but the frozen catalog did — the
		// used identity differs, so this is a NEW revision.
		d2, _ := repo.GetDraft(ctx, "wf")
		r2, err := svc.Publish(ctx, "wf", d2.ETag, catalogWithImpl("impl-b"))
		if err != nil {
			t.Fatalf("republish under new catalog: %v", err)
		}
		if r2.Revision != 2 {
			t.Fatalf("changed catalog did not allocate new revision: %d", r2.Revision)
		}
		if r1.UsedCatalogDigest == r2.UsedCatalogDigest {
			t.Fatal("revisions share used-catalog digest across different impls")
		}
	})

	t.Run("dual publish CAS: one winner", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		// Two editors hold the same ETag; publish consumes it, so only
		// one can allocate revision 2. Make the draft diverge first.
		var wins, conflicts atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				a := artifact("v1")
				a.Presentation.Title = "variant"
				a.Definition.Graph.Nodes[0].Config = json.RawMessage(`{"v":` + string(rune('0'+i)) + `}`)
				// Each editor saves its own draft under the SAME etag;
				// the loser must fail the save CAS — publish CAS is the
				// second line of defense and must also produce exactly
				// one winner.
				dd, err := svc.SaveDraft(ctx, "wf", d.ETag, a)
				if err != nil {
					conflicts.Add(1)
					return
				}
				if _, err := svc.Publish(ctx, "wf", dd.ETag, catalogWithImpl("impl-a")); err != nil {
					conflicts.Add(1)
					return
				}
				wins.Add(1)
			}(i)
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("dual publish winners=%d conflicts=%d", wins.Load(), conflicts.Load())
		}
	})

	t.Run("archived workflow: revision stays readable, no new publish", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		r1, _ := svc.Publish(ctx, "wf", d.ETag, catalogWithImpl("impl-a"))
		if err := repo.Archive(ctx, "wf"); err != nil {
			t.Fatalf("archive: %v", err)
		}
		got, err := svc.GetRevision(ctx, "wf", r1.Revision)
		if err != nil || got.Revision != 1 {
			t.Fatalf("archived revision unreadable: %v", err)
		}
		d2, _ := repo.GetDraft(ctx, "wf")
		if _, err := svc.Publish(ctx, "wf", d2.ETag, catalogWithImpl("impl-a")); err == nil {
			t.Fatal("publish after archive accepted")
		}
	})

	t.Run("published revision is immutable", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		r1, _ := svc.Publish(ctx, "wf", d.ETag, catalogWithImpl("impl-a"))
		// Mutate the draft afterwards; the revision must not move.
		if _, err := svc.SaveDraft(ctx, "wf", d.ETag, artifact("v2-changed")); err != nil {
			t.Fatalf("post-publish save: %v", err)
		}
		got, _ := svc.GetRevision(ctx, "wf", r1.Revision)
		if got.Artifact.Presentation.Title != "v1" {
			t.Fatalf("revision mutated: %q", got.Artifact.Presentation.Title)
		}
	})
}
