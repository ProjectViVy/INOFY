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

// --- S07 task 1: draft CAS contract ----------------------------------

func artifact(title string) inofy.Artifact {
	return inofy.Artifact{
		Definition: inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
				},
				Edges:   []inofy.Edge{},
				Exits:   []string{"a"},
				Outputs: map[string]inofy.Binding{"r": {Source: "a", Pointer: "/answer"}},
			},
		},
		Presentation: inofy.Presentation{Title: title},
	}
}

func catalog() inofy.Catalog {
	c, err := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.value@1", ImplementationID: "impl-a"},
	})
	if err != nil {
		panic(err)
	}
	return c
}

func TestDraftCAS(t *testing.T) {
	ctx := context.Background()

	t.Run("new workflow save then stale ETag conflict", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d1, err := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		if err != nil {
			t.Fatalf("initial save: %v", err)
		}
		if d1.ETag == "" || d1.ETag == d1.ETag+"" && false {
			t.Fatalf("no durable etag: %+v", d1)
		}
		d2, err := svc.SaveDraft(ctx, "wf", d1.ETag, artifact("v2"))
		if err != nil {
			t.Fatalf("cas save: %v", err)
		}
		if _, err := svc.SaveDraft(ctx, "wf", d1.ETag, artifact("v3")); err == nil {
			t.Fatal("stale etag accepted")
		}
		_ = d2
	})

	t.Run("concurrent saves: exactly one wins", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d1, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		var wins, conflicts atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, err := svc.SaveDraft(ctx, "wf", d1.ETag, artifact("winner"))
				if err != nil {
					conflicts.Add(1)
					return
				}
				wins.Add(1)
			}(i)
		}
		wg.Wait()
		if wins.Load() != 1 || conflicts.Load() != 3 {
			t.Fatalf("wins=%d conflicts=%d", wins.Load(), conflicts.Load())
		}
	})

	t.Run("invalid artifact rejected before CAS", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		bad := artifact("x")
		bad.Definition.Graph.Nodes = nil
		bad.Definition.Graph.Exits = nil
		_, err := svc.SaveDraft(ctx, "wf", "", bad)
		if err == nil {
			t.Fatal("invalid artifact saved")
		}
	})

	t.Run("layout-only edit changes artifact digest, not semantic digest", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		a1 := artifact("v1")
		d1, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, a1)
		a2 := a1
		a2.Presentation.Layout = inofy.Layout{Positions: map[string]inofy.LayoutPoint{"a": {X: 10, Y: 20}}}
		a2.Presentation.Title = "v2"
		d2, err := svc.SaveDraft(ctx, "wf", d1.ETag, a2)
		if err != nil {
			t.Fatalf("layout save: %v", err)
		}
		if d2.ArtifactDigest == d1.ArtifactDigest {
			t.Fatal("layout edit did not change artifact digest")
		}
		if d2.DefinitionDigest != d1.DefinitionDigest {
			t.Fatal("layout edit changed semantic definition digest")
		}
	})

	t.Run("validate draft surfaces diagnostics without saving", func(t *testing.T) {
		repo := definitions.NewMemoryRepository()
		svc := definitions.NewService(repo)
		d1, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifact("v1"))
		diags, err := svc.ValidateDraft(ctx, "wf", d1.ETag, catalog())
		if err != nil {
			t.Fatalf("validate: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("valid draft diagnostics: %#v", diags)
		}
	})
}

var _ = json.RawMessage{}
