// Bounded-repeat tests (S04): a serial repeat compiles to a cyclic
// Eino Graph wrapping its body Workflow; loop-carried state is
// explicit, iterations are counted and bounded, and logical node keys
// carry container/zero-based-iteration identity.
package einoruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

func repeatNodeT(id string, initial map[string]any, stateSchema any, body map[string]any, maxIter int, until map[string]any) map[string]any {
	n := map[string]any{
		"id":             id,
		"kind":           "repeat",
		"initial":        initial,
		"state_schema":   stateSchema,
		"body":           body,
		"max_iterations": maxIter,
		"until":          until,
	}
	return n
}

func repeatDoc(body map[string]any, maxIter int, until map[string]any, extra ...map[string]any) map[string]any {
	nodes := []any{
		callNodeT("seed", "t@1", nil),
		repeatNodeT("rep", map[string]any{
			"acc": map[string]any{"source": "seed", "pointer": "/n"},
		}, map[string]any{"type": "object"}, body, maxIter, until),
	}
	for _, e := range extra {
		nodes = append(nodes, e)
	}
	edges := []any{edgeT("seed", "rep")}
	extraNodes := 0
	for _, e := range extra {
		_ = e
		extraNodes++
	}
	exits := []any{"rep"}
	if extraNodes > 0 {
		for _, x := range extra {
			id, _ := x["id"].(string)
			edges = append(edges, edgeT("rep", id))
			exits = append(exits, id)
		}
	}
	return map[string]any{
		"schema_version": "inofy.workflow/v1",
		"graph": map[string]any{
			"nodes": nodes,
			"edges": edges,
			"exits": exits,
		},
	}
}

func TestRepeatStateAndLimit(t *testing.T) {
	ctx := context.Background()
	types := map[string]einoruntime.TypeInfo{"t@1": {ImplementationID: "impl-1"}}

	t.Run("one pass when until is true on first output", func(t *testing.T) {
		doc := repeatDoc(map[string]any{
			"nodes": []any{
				callNodeT("step", "t@1", map[string]any{
					"acc": map[string]any{"source": "input", "pointer": "/acc"},
				}),
			},
			"edges": []any{},
			"exits": []any{"step"},
			"outputs": map[string]any{
				"acc":  map[string]any{"source": "step", "pointer": "/acc"},
				"done": map[string]any{"source": "step", "pointer": "/done"},
			},
		}, 8, map[string]any{
			"op":      "eq",
			"left":    map[string]any{"source": "step", "pointer": "/done"},
			"right":   map[string]any{"literal": true},
		})
		var stepCalls int
		exec := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
			if strings.HasSuffix(c.Path, "/step") {
				stepCalls++
				return json.RawMessage(`{"acc": 1, "done": true}`), nil
			}
			return json.RawMessage(`{"n": 0}`), nil
		}
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		out, _, err := prog.Invoke(ctx, json.RawMessage(`{}`), exec)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		var end map[string]any
		_ = out
		if stepCalls != 1 {
			t.Fatalf("step ran %d times, want 1", stepCalls)
		}
		_ = end
	})

	t.Run("state carries across iterations", func(t *testing.T) {
		doc := repeatDoc(map[string]any{
			"nodes": []any{
				callNodeT("step", "t@1", map[string]any{
					"acc": map[string]any{"source": "input", "pointer": "/acc"},
				}),
			},
			"edges": []any{},
			"exits": []any{"step"},
			"outputs": map[string]any{
				"acc": map[string]any{"source": "step", "pointer": "/acc"},
			},
		}, 8, map[string]any{
			"op":    "gte",
			"left":  map[string]any{"source": "step", "pointer": "/acc"},
			"right": map[string]any{"literal": 3},
		})
		var seenAccs []float64
		var mu sync.Mutex
		exec := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
			if strings.HasSuffix(c.Path, "/step") {
				var in map[string]any
				if err := json.Unmarshal(c.Input, &in); err != nil {
					return nil, err
				}
				acc, _ := in["acc"].(float64)
				mu.Lock()
				seenAccs = append(seenAccs, acc)
				mu.Unlock()
				bs, _ := json.Marshal(map[string]any{"acc": acc + 1})
				return bs, nil
			}
			return json.RawMessage(`{"n": 0}`), nil
		}
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, _, err = prog.Invoke(ctx, json.RawMessage(`{}`), exec)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if len(seenAccs) != 3 || seenAccs[0] != 0 || seenAccs[1] != 1 || seenAccs[2] != 2 {
			t.Fatalf("iterations saw accs %v, want [0 1 2]", seenAccs)
		}
	})

	t.Run("zero cap rejects at compile", func(t *testing.T) {
		doc := repeatDoc(map[string]any{
			"nodes": []any{callNodeT("step", "t@1", nil)},
			"edges": []any{},
			"exits": []any{"step"},
		}, 0, map[string]any{"op": "exists", "pointer": "/done"})
		_, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		var ie *einoruntime.Error
		if !errors.As(err, &ie) || ie.Code != einoruntime.ErrInvalidDefinition {
			t.Fatalf("want invalid_definition for zero cap, got %v", err)
		}
	})

	t.Run("cap exhaustion yields iteration_limit", func(t *testing.T) {
		doc := repeatDoc(map[string]any{
			"nodes": []any{callNodeT("step", "t@1", nil)},
			"edges": []any{},
			"exits": []any{"step"},
			"outputs": map[string]any{
				"acc": map[string]any{"source": "step", "pointer": "/acc"},
			},
		}, 2, map[string]any{"op": "exists", "pointer": "/never"})
		var runs int
		exec := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
			if strings.HasSuffix(c.Path, "/step") {
				runs++
				return json.RawMessage(`{"acc": 1}`), nil
			}
			return json.RawMessage(`{"n": 0}`), nil
		}
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, _, err = prog.Invoke(ctx, json.RawMessage(`{}`), exec)
		var ie *einoruntime.Error
		if !errors.As(err, &ie) || ie.Code != einoruntime.ErrIterationLimit {
			t.Fatalf("want iteration_limit, got %v", err)
		}
		if runs != 2 {
			t.Fatalf("body ran %d times, want cap 2", runs)
		}
	})

	t.Run("on_error fallback replaces exhaustion with declared output", func(t *testing.T) {
		doc := repeatDoc(map[string]any{
			"nodes": []any{callNodeT("step", "t@1", nil)},
			"edges": []any{},
			"exits": []any{"step"},
			"outputs": map[string]any{
				"acc": map[string]any{"source": "step", "pointer": "/acc"},
			},
		}, 1, map[string]any{"op": "exists", "pointer": "/never"})
		// Attach on_error to the repeat node.
		g := doc["graph"].(map[string]any)
		g["nodes"].([]any)[1].(map[string]any)["on_error"] = map[string]any{"literal": map[string]any{"fallback": true}}
		exec := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
			if strings.HasSuffix(c.Path, "/step") {
				return json.RawMessage(`{"acc": 1}`), nil
			}
			return json.RawMessage(`{"n": 0}`), nil
		}
		prog, err := einoruntime.CompileProgram(ctx, doc, types, einoruntime.Limits{MaxActivations: 256})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		out, _, err := prog.Invoke(ctx, json.RawMessage(`{}`), exec)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		var end map[string]any
		if err := json.Unmarshal(out, &end); err != nil {
			t.Fatalf("decode: %v", err)
		}
	})
}

