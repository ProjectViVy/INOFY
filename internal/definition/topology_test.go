// Package definition_test graph admission tests (architecture §4.1,
// §5.2, §7.4). A definition is admitted only as a bounded structured
// graph: small, acyclic per scope, fully connected to its exits, with
// verified disjoint branch regions.
package definition_test

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/internal/definition"
)

var testLimits = definition.LimitsRef{
	MaxNodes:           64,
	MaxEdges:           128,
	MaxRepeatNesting:   1,
	MaxIterations:      8,
	MaxActivations:     256,
	MaxAttemptsPerCall: 3,
	MaxPredicateDepth:  8,
	Parallelism:        4,
}

func callNode(id string) map[string]any {
	return map[string]any{"id": id, "kind": "call", "type": "inofy.value@1"}
}

func graphDoc(nodes []any, edges []any, exits []any) map[string]any {
	return map[string]any{
		"graph": map[string]any{
			"nodes": nodes,
			"edges": edges,
			"exits": exits,
		},
	}
}

func graphOf(doc map[string]any) map[string]any {
	return doc["graph"].(map[string]any)
}

func addNode(doc map[string]any, n map[string]any) {
	g := graphOf(doc)
	g["nodes"] = append(g["nodes"].([]any), n)
}

func addEdge(doc map[string]any, e map[string]any) {
	g := graphOf(doc)
	g["edges"] = append(g["edges"].([]any), e)
}

func edge(from, to string) map[string]any {
	return map[string]any{"from": from, "to": to}
}

// chainDoc builds a linear call chain n1 -> n2 -> ... with the last
// node as the exit.
func chainDoc(ids ...string) map[string]any {
	var nodes, edges []any
	for i, id := range ids {
		nodes = append(nodes, callNode(id))
		if i > 0 {
			edges = append(edges, edge(ids[i-1], id))
		}
	}
	return graphDoc(nodes, edges, []any{ids[len(ids)-1]})
}

func admit(t *testing.T, doc map[string]any) []definition.Finding {
	t.Helper()
	return definition.ValidateTopologyDoc(doc, testLimits)
}

func wantCode(t *testing.T, findings []definition.Finding, code string) {
	t.Helper()
	for _, f := range findings {
		if f.Code == code {
			return
		}
	}
	t.Fatalf("missing finding code %q in %#v", code, findings)
}

func noFindings(t *testing.T, findings []definition.Finding) {
	t.Helper()
	if len(findings) != 0 {
		t.Fatalf("expected admission, got %#v", findings)
	}
}

func TestGraphAdmission(t *testing.T) {
	t.Run("valid chain", func(t *testing.T) {
		noFindings(t, admit(t, chainDoc("a", "b", "c")))
	})

	t.Run("sixty fifth node nested", func(t *testing.T) {
		// 63 outer nodes + repeat with 2 body nodes = 65 > 64.
		var ids []string
		for i := 0; i < 62; i++ {
			ids = append(ids, "n"+strconv.Itoa(i))
		}
		doc := chainDoc(append(ids, "rep")...)
		nodes := doc["graph"].(map[string]any)["nodes"].([]any)
		nodes[len(nodes)-1] = map[string]any{
			"id": "rep", "kind": "repeat",
			"initial":      map[string]any{"acc": map[string]any{"literal": 0}},
			"state_schema": map[string]any{"type": "object"},
			"body": map[string]any{
				"nodes": []any{callNode("s1"), callNode("s2")},
				"edges": []any{edge("s1", "s2")},
				"exits": []any{"s2"},
			},
			"max_iterations": 2,
			"until":          map[string]any{"op": "exists", "pointer": "/acc"},
		}
		wantCode(t, admit(t, doc), "too_many_nodes")
	})

	t.Run("one hundred twenty ninth edge", func(t *testing.T) {
		// 64 nodes in a chain (63 edges) plus parallel duplicate edges
		// to stay inside the node bound while exceeding max_edges.
		var ids []string
		for i := 0; i < 64; i++ {
			ids = append(ids, "n"+strconv.Itoa(i))
		}
		doc := chainDoc(ids...)
		g := graphOf(doc)
		edges := g["edges"].([]any)
		for i := 0; i < 65; i++ {
			edges = append(edges, edge("n0", "n1"))
		}
		g["edges"] = edges // 63 + 65 = 128 edges exactly: legal
		noFindings(t, admit(t, doc))
		addEdge(doc, edge("n0", "n1"))
		wantCode(t, admit(t, doc), "too_many_edges")
	})

	t.Run("nonexit dead end", func(t *testing.T) {
		doc := chainDoc("a", "b")
		addNode(doc, callNode("dead"))
		addEdge(doc, edge("a", "dead"))
		wantCode(t, admit(t, doc), "nonexit_dead_end")
	})

	t.Run("unreachable node", func(t *testing.T) {
		doc := chainDoc("a", "b")
		// y and z form a cycle feeding w; w is outside the cycle but
		// cannot be reached from any root.
		addNode(doc, callNode("y"))
		addNode(doc, callNode("z"))
		addNode(doc, callNode("w"))
		addEdge(doc, edge("y", "z"))
		addEdge(doc, edge("z", "y"))
		addEdge(doc, edge("z", "w"))
		addEdge(doc, edge("w", "b"))
		wantCode(t, admit(t, doc), "unreachable_node")
	})

	t.Run("cycle outside repeat", func(t *testing.T) {
		doc := chainDoc("a", "b", "c")
		addEdge(doc, edge("c", "a"))
		wantCode(t, admit(t, doc), "cycle_outside_repeat")
	})

	t.Run("missing direct data edge", func(t *testing.T) {
		doc := chainDoc("a", "b")
		addNode(doc, map[string]any{
			"id": "cons", "kind": "call", "type": "inofy.value@1",
			"inputs": map[string]any{"v": map[string]any{"source": "a", "pointer": "/out"}},
		})
		addEdge(doc, edge("cons", "b")) // consumer runs but no a->cons edge
		wantCode(t, admit(t, doc), "missing_direct_edge")
	})

	t.Run("root input binding needs no edge", func(t *testing.T) {
		doc := chainDoc("a", "b")
		addNode(doc, map[string]any{
			"id": "cons", "kind": "call", "type": "inofy.value@1",
			"inputs": map[string]any{"v": map[string]any{"source": "input", "pointer": "/q"}},
		})
		addEdge(doc, edge("a", "cons"))
		addEdge(doc, edge("cons", "b"))
		noFindings(t, admit(t, doc))
	})

	t.Run("overlapping branch regions", func(t *testing.T) {
		doc := branchDoc()
		// Cross-region edge: "have" region node feeds the "none" region,
		// putting n1 in both regions.
		addEdge(doc, edge("h1", "n1"))
		wantCode(t, admit(t, doc), "region_overlap")
	})

	t.Run("crossing branch region", func(t *testing.T) {
		doc := branchDoc()
		// An edge into the select that does not come from a region of
		// its switch crosses the region boundary.
		addNode(doc, callNode("other"))
		g := graphOf(doc)
		g["exits"] = append(g["exits"].([]any), "other")
		addEdge(doc, edge("other", "sel"))
		fs := admit(t, doc)
		wantCode(t, fs, "region_crossing")
		// Diagnostic must name the offending edge endpoints.
		for _, f := range fs {
			if f.Code == "region_crossing" && f.Message == "" {
				t.Fatal("region_crossing must describe the offending edge")
			}
		}
	})

	t.Run("outside entry into region", func(t *testing.T) {
		doc := branchDoc()
		g := doc["graph"].(map[string]any)
		// "other" is a second exit; it does not dominate sw, so an edge
		// from it into the have region is an illegal region entry.
		g["nodes"] = append(g["nodes"].([]any), callNode("other"))
		g["exits"] = append(g["exits"].([]any), "other")
		g["edges"] = append(g["edges"].([]any), edge("other", "h1"))
		wantCode(t, admit(t, doc), "region_entry_illegal")
	})

	t.Run("dominating ancestor may enter region", func(t *testing.T) {
		doc := branchDoc()
		// "in" is the only route to sw, so it strictly dominates sw; a
		// direct in -> h1 edge supplies ancestor data legally.
		g := doc["graph"].(map[string]any)
		g["edges"] = append(g["edges"].([]any), edge("in", "h1"))
		noFindings(t, admit(t, doc))
	})

	t.Run("region bypasses join", func(t *testing.T) {
		doc := branchDoc()
		g := doc["graph"].(map[string]any)
		// sink is reached from the have region but cannot reach sel.
		g["nodes"] = append(g["nodes"].([]any), callNode("sink"))
		g["exits"] = append(g["exits"].([]any), "sink")
		g["edges"] = append(g["edges"].([]any), edge("h1", "sink"))
		wantCode(t, admit(t, doc), "region_not_joined")
	})

	t.Run("valid nested branch", func(t *testing.T) {
		doc := nestedBranchDoc()
		noFindings(t, admit(t, doc))
	})

	t.Run("missing port target", func(t *testing.T) {
		doc := branchDoc()
		edges := doc["graph"].(map[string]any)["edges"].([]any)
		// Drop the default-port edge: port "none" now has no target.
		kept := edges[:0]
		for _, e := range edges {
			m := e.(map[string]any)
			if m["port"] == "none" {
				continue
			}
			kept = append(kept, e)
		}
		doc["graph"].(map[string]any)["edges"] = kept
		wantCode(t, admit(t, doc), "missing_port_target")
	})

	t.Run("activation bound exceeded", func(t *testing.T) {
		// 40 call nodes with retry 3 inside and outside a repeat.
		var ids []string
		for i := 0; i < 40; i++ {
			ids = append(ids, "n"+strconv.Itoa(i))
		}
		doc := chainDoc(append(ids, "rep", "tail")...)
		nodes := doc["graph"].(map[string]any)["nodes"].([]any)
		retry := map[string]any{"max_attempts": 3, "delay_ms": 10}
		for _, nv := range nodes {
			nv.(map[string]any)["retry"] = retry
		}
		nodes[len(nodes)-2] = map[string]any{
			"id": "rep", "kind": "repeat",
			"initial":      map[string]any{"acc": map[string]any{"literal": 0}},
			"state_schema": map[string]any{"type": "object"},
			"body": map[string]any{
				"nodes": []any{
					map[string]any{"id": "s1", "kind": "call", "type": "inofy.value@1", "retry": retry},
					map[string]any{"id": "s2", "kind": "call", "type": "inofy.value@1", "retry": retry},
				},
				"edges": []any{edge("s1", "s2")},
				"exits": []any{"s2"},
			},
			"max_iterations": 8,
			"until":          map[string]any{"op": "exists", "pointer": "/acc"},
		}
		// 41 outer nodes * 3 attempts = 123; body: 2*3*8=48; total 171
		// stays under 256 — lower the limit to force the bound to bite.
		lim := testLimits
		lim.MaxActivations = 128
		wantCode(t, definition.ValidateTopologyDoc(doc, lim), "activation_bound")
	})
}

// TestValidateDefinitionTopology checks the structural findings reach
// the public diagnostic adapter.
func TestValidateDefinitionTopology(t *testing.T) {
	catalog, err := inofy.NewCatalog([]inofy.NodeDescriptor{{
		TypeID:           "inofy.value@1",
		ImplementationID: "impl-1",
	}})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	env := func(graph map[string]any) map[string]any {
		return map[string]any{
			"definition": map[string]any{
				"schema_version": "inofy.workflow/v1",
				"graph":          graph["graph"],
			},
		}
	}
	raw, err := json.Marshal(env(chainDoc("a", "b")))
	if err != nil {
		t.Fatal(err)
	}
	art, diags, err := inofy.DecodeArtifact(raw)
	if err != nil || len(diags) != 0 {
		t.Fatalf("decode: %v %#v", err, diags)
	}
	if got := inofy.ValidateDefinition(art.Definition, catalog, inofy.CompileOptions{}); len(got) != 0 {
		t.Fatalf("clean graph produced diagnostics %#v", got)
	}

	bad := chainDoc("a", "b")
	g := bad["graph"].(map[string]any)
	g["edges"] = append(g["edges"].([]any), edge("b", "a"))
	raw, _ = json.Marshal(env(bad))
	art, diags, err = inofy.DecodeArtifact(raw)
	if err != nil || len(diags) != 0 {
		t.Fatalf("decode: %v %#v", err, diags)
	}
	got := inofy.ValidateDefinition(art.Definition, catalog, inofy.CompileOptions{})
	if !hasDiag(got, inofy.Diagnostic{Check: inofy.CheckTopology, Code: "cycle_outside_repeat"}) {
		t.Fatalf("expected cycle diagnostic, got %#v", got)
	}
}

// branchDoc is the canonical legal branch: sw -> {have: h1, none: n1}
// both converging on select sel, which is the sole exit.
func branchDoc() map[string]any {
	return graphDoc(
		[]any{
			callNode("in"),
			map[string]any{
				"id": "sw", "kind": "switch",
				"inputs":       map[string]any{"v": map[string]any{"source": "in", "pointer": "/v"}},
				"cases":        []any{map[string]any{"port": "have", "when": map[string]any{"op": "exists", "pointer": "/v"}}},
				"default_port": "none",
				"join":         "sel",
			},
			callNode("h1"), callNode("n1"),
			map[string]any{
				"id": "sel", "kind": "select", "switch": "sw",
				"candidates": []any{
					map[string]any{"source": "h1", "pointer": "/out"},
					map[string]any{"source": "n1", "pointer": "/out"},
				},
			},
		},
		[]any{
			edge("in", "sw"),
			map[string]any{"from": "sw", "to": "h1", "port": "have"},
			map[string]any{"from": "sw", "to": "n1", "port": "none"},
			edge("h1", "sel"), edge("n1", "sel"),
		},
		[]any{"sel"},
	)
}

// nestedBranchDoc places a second switch inside the "have" region; its
// own join sits inside the same region, keeping nesting proper.
func nestedBranchDoc() map[string]any {
	doc := branchDoc()
	g := doc["graph"].(map[string]any)
	nodes := g["nodes"].([]any)
	edges := g["edges"].([]any)

	// Replace the h1 call with: sw2 inside the have region. Region
	// membership: h2a (port p2a), h2b (default p2b), sel2 (join).
	nodes = append(nodes,
		map[string]any{
			"id": "sw2", "kind": "switch",
			"inputs":       map[string]any{"v": map[string]any{"source": "h1", "pointer": "/v"}},
			"cases":        []any{map[string]any{"port": "p2a", "when": map[string]any{"op": "exists", "pointer": "/v"}}},
			"default_port": "p2b",
			"join":         "sel2",
		},
		callNode("h2a"), callNode("h2b"),
		map[string]any{
			"id": "sel2", "kind": "select", "switch": "sw2",
			"candidates": []any{
				map[string]any{"source": "h2a", "pointer": "/out"},
				map[string]any{"source": "h2b", "pointer": "/out"},
			},
		},
	)
	// h1 -> sw2 stays inside the have region; sel2 rejoins sel.
	edges = append(edges,
		edge("h1", "sw2"),
		map[string]any{"from": "sw2", "to": "h2a", "port": "p2a"},
		map[string]any{"from": "sw2", "to": "h2b", "port": "p2b"},
		edge("h2a", "sel2"), edge("h2b", "sel2"),
		edge("sel2", "sel"),
	)
	g["nodes"] = nodes
	g["edges"] = edges
	return doc
}
