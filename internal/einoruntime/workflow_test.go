// Package einoruntime_test program-level tests (architecture §7.2,
// gate G2): admitted DAG scopes compile to an Eino workflow and run
// once through the runtime facade.
package einoruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

// fakeExec records calls in order and replies from a canned map keyed
// by call Path.
type fakeExec struct {
	mu        *sync.Mutex
	calls     []string
	replies   map[string]json.RawMessage
	failCalls map[string]error
}

func newFakeExec(replies map[string]json.RawMessage) *fakeExec {
	return &fakeExec{mu: &sync.Mutex{}, replies: replies}
}

// nodeID returns the last segment of a spec path, which is the node ID.
func nodeID(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (f *fakeExec) execute(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
	id := nodeID(c.Path)
	f.mu.Lock()
	f.calls = append(f.calls, id)
	f.mu.Unlock()
	if f.failCalls != nil {
		if err, ok := f.failCalls[id]; ok {
			return nil, err
		}
	}
	out, ok := f.replies[id]
	if !ok {
		return json.RawMessage(`{"ok":true}`), nil
	}
	return out, nil
}

func (f *fakeExec) called(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == path {
			return true
		}
	}
	return false
}

func callNodeT(id, typ string, inputs map[string]any) map[string]any {
	n := map[string]any{"id": id, "kind": "call", "type": typ}
	if inputs != nil {
		n["inputs"] = inputs
	}
	return n
}

func edgeT(from, to string) map[string]any {
	return map[string]any{"from": from, "to": to}
}

func TestProgramDAG(t *testing.T) {
	ctx := context.Background()
	types := map[string]einoruntime.TypeInfo{
		"t@1": {ImplementationID: "impl-1"},
	}

	t.Run("parallel_and_aggregate", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					callNodeT("a", "t@1", map[string]any{
						"q": map[string]any{"source": "input", "pointer": "/q"},
					}),
					callNodeT("b", "t@1", nil),
					callNodeT("agg", "t@1", map[string]any{
						"va": map[string]any{"source": "a", "pointer": "/va"},
						"vb": map[string]any{"source": "b", "pointer": "/vb"},
					}),
				},
				"edges": []any{
					edgeT("a", "agg"),
					edgeT("b", "agg"),
				},
				"exits": []any{"agg"},
				"outputs": map[string]any{
					"answer": map[string]any{"source": "agg", "pointer": "/sum"},
				},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"a":   json.RawMessage(`{"va": 3}`),
			"b":   json.RawMessage(`{"vb": 4}`),
			"agg": json.RawMessage(`{"sum": 7}`),
		})
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		out, _, err := invoke3(prog, ctx, json.RawMessage(`{"q":"hi"}`), exec.execute)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		var answer map[string]any
		if err := json.Unmarshal(out, &answer); err != nil {
			t.Fatalf("outputs decode: %v", err)
		}
		if answer["answer"] != float64(7) {
			t.Fatalf("answer = %v, want 7", answer["answer"])
		}
		for _, want := range []string{"a", "b", "agg"} {
			if !exec.called(want) {
				t.Fatalf("node %s was not executed", want)
			}
		}
	})

	t.Run("agg input resolved from predecessors", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					callNodeT("a", "t@1", nil),
					callNodeT("b", "t@1", nil),
					callNodeT("agg", "t@1", map[string]any{
						"va": map[string]any{"source": "a", "pointer": "/va"},
						"vb": map[string]any{"source": "b", "pointer": "/vb"},
					}),
				},
				"edges": []any{edgeT("a", "agg"), edgeT("b", "agg")},
				"exits": []any{"agg"},
			},
		}
		var seen map[string]any
		exec := newFakeExec(map[string]json.RawMessage{
			"a": json.RawMessage(`{"va": 3}`),
			"b": json.RawMessage(`{"vb": 4}`),
		})
		inner := exec.execute
		execCap := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
			if nodeID(c.Path) == "agg" {
				var m map[string]any
				if err := json.Unmarshal(c.Input, &m); err == nil {
					seen = m
				}
			}
			return inner(ctx, c)
		}
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if _, _, err := invoke3(prog, ctx, json.RawMessage(`{}`), execCap); err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if seen["va"] != float64(3) || seen["vb"] != float64(4) {
			t.Fatalf("agg input = %v, want va=3 vb=4", seen)
		}
	})

	t.Run("missing pointer is binding_missing", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					callNodeT("a", "t@1", nil),
					callNodeT("cons", "t@1", map[string]any{
						"v": map[string]any{"source": "a", "pointer": "/nope"},
					}),
				},
				"edges": []any{edgeT("a", "cons")},
				"exits": []any{"cons"},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"a": json.RawMessage(`{"va": 3}`),
		})
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, _, err = invoke3(prog, ctx, json.RawMessage(`{}`), exec.execute)
		var ie *einoruntime.Error
		if !errors.As(err, &ie) || ie.Code != einoruntime.ErrBindingMissing {
			t.Fatalf("want binding_missing, got %v", err)
		}
	})

	t.Run("second exit effect settles before success", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					callNodeT("a", "t@1", nil),
					callNodeT("agg", "t@1", map[string]any{
						"v": map[string]any{"source": "a", "pointer": "/va"},
					}),
					callNodeT("fx", "t@1", nil),
				},
				"edges": []any{edgeT("a", "agg"), edgeT("a", "fx"), edgeT("agg", "fx")},
				"exits": []any{"agg", "fx"},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"a": json.RawMessage(`{"va": 1}`),
		})
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if _, _, err := invoke3(prog, ctx, json.RawMessage(`{}`), exec.execute); err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if !exec.called("fx") {
			t.Fatal("side-effect exit fx was abandoned before success")
		}
	})

	t.Run("concurrent runs isolate inputs", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					callNodeT("a", "t@1", map[string]any{
						"q": map[string]any{"source": "input", "pointer": "/q"},
					}),
				},
				"edges": []any{},
				"exits": []any{"a"},
				"outputs": map[string]any{
					"echo": map[string]any{"source": "a", "pointer": "/echo"},
				},
			},
		}
		var gotA, gotB atomic.Value
		exec := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
			var in map[string]any
			if err := json.Unmarshal(c.Input, &in); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"echo": in["q"]})
		}
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		var wg sync.WaitGroup
		var errA, errB error
		wg.Add(2)
		go func() {
			defer wg.Done()
			var out json.RawMessage
			out, _, errA = invoke3(prog, ctx, json.RawMessage(`{"q":"A"}`), exec)
			if errA == nil {
				var m map[string]any
				_ = json.Unmarshal(out, &m)
				gotA.Store(m["echo"])
			}
		}()
		go func() {
			defer wg.Done()
			var out json.RawMessage
			out, _, errB = invoke3(prog, ctx, json.RawMessage(`{"q":"B"}`), exec)
			if errB == nil {
				var m map[string]any
				_ = json.Unmarshal(out, &m)
				gotB.Store(m["echo"])
			}
		}()
		wg.Wait()
		if errA != nil || errB != nil {
			t.Fatalf("runs failed: %v %v", errA, errB)
		}
		if gotA.Load() != "A" || gotB.Load() != "B" {
			t.Fatalf("inputs not isolated: %v %v", gotA.Load(), gotB.Load())
		}
	})

	t.Run("nested repeat beyond one level rejected", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					map[string]any{
						"id": "rep", "kind": "repeat",
						"initial":      map[string]any{"acc": map[string]any{"literal": 0}},
						"state_schema": map[string]any{"type": "object"},
						"body": map[string]any{
							"nodes": []any{
								map[string]any{
									"id": "rep2", "kind": "repeat",
									"initial":      map[string]any{"x": map[string]any{"literal": 0}},
									"state_schema": map[string]any{"type": "object"},
									"body": map[string]any{
										"nodes": []any{callNodeT("s", "t@1", nil)},
										"edges": []any{},
										"exits": []any{"s"},
									},
									"max_iterations": 2,
									"until":          map[string]any{"op": "exists", "pointer": "/x"},
								},
							},
							"edges": []any{},
							"exits": []any{"rep2"},
						},
						"max_iterations": 2,
						"until":          map[string]any{"op": "exists", "pointer": "/acc"},
					},
				},
				"edges": []any{},
				"exits": []any{"rep"},
			},
		}
		_, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		var ie *einoruntime.Error
		if !errors.As(err, &ie) || ie.Code != einoruntime.ErrInvalidDefinition {
			t.Fatalf("want invalid_definition for nested repeat, got %v", err)
		}
	})
}
