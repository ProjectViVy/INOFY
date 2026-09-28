package inofy_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
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

// --- S03 Task 1: public Compile + Program.Run ---

// stubExec replies to every node call with a fixed JSON object.
type stubExec struct {
	mu    sync.Mutex
	calls []inofy.NodeCall
	reply func(inofy.NodeCall) inofy.NodeReply
}

func (s *stubExec) Execute(ctx context.Context, call inofy.NodeCall) (inofy.NodeReply, error) {
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
	return s.reply(call), nil
}

// lastPathID returns the node ID (last path segment).
func lastPathID(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (s *stubExec) executed(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if lastPathID(c.Path) == path {
			return true
		}
	}
	return false
}

func compileTestProgram(t *testing.T, def inofy.Definition) *inofy.Program {
	t.Helper()
	catalog, err := inofy.NewCatalog([]inofy.NodeDescriptor{{
		TypeID:           "inofy.value@1",
		ImplementationID: "impl-1",
	}})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	prog, diags, err := inofy.Compile(context.Background(), def, catalog, inofy.CompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("compile diagnostics: %#v", diags)
	}
	if prog == nil {
		t.Fatal("compile returned no program and no diagnostics")
	}
	return prog
}

func runRequest(prog *inofy.Program, input json.RawMessage) inofy.RunRequest {
	return inofy.RunRequest{
		Ref:   inofy.ExecutionRef{RunID: "run-1", ProgramDigest: prog.Meta().ProgramDigest},
		Input: input,
	}
}

func TestProgramDAGPublic(t *testing.T) {
	ctx := context.Background()
	def := inofy.Definition{
		SchemaVersion: inofy.SchemaVersionV1,
		Graph: inofy.Graph{
			Nodes: []inofy.Node{
				{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
					Inputs: map[string]inofy.Binding{"q": {Source: "input", Pointer: "/q"}}},
				{ID: "b", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
				{ID: "agg", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
					Inputs: map[string]inofy.Binding{
						"va": {Source: "a", Pointer: "/va"},
						"vb": {Source: "b", Pointer: "/vb"},
					}},
			},
			Edges: []inofy.Edge{{From: "a", To: "agg"}, {From: "b", To: "agg"}},
			Exits: []string{"agg"},
			Outputs: map[string]inofy.Binding{
				"answer": {Source: "agg", Pointer: "/sum"},
			},
		},
	}
	prog := compileTestProgram(t, def)

	if prog.Meta().ProgramDigest == "" || prog.Meta().EinoBuild == "" {
		t.Fatalf("program meta incomplete: %#v", prog.Meta())
	}

	exec := &stubExec{reply: func(c inofy.NodeCall) inofy.NodeReply {
		switch lastPathID(c.Path) {
		case "a":
			return inofy.NodeReply{Output: json.RawMessage(`{"va": 3}`)}
		case "b":
			return inofy.NodeReply{Output: json.RawMessage(`{"vb": 4}`)}
		default:
			return inofy.NodeReply{Output: json.RawMessage(`{"sum": 7}`)}
		}
	}}
	res, err := prog.Run(ctx, runRequest(prog, json.RawMessage(`{"q":"hi"}`)), inofy.Bindings{Nodes: exec})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != inofy.RunSucceeded {
		t.Fatalf("status = %s", res.Status)
	}
	var out map[string]any
	if err := json.Unmarshal(res.Outputs, &out); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	if out["answer"] != float64(7) {
		t.Fatalf("answer = %v", out["answer"])
	}
	for _, p := range []string{"a", "b", "agg"} {
		if !exec.executed(p) {
			t.Fatalf("%s not executed", p)
		}
	}

	// Mismatched digest is rejected before any work.
	bad := runRequest(prog, json.RawMessage(`{}`))
	bad.Ref.ProgramDigest = "bogus"
	res, err = prog.Run(ctx, bad, inofy.Bindings{Nodes: exec})
	if err == nil || res.Status != inofy.RunFailed {
		t.Fatalf("mismatched digest run got %v", res.Status)
	}
}
