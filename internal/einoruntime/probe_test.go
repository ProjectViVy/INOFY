// Package einoruntime_test pins the Eino compose behaviors the INOFY
// compiler depends on (architecture §7.1, gate G2). These probes record
// observed v0.9.13 behavior; they introduce no public INOFY type.
package einoruntime_test

import (
	"context"
	"encoding/json"
	"os"
	"runtime/debug"
	"sort"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/compose"
)

const pinnedEino = "v0.9.13"

type graphCase struct {
	Name              string         `json:"name"`
	Input             map[string]any `json:"input"`
	ExpectExecuted    []string       `json:"expect_executed"`
	ExpectNotExecuted []string       `json:"expect_not_executed"`
	ExpectSelSource   string         `json:"expect_sel_source"`
	ExpectMixFromP1   bool           `json:"expect_mix_has_from_p1"`
}

type graphCasesFile struct {
	Description string      `json:"description"`
	Cases       []graphCase `json:"cases"`
}

func loadG2Cases(t *testing.T) []graphCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/graph_cases.json")
	if err != nil {
		t.Fatalf("read graph_cases.json: %v", err)
	}
	var f graphCasesFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse graph_cases.json: %v", err)
	}
	if len(f.Cases) == 0 {
		t.Fatal("graph_cases.json carries no cases")
	}
	return f.Cases
}

func TestEinoModuleVersionPinned(t *testing.T) {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("build info unavailable")
	}
	for _, dep := range bi.Deps {
		if dep.Path == "github.com/cloudwego/eino" {
			if dep.Version != pinnedEino {
				t.Fatalf("eino version %s, want %s", dep.Version, pinnedEino)
			}
			return
		}
	}
	t.Fatal("eino not found in build info")
}

type g2Probe struct {
	executed map[string]*atomic.Int64
	joinSaw  *atomic.Bool // both AND inputs present
	selSrcs  *atomic.Value
	mixIn    *atomic.Value
	rootHit  *atomic.Bool // gated node actually received non-triggering root data
}

func newG2Probe(nodeKeys ...string) *g2Probe {
	p := &g2Probe{
		executed: make(map[string]*atomic.Int64, len(nodeKeys)),
		joinSaw:  &atomic.Bool{},
		selSrcs:  &atomic.Value{},
		mixIn:    &atomic.Value{},
		rootHit:  &atomic.Bool{},
	}
	for _, k := range nodeKeys {
		p.executed[k] = &atomic.Int64{}
	}
	return p
}

func (p *g2Probe) counter(key string) *atomic.Int64 {
	return p.executed[key]
}

func (p *g2Probe) node(key string, fn func(in map[string]any) map[string]any) *compose.Lambda {
	return compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		p.counter(key).Add(1)
		if fn == nil {
			return in, nil
		}
		return fn(in), nil
	})
}

// buildG2Workflow compiles the fixed probe topology described in
// testdata/graph_cases.json.
func buildG2Workflow(ctx context.Context, p *g2Probe) (compose.Runnable[map[string]any, map[string]any], error) {
	wf := compose.NewWorkflow[map[string]any, map[string]any]()

	wf.AddLambdaNode("a", p.node("a", nil)).AddInput(compose.START)
	wf.AddLambdaNode("b", p.node("b", nil)).AddInput(compose.START)
	wf.AddLambdaNode("c", p.node("c", nil)).AddInput(compose.START)

	// Parallel AND: join must not run before both a and b complete.
	wf.AddLambdaNode("join", p.node("join", func(in map[string]any) map[string]any {
		if _, okA := in["a"]; !okA {
			return map[string]any{"error": "missing a"}
		}
		if _, okB := in["b"]; !okB {
			return map[string]any{"error": "missing b"}
		}
		p.joinSaw.Store(true)
		return in
	})).AddInput("a", compose.ToField("a")).AddInput("b", compose.ToField("b"))

	// Switch: emits the chosen port. Root port value arrives through a
	// non-triggering data mapping; activation still comes from join.
	wf.AddLambdaNode("sw", p.node("sw", func(in map[string]any) map[string]any {
		port, _ := in["port"].(string)
		return map[string]any{"port": port}
	})).AddInput("join", compose.ToField("join")).
		AddInputWithOptions(compose.START,
			[]*compose.FieldMapping{compose.MapFields("port", "port")},
			compose.WithNoDirectDependency())

	// Port p1 region: p1a -> p1b. p1a also reads root payload without a
	// dependency edge — activation comes only from the branch.
	wf.AddLambdaNode("p1a", p.node("p1a", func(in map[string]any) map[string]any {
		if _, ok := in["payload"]; ok {
			p.rootHit.Store(true)
		}
		return in
	})).AddInputWithOptions(compose.START,
		[]*compose.FieldMapping{compose.MapFields("payload", "payload")},
		compose.WithNoDirectDependency())
	wf.AddLambdaNode("p1b", p.node("p1b", nil)).AddInput("p1a")

	// Port p2 region (also the default).
	wf.AddLambdaNode("p2a", p.node("p2a", nil)).AddInputWithOptions("sw",
		[]*compose.FieldMapping{compose.MapFields("port", "port")},
		compose.WithNoDirectDependency())

	wf.AddBranch("sw", compose.NewGraphMultiBranch(
		func(ctx context.Context, in map[string]any) (map[string]bool, error) {
			switch in["port"] {
			case "p1":
				return map[string]bool{"p1a": true}, nil
			default:
				return map[string]bool{"p2a": true}, nil
			}
		},
		map[string]bool{"p1a": true, "p2a": true}))

	// Select convergence: present predecessor packets only; the inactive
	// region must contribute nothing.
	wf.AddLambdaNode("sel", p.node("sel", func(in map[string]any) map[string]any {
		srcs := []string{}
		if v, ok := in["p1"]; ok && v != nil {
			srcs = append(srcs, "p1")
		}
		if v, ok := in["p2"]; ok && v != nil {
			srcs = append(srcs, "p2")
		}
		sort.Strings(srcs)
		p.selSrcs.Store(srcs)
		if len(srcs) == 1 {
			return map[string]any{"sel": srcs[0]}
		}
		return map[string]any{"sel_error": srcs}
	})).AddInput("p1b", compose.ToField("p1")).AddInput("p2a", compose.ToField("p2"))

	// mix illegally ANDs a region node with an unconditional node: G2
	// documents that a skipped control predecessor does not block firing.
	wf.AddLambdaNode("mix", p.node("mix", func(in map[string]any) map[string]any {
		p.mixIn.Store(in)
		return in
	})).AddInput("p1b", compose.ToField("from_p1")).AddInput("c", compose.ToField("c"))

	// All active exits settle before success: exits are sel and a.
	wf.End().AddInput("sel", compose.ToField("sel")).AddInput("a", compose.ToField("a"))

	return wf.Compile(ctx)
}

func TestEinoWorkflowG2(t *testing.T) {
	ctx := context.Background()
	for _, tc := range loadG2Cases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			p := newG2Probe("a", "b", "c", "join", "sw", "p1a", "p1b", "p2a", "sel", "mix")
			runner, err := buildG2Workflow(ctx, p)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}
			out, err := runner.Invoke(ctx, tc.Input)
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}

			// c and mix are not END-ancestral: observed on v0.9.13 they race
			// run settlement and may or may not execute. Drop them from the
			// executed-set comparison; their conditional invariants are
			// asserted below.
			gotExec := []string{}
			for k, c := range p.executed {
				if c.Load() > 0 && k != "c" && k != "mix" {
					gotExec = append(gotExec, k)
				}
			}
			sort.Strings(gotExec)
			wantExec := append([]string{}, tc.ExpectExecuted...)
			sort.Strings(wantExec)
			if !equalSet(gotExec, wantExec) {
				t.Fatalf("executed %v, want %v", gotExec, wantExec)
			}
			for _, k := range tc.ExpectNotExecuted {
				if p.executed[k] != nil && p.executed[k].Load() > 0 {
					t.Fatalf("inactive-region node %s executed", k)
				}
			}

			if !p.joinSaw.Load() {
				t.Fatal("AND join ran without both predecessors")
			}

			sel, _ := out["sel"].(map[string]any)
			if sel == nil {
				t.Fatalf("output missing settled exit sel: %v", out)
			}
			if sel["sel"] != tc.ExpectSelSource {
				t.Fatalf("select got %v, want single candidate %s", sel, tc.ExpectSelSource)
			}
			if _, ok := out["a"]; !ok {
				t.Fatal("unconditional exit a missing from output")
			}

			switch tc.ExpectSelSource {
			case "p1":
				if !p.rootHit.Load() {
					t.Fatal("gated node ran without receiving non-triggering root data")
				}
			}

			// mix has no path to END: observed on v0.9.13 it races run
			// settlement and may or may not execute. When it does run, a
			// skipped predecessor contributes nothing — never data, never a
			// block — which is why the compiler must reject mixed-region ANDs.
			if p.counter("mix").Load() > 0 {
				mi, _ := p.mixIn.Load().(map[string]any)
				_, hasP1 := mi["from_p1"]
				if hasP1 != tc.ExpectMixFromP1 {
					t.Fatalf("mix from_p1 present=%v, want %v (skipped predecessor data leaked/blocked)", hasP1, tc.ExpectMixFromP1)
				}
			}
		})
	}
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
