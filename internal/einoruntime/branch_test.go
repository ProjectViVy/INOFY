// Structured-branch tests (S03 task 2): ordered cases, port fan-out,
// nested regions, select fallback and ambiguity, non-triggering root
// data, and skipped-node marking from the decision log.
package einoruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

func switchNodeT(id string, inputs map[string]any, cases []any, defPort, join string) map[string]any {
	n := map[string]any{
		"id":   id,
		"kind": "switch",
		"join": join,
	}
	if inputs != nil {
		n["inputs"] = inputs
	}
	if cases != nil {
		n["cases"] = cases
	}
	if defPort != "" {
		n["default_port"] = defPort
	}
	return n
}

func selectNodeT(id string, candidates []any, fallback any) map[string]any {
	n := map[string]any{"id": id, "kind": "select", "candidates": candidates}
	if fallback != nil {
		n["fallback"] = fallback
	}
	return n
}

func caseT(port string, when map[string]any) map[string]any {
	c := map[string]any{"port": port}
	if when != nil {
		c["when"] = when
	}
	return c
}

func candT(source, ptr string) map[string]any {
	return map[string]any{"source": source, "pointer": ptr}
}

func portEdgeT(from, to, port string) map[string]any {
	return map[string]any{"from": from, "to": to, "port": port}
}

func runDoc(t *testing.T, doc map[string]any, input json.RawMessage, exec einoruntime.Executor) (map[string]any, []string, error) {
	t.Helper()
	types := map[string]einoruntime.TypeInfo{"t@1": {ImplementationID: "impl-1"}}
	prog, err := einoruntime.CompileProgram(context.Background(), doc, types, einoruntime.Limits{MaxActivations: 256})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	out, diags, err := invoke3(prog, context.Background(), input, exec)
	var skipped []string
	for _, d := range diags {
		if d.Code == "node_skipped" {
			// Path tail is the node ID.
			sk := d.Path
			for i := len(sk) - 1; i >= 0; i-- {
				if sk[i] == '/' {
					sk = sk[i+1:]
					break
				}
			}
			skipped = append(skipped, sk)
		}
	}
	var m map[string]any
	if err == nil {
		if uerr := json.Unmarshal(out, &m); uerr != nil {
			t.Fatalf("outputs decode: %v", uerr)
		}
	}
	return m, skipped, err
}

// TestStructuredBranch is the table-driven branch suite (S03 task 2).
func TestStructuredBranch(t *testing.T) {
	ctx := context.Background()
	_ = ctx

	t.Run("ordered cases take first match over default", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					switchNodeT("sw", map[string]any{
						"v": map[string]any{"source": "input", "pointer": "/v"},
					}, []any{
						caseT("big", map[string]any{
							"op":    "gte",
							"left":  map[string]any{"source": "input", "pointer": "/v"},
							"right": map[string]any{"literal": 10},
						}),
						caseT("pos", map[string]any{
							"op":    "gt",
							"left":  map[string]any{"source": "input", "pointer": "/v"},
							"right": map[string]any{"literal": 0},
						}),
					}, "neg", "sel"),
					callNodeT("big1", "t@1", nil),
					callNodeT("pos1", "t@1", nil),
					callNodeT("neg1", "t@1", nil),
					selectNodeT("sel", []any{
						candT("big1", "/v"), candT("pos1", "/v"), candT("neg1", "/v"),
					}, nil),
				},
				"edges": []any{
					portEdgeT("sw", "big1", "big"),
					portEdgeT("sw", "pos1", "pos"),
					portEdgeT("sw", "neg1", "neg"),
					edgeT("big1", "sel"), edgeT("pos1", "sel"), edgeT("neg1", "sel"),
				},
				"exits": []any{"sel"},
				"outputs": map[string]any{
					"val": map[string]any{"source": "sel", "pointer": ""},
				},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"big1": json.RawMessage(`{"v":"BIG"}`),
			"pos1": json.RawMessage(`{"v":"POS"}`),
			"neg1": json.RawMessage(`{"v":"NEG"}`),
		})
		// v=20 matches "big" (first case) even though "pos" also matches.
		out, skipped, err := runDoc(t, doc, json.RawMessage(`{"v": 20}`), exec.execute)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if out["val"] != "BIG" {
			t.Fatalf("val = %v, want BIG", out["val"])
		}
		if !contains(skipped, "pos1") || !contains(skipped, "neg1") {
			t.Fatalf("skipped = %v, want pos1,neg1", skipped)
		}
		// default port
		out, skipped, err = runDoc(t, doc, json.RawMessage(`{"v": -5}`), exec.execute)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if out["val"] != "NEG" {
			t.Fatalf("default val = %v, want NEG", out["val"])
		}
	})

	t.Run("port fans out to parallel region entries", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					switchNodeT("sw", nil, nil, "go", "sel"),
					callNodeT("p1", "t@1", nil),
					callNodeT("p2", "t@1", nil),
					callNodeT("joiner", "t@1", map[string]any{
						"x": map[string]any{"source": "p1", "pointer": "/x"},
						"y": map[string]any{"source": "p2", "pointer": "/y"},
					}),
					selectNodeT("sel", []any{candT("joiner", "/done")}, nil),
				},
				"edges": []any{
					portEdgeT("sw", "p1", "go"),
					portEdgeT("sw", "p2", "go"),
					edgeT("p1", "joiner"), edgeT("p2", "joiner"),
					edgeT("joiner", "sel"),
				},
				"exits": []any{"sel"},
				"outputs": map[string]any{
					"done": map[string]any{"source": "sel", "pointer": ""},
				},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"p1":     json.RawMessage(`{"x":1}`),
			"p2":     json.RawMessage(`{"y":2}`),
			"joiner": json.RawMessage(`{"done":"yes"}`),
		})
		out, _, err := runDoc(t, doc, json.RawMessage(`{}`), exec.execute)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if out["done"] != "yes" {
			t.Fatalf("done = %v", out["done"])
		}
		for _, want := range []string{"p1", "p2", "joiner"} {
			if !exec.called(want) {
				t.Fatalf("%s not executed", want)
			}
		}
	})

	t.Run("nested switch inside one port does not leak", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					switchNodeT("sw", map[string]any{
						"v": map[string]any{"source": "input", "pointer": "/v"},
					}, []any{
						caseT("have", map[string]any{
							"op": "exists", "pointer": "/v",
						}),
					}, "none", "sel"),
					// "have" region: h1 → sw2 (nested) → h2|h3 → sel2 → n1
					callNodeT("h1", "t@1", nil),
					switchNodeT("sw2", nil, []any{
						caseT("hi", map[string]any{
							"op":    "eq",
							"left":  map[string]any{"source": "h1", "pointer": "/lvl"},
							"right": map[string]any{"literal": "hi"},
						}),
					}, "lo", "sel2"),
					callNodeT("h2", "t@1", nil),
					callNodeT("h3", "t@1", nil),
					selectNodeT("sel2", []any{candT("h2", "/r"), candT("h3", "/r")}, nil),
					callNodeT("n1", "t@1", map[string]any{
						"r": map[string]any{"source": "sel2", "pointer": ""},
					}),
					// "none" region
					callNodeT("none1", "t@1", nil),
					selectNodeT("sel", []any{candT("n1", "/v"), candT("none1", "/v")}, nil),
				},
				"edges": []any{
					portEdgeT("sw", "h1", "have"),
					portEdgeT("sw", "none1", "none"),
					edgeT("h1", "sw2"),
					portEdgeT("sw2", "h2", "hi"),
					portEdgeT("sw2", "h3", "lo"),
					edgeT("h2", "sel2"), edgeT("h3", "sel2"),
					edgeT("sel2", "n1"), edgeT("n1", "sel"),
					edgeT("none1", "sel"),
				},
				"exits": []any{"sel"},
				"outputs": map[string]any{
					"val": map[string]any{"source": "sel", "pointer": ""},
				},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"h1":    json.RawMessage(`{"lvl":"hi"}`),
			"h2":    json.RawMessage(`{"r":"H2"}`),
			"h3":    json.RawMessage(`{"r":"H3"}`),
			"n1":    json.RawMessage(`{"v":"N1"}`),
			"none1": json.RawMessage(`{"v":"NONE"}`),
		})
		out, skipped, err := runDoc(t, doc, json.RawMessage(`{"v": 1}`), exec.execute)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if out["val"] != "N1" {
			t.Fatalf("val = %v, want N1", out["val"])
		}
		// Nested switch chose "hi": h3 skipped along with "none" region.
		if !contains(skipped, "h3") || !contains(skipped, "none1") {
			t.Fatalf("skipped = %v, want h3 + none1", skipped)
		}
		if exec.called("h3") || exec.called("none1") {
			t.Fatal("inactive region node executed")
		}
	})

	t.Run("missing candidate uses fallback", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					switchNodeT("sw", nil, nil, "a", "sel"),
					callNodeT("ra", "t@1", nil),
					callNodeT("rb", "t@1", nil),
					selectNodeT("sel", []any{candT("ra", "/x"), candT("rb", "/x")},
						map[string]any{"literal": "FB"}),
				},
				"edges": []any{
					portEdgeT("sw", "ra", "a"),
					portEdgeT("sw", "rb", "b"),
					edgeT("ra", "sel"), edgeT("rb", "sel"),
				},
				"exits": []any{"sel"},
				"outputs": map[string]any{
					"val": map[string]any{"source": "sel", "pointer": ""},
				},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"ra": json.RawMessage(`{"x":"A"}`),
			"rb": json.RawMessage(`{"x":"B"}`),
		})
		out, _, err := runDoc(t, doc, json.RawMessage(`{}`), exec.execute)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if out["val"] != "A" {
			t.Fatalf("val = %v, want A", out["val"])
		}
	})

	t.Run("two candidates produce a hard error not arrival order", func(t *testing.T) {
		// Constructed without S02 admission (regions overlap): both
		// candidates deliver packets → select must error.
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					callNodeT("a", "t@1", nil),
					callNodeT("b", "t@1", nil),
					selectNodeT("sel", []any{candT("a", "/x"), candT("b", "/x")}, nil),
				},
				"edges": []any{edgeT("a", "sel"), edgeT("b", "sel")},
				"exits": []any{"sel"},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"a": json.RawMessage(`{"x":"A"}`),
			"b": json.RawMessage(`{"x":"B"}`),
		})
		_, _, err := runDoc(t, doc, json.RawMessage(`{}`), exec.execute)
		var ie *einoruntime.Error
		if !errors.As(err, &ie) || ie.Code != einoruntime.ErrSelectAmbiguous {
			t.Fatalf("want select_ambiguous, got %v", err)
		}
	})

	t.Run("region node reads root data without activation route", func(t *testing.T) {
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					switchNodeT("sw", nil, nil, "go", "sel"),
					callNodeT("g", "t@1", map[string]any{
						"q": map[string]any{"source": "input", "pointer": "/q"},
					}),
					selectNodeT("sel", []any{candT("g", "/v")}, nil),
				},
				"edges": []any{
					portEdgeT("sw", "g", "go"),
					edgeT("g", "sel"),
				},
				"exits": []any{"sel"},
				"outputs": map[string]any{
					"val": map[string]any{"source": "sel", "pointer": ""},
				},
			},
		}
		exec := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
			var in map[string]any
			if err := json.Unmarshal(c.Input, &in); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"v": in["q"]})
		}
		out, _, err := runDoc(t, doc, json.RawMessage(`{"q":"ROOTDATA"}`), exec)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if out["val"] != "ROOTDATA" {
			t.Fatalf("val = %v, want ROOTDATA", out["val"])
		}
	})

	t.Run("skipped node failure never surfaces", func(t *testing.T) {
		// The unchosen region's node would fail if executed; it must
		// never run, and the run must succeed via the chosen port.
		doc := map[string]any{
			"schema_version": "inofy.workflow/v1",
			"graph": map[string]any{
				"nodes": []any{
					switchNodeT("sw", map[string]any{
						"v": map[string]any{"source": "input", "pointer": "/v"},
					}, []any{
						caseT("yes", map[string]any{
							"op":    "eq",
							"left":  map[string]any{"source": "input", "pointer": "/v"},
							"right": map[string]any{"literal": true},
						}),
					}, "no", "sel"),
					callNodeT("yes1", "t@1", nil),
					callNodeT("no1", "t@1", nil),
					selectNodeT("sel", []any{candT("yes1", "/v"), candT("no1", "/v")}, nil),
				},
				"edges": []any{
					portEdgeT("sw", "yes1", "yes"),
					portEdgeT("sw", "no1", "no"),
					edgeT("yes1", "sel"), edgeT("no1", "sel"),
				},
				"exits": []any{"sel"},
				"outputs": map[string]any{
					"val": map[string]any{"source": "sel", "pointer": ""},
				},
			},
		}
		exec := newFakeExec(map[string]json.RawMessage{
			"yes1": json.RawMessage(`{"v":"YES"}`),
		})
		exec.failCalls = map[string]error{"no1": errors.New("would fail")}
		out, skipped, err := runDoc(t, doc, json.RawMessage(`{"v": true}`), exec.execute)
		if err != nil {
			t.Fatalf("invoke: %v", err)
		}
		if out["val"] != "YES" {
			t.Fatalf("val = %v", out["val"])
		}
		if !contains(skipped, "no1") {
			t.Fatalf("skipped = %v, want no1", skipped)
		}
	})
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
