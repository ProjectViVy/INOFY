package inofy_test

import (
	"context"
	"testing"

	"github.com/ProjectViVy/inofy"
)

func TestZeroValueCatalogRejected(t *testing.T) {
	var catalog inofy.Catalog // zero value, never built via NewCatalog

	if _, ok := catalog.Lookup("inofy.value@1"); ok {
		t.Fatal("zero-value catalog resolved a descriptor")
	}

	def := inofy.Definition{
		SchemaVersion: inofy.SchemaVersionV1,
		Graph: inofy.Graph{
			Nodes: []inofy.Node{
				{ID: "echo", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
			},
			Exits: []string{"echo"},
		},
	}
	if _, _, err := inofy.Compile(context.Background(), def, catalog, inofy.CompileOptions{}); err == nil {
		t.Fatal("Compile accepted a zero-value catalog")
	}
}

func TestNewCatalogRejectsMissingIDs(t *testing.T) {
	if _, err := inofy.NewCatalog([]inofy.NodeDescriptor{{ImplementationID: "impl-1"}}); err == nil {
		t.Fatal("descriptor without type ID accepted")
	}
	if _, err := inofy.NewCatalog([]inofy.NodeDescriptor{{TypeID: "inofy.value@1"}}); err == nil {
		t.Fatal("descriptor without implementation ID accepted")
	}
}

func TestNewCatalogFreezesAndDeduplicates(t *testing.T) {
	catalog, err := inofy.NewCatalog([]inofy.NodeDescriptor{{
		TypeID:           "inofy.value@1",
		ImplementationID: "impl-1",
	}})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
	}
	if _, ok := catalog.Lookup("inofy.value@1"); !ok {
		t.Fatal("registered type missing")
	}

	if _, err := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.value@1", ImplementationID: "impl-1"},
		{TypeID: "inofy.value@1", ImplementationID: "impl-2"},
	}); err == nil {
		t.Fatal("duplicate type ID accepted")
	}
}
