// Package consumer_test exercises INOFY the way an external Go module
// does: pure descriptor construction, catalog freezing, compile and
// one full library-facade run (G6). No App, SQL or HTTP initialization
// may be required to reach this point.
package consumer_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ProjectViVy/inofy"
)

type echoExec struct{}

func (echoExec) Execute(ctx context.Context, call inofy.NodeCall) (inofy.NodeReply, error) {
	return inofy.NodeReply{Output: json.RawMessage(`{"echo":"ok"}`)}, nil
}

func TestExternalConsumerRunsLibrary(t *testing.T) {
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
			Outputs: map[string]inofy.Binding{
				"echo": {Source: "echo", Pointer: "/echo"},
			},
		},
	}

	prog, diags, err := inofy.Compile(context.Background(), def, catalog, inofy.CompileOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("unexpected diagnostics: %#v", diags)
	}

	res, err := prog.Run(context.Background(), inofy.RunRequest{
		Ref:   inofy.ExecutionRef{RunID: "consumer-1", ProgramDigest: prog.Meta().ProgramDigest},
		Input: json.RawMessage(`{}`),
	}, inofy.Bindings{Nodes: echoExec{}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Status != inofy.RunSucceeded {
		t.Fatalf("status = %s", res.Status)
	}
}
