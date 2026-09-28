// Package consumer_test exercises INOFY the way an external Go module does:
// pure descriptor construction, catalog freezing and an honest
// unsupported_feature from the not-yet-implemented compiler. No App, SQL or
// HTTP initialization may be required to reach this point (G6).
package consumer_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ProjectViVy/inofy"
)

func TestExternalConsumerCompilesWithoutApp(t *testing.T) {
	catalog, err := inofy.NewCatalog([]inofy.NodeDescriptor{{
		TypeID:           "inofy.value@1",
		ImplementationID: "inofy-app-value-impl-1",
	}})
	if err != nil {
		t.Fatalf("NewCatalog: %v", err)
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

	_, _, err = inofy.Compile(context.Background(), def, catalog, inofy.CompileOptions{})
	if err == nil {
		t.Fatal("expected an error until the S03 compiler lands")
	}
	var ierr *inofy.Error
	if !errors.As(err, &ierr) {
		t.Fatalf("expected *inofy.Error, got %T", err)
	}
	if ierr.Code != inofy.ErrUnsupportedFeature {
		t.Fatalf("expected unsupported_feature, got %q", ierr.Code)
	}
}
