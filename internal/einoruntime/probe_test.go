// Package einoruntime_test pins the Eino compose behaviors the INOFY
// compiler depends on (architecture §7.1, gate G2). These probes record
// observed v0.9.13 behavior; they introduce no public INOFY type.
package einoruntime_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
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
	// Some Go test binaries omit dependency modules from their build info.
	// Verify the resolved module graph rather than treating that omission as
	// a version mismatch.
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/cloudwego/eino").Output()
	if err != nil {
		t.Fatalf("resolve Eino module: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != pinnedEino {
		t.Fatalf("eino version %s, want %s", got, pinnedEino)
	}
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

// --- Task 3: repeat + interrupt probes (gates G3/G4) ---
//
// Pins the Eino behaviors the compiler's repeat container and wait
// continuation depend on: a cyclic plain Graph carrying serializable local
// state, an embedded branch Workflow as the loop body, addressed
// StatefulInterrupt waits, checkpoint staging/reopen, BatchResumeWithData,
// and a hard step bound.

type probeLoopState struct {
	Iter int
}

func init() {
	schema.RegisterName[*probeLoopState]("inofy_probe_loop_state")
}

// stagingCheckPointStore mirrors the contract §8.3 staging semantics at
// probe level: Set buffers a snapshot per checkpoint ID; Get reads it back.
type stagingCheckPointStore struct {
	mu   sync.Mutex
	data map[string][]byte
	sets int
}

func (s *stagingCheckPointStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data[id]
	return b, ok, nil
}

func (s *stagingCheckPointStore) Set(_ context.Context, id string, cp []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		s.data = map[string][]byte{}
	}
	s.data[id] = append([]byte(nil), cp...)
	s.sets++
	return nil
}

func (s *stagingCheckPointStore) snapshot(id string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data[id]
}

type g34Probe struct {
	bodyRuns           *atomic.Int64
	waits              map[string]*atomic.Int64
	interruptStateSeen *atomic.Int64
	answerJoin         *atomic.Value
}

func newG34Probe() *g34Probe {
	return &g34Probe{
		bodyRuns:           &atomic.Int64{},
		waits:              map[string]*atomic.Int64{"waitA": {}, "waitB": {}},
		interruptStateSeen: &atomic.Int64{},
		answerJoin:         &atomic.Value{},
	}
}

// buildG34Body is the loop body: an inner Workflow whose branch activates
// either two simultaneous wait nodes (arm=true) or a fast node. joinW
// converges whichever region fired.
func buildG34Body(p *g34Probe) *compose.Workflow[map[string]any, map[string]any] {
	bf := compose.NewWorkflow[map[string]any, map[string]any]()

	bf.AddLambdaNode("sw", compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return in, nil
	})).AddInput(compose.START)

	wait := func(name string) *compose.Lambda {
		return compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
			p.waits[name].Add(1)
			if isResume, _, data := compose.GetResumeContext[string](ctx); isResume {
				return map[string]any{"node": name, "answer": data}, nil
			}
			// Re-executed without being the resume target: Eino requires
			// re-interrupting to preserve the wait. GetInterruptState must
			// report the persisted state here.
			if wasInterrupted, hasState, _ := compose.GetInterruptState[map[string]any](ctx); wasInterrupted {
				p.interruptStateSeen.Add(1)
				if !hasState {
					return nil, fmt.Errorf("%s rerun without persisted interrupt state", name)
				}
			}
			return nil, compose.StatefulInterrupt(ctx,
				map[string]any{"kind": "wait", "node": name},
				map[string]any{"armed": true})
		})
	}
	bf.AddLambdaNode("waitA", wait("waitA")).AddInputWithOptions("sw",
		[]*compose.FieldMapping{}, compose.WithNoDirectDependency())
	bf.AddLambdaNode("waitB", wait("waitB")).AddInputWithOptions("sw",
		[]*compose.FieldMapping{}, compose.WithNoDirectDependency())
	bf.AddLambdaNode("noWait", compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return map[string]any{"node": "noWait"}, nil
	})).AddInputWithOptions("sw",
		[]*compose.FieldMapping{}, compose.WithNoDirectDependency())

	bf.AddBranch("sw", compose.NewGraphMultiBranch(
		func(ctx context.Context, in map[string]any) (map[string]bool, error) {
			if in["arm"] == true {
				return map[string]bool{"waitA": true, "waitB": true}, nil
			}
			return map[string]bool{"noWait": true}, nil
		},
		map[string]bool{"waitA": true, "waitB": true, "noWait": true}))

	bf.AddLambdaNode("joinW", compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		if _, ok := in["wa"]; ok {
			p.answerJoin.Store(in)
		}
		return in, nil
	})).AddInput("waitA", compose.ToField("wa")).
		AddInput("waitB", compose.ToField("wb")).
		AddInput("noWait", compose.ToField("fast"))
	bf.End().AddInput("joinW")
	return bf
}

// compileG34 builds the cyclic outer graph: START -> body -> (branch loops to
// body while iter < 2, else END; endless pins an unbounded cycle). Loop count
// lives in WithGenLocalState and is stamped onto the body output by the
// post-handler, which also strips "arm" so iteration 2 takes the fast branch.
func compileG34(ctx context.Context, p *g34Probe, store compose.CheckPointStore, maxSteps int, endless bool) (compose.Runnable[map[string]any, map[string]any], error) {
	g := compose.NewGraph[map[string]any, map[string]any](
		compose.WithGenLocalState(func(ctx context.Context) *probeLoopState {
			return &probeLoopState{}
		}))

	if err := g.AddGraphNode("body", buildG34Body(p),
		compose.WithStatePostHandler[map[string]any, *probeLoopState](
			func(ctx context.Context, out map[string]any, st *probeLoopState) (map[string]any, error) {
				st.Iter++
				p.bodyRuns.Add(1)
				if out == nil {
					out = map[string]any{}
				}
				out["iter"] = st.Iter
				delete(out, "arm")
				return out, nil
			})); err != nil {
		return nil, err
	}
	if err := g.AddEdge(compose.START, "body"); err != nil {
		return nil, err
	}
	if err := g.AddBranch("body", compose.NewGraphBranch(
		func(ctx context.Context, in map[string]any) (string, error) {
			iter, _ := in["iter"].(int)
			if endless || iter < 2 {
				return "body", nil
			}
			return compose.END, nil
		},
		map[string]bool{"body": true, compose.END: true})); err != nil {
		return nil, err
	}

	opts := []compose.GraphCompileOption{
		compose.WithGraphName("inofy-repeat-probe"),
		compose.WithCheckPointStore(store),
	}
	if maxSteps > 0 {
		opts = append(opts, compose.WithMaxRunSteps(maxSteps))
	}
	return g.Compile(ctx, opts...)
}

// rootCauseCtxs collects every IsRootCause interrupt context, including
// those nested under SubGraphs, deduplicated by resume ID.
func rootCauseCtxs(info *compose.InterruptInfo) []*compose.InterruptCtx {
	seen := map[string]*compose.InterruptCtx{}
	var walk func(ii *compose.InterruptInfo)
	walk = func(ii *compose.InterruptInfo) {
		for _, c := range ii.InterruptContexts {
			if c.IsRootCause {
				seen[c.ID] = c
			}
		}
		for _, sub := range ii.SubGraphs {
			walk(sub)
		}
	}
	walk(info)
	out := make([]*compose.InterruptCtx, 0, len(seen))
	for _, c := range seen {
		out = append(out, c)
	}
	return out
}

// rootCauseAddrs returns the sorted hierarchical addresses of all
// root-cause contexts — the stable identity of an interrupt point.
func rootCauseAddrs(info *compose.InterruptInfo) []string {
	ctxs := rootCauseCtxs(info)
	out := make([]string, 0, len(ctxs))
	for _, c := range ctxs {
		out = append(out, c.Address.String())
	}
	sort.Strings(out)
	return out
}

func TestEinoGraphRepeatAndResume(t *testing.T) {
	ctx := context.Background()

	// An unbounded cycle must fail, not spin, once the step bound is hit.
	t.Run("max_run_steps_bounds_cycle", func(t *testing.T) {
		p := newG34Probe()
		r, err := compileG34(ctx, p, &stagingCheckPointStore{}, 6, true)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, err = r.Invoke(ctx, map[string]any{"arm": false})
		if !errors.Is(err, compose.ErrExceedMaxSteps) {
			t.Fatalf("want ErrExceedMaxSteps, got %v", err)
		}
	})

	// Step caps are a step-mode (plain Graph) concept: a dag-mode Workflow
	// rejects the option at compile time.
	t.Run("workflow_rejects_max_steps", func(t *testing.T) {
		wf := compose.NewWorkflow[map[string]any, map[string]any]()
		wf.AddLambdaNode("n", compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
			return in, nil
		})).AddInput(compose.START)
		wf.End().AddInput("n")
		_, err := wf.Compile(ctx, compose.WithMaxRunSteps(4))
		if err == nil || !strings.Contains(err.Error(), "dag") {
			t.Fatalf("want dag max-steps rejection, got %v", err)
		}
	})

	t.Run("two_waits_resume_with_stable_addresses", func(t *testing.T) {
		p := newG34Probe()
		store := &stagingCheckPointStore{}
		const cpID = "s01-g34"

		r1, err := compileG34(ctx, p, store, 0, false)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		_, err = r1.Invoke(ctx, map[string]any{"arm": true}, compose.WithCheckPointID(cpID))
		info, ok := compose.ExtractInterruptInfo(err)
		if !ok {
			t.Fatalf("want interrupt, got err=%v", err)
		}

		// Both waits interrupted together. Each root cause carries an opaque
		// resume ID plus a hierarchical Address proving it sits inside the
		// subgraph: "...;node:body;node:waitX".
		ctxs := rootCauseCtxs(info)
		if len(ctxs) != 2 {
			t.Fatalf("want 2 root-cause wait addresses, got %v", ctxs)
		}
		waitID := map[string]string{}
		for _, c := range ctxs {
			addr := c.Address.String()
			switch {
			case strings.HasSuffix(addr, "node:waitA"):
				waitID["waitA"] = c.ID
			case strings.HasSuffix(addr, "node:waitB"):
				waitID["waitB"] = c.ID
			}
			if !strings.Contains(addr, "node:body") {
				t.Fatalf("wait address lost subgraph nesting: %s", addr)
			}
		}
		if waitID["waitA"] == "" || waitID["waitB"] == "" {
			t.Fatalf("interrupt addresses do not name the wait nodes: %v", ctxs)
		}
		if waitID["waitA"] == waitID["waitB"] {
			t.Fatal("simultaneous waits share one resume ID")
		}
		addrs := rootCauseAddrs(info)

		// The wait's user info survived into the interrupt bundle.
		var sawInfo bool
		for _, c := range info.InterruptContexts {
			if m, okm := c.Info.(map[string]any); okm && m["kind"] == "wait" {
				sawInfo = true
			}
		}
		if !sawInfo {
			t.Fatal("interrupt contexts lost the wait's info payload")
		}

		// Checkpoint staged on suspend.
		if len(store.snapshot(cpID)) == 0 {
			t.Fatal("no checkpoint bytes staged under the run's checkpoint ID")
		}

		// Malformed resume address: must not complete the run. Observed on
		// v0.9.13 an unmatched resume key is silently ignored — no error, no
		// diagnostic — and the untargeted waits re-interrupt. The Address is
		// the stable identity; InterruptCtx.ID is re-minted on every emission,
		// so a resume must use the IDs from the latest suspend.
		_, err = r1.Invoke(compose.BatchResumeWithData(ctx, map[string]any{"bogus-address": "x"}),
			nil, compose.WithCheckPointID(cpID))
		info2, ok2 := compose.ExtractInterruptInfo(err)
		if !ok2 {
			t.Fatalf("malformed resume did not re-interrupt: err=%v", err)
		}
		addrs2 := rootCauseAddrs(info2)
		if !equalSet(addrs, addrs2) {
			t.Fatalf("wait addresses changed across resume: %v -> %v", addrs, addrs2)
		}
		ctxs2 := rootCauseCtxs(info2)
		waitID2 := map[string]string{}
		for _, c := range ctxs2 {
			addr := c.Address.String()
			switch {
			case strings.HasSuffix(addr, "node:waitA"):
				waitID2["waitA"] = c.ID
			case strings.HasSuffix(addr, "node:waitB"):
				waitID2["waitB"] = c.ID
			}
		}
		if waitID2["waitA"] == "" || waitID2["waitB"] == "" {
			t.Fatalf("re-interrupt addresses do not name the wait nodes: %v", ctxs2)
		}
		for _, w := range []string{"waitA", "waitB"} {
			if waitID[w] == waitID2[w] {
				t.Fatalf("InterruptCtx.ID for %s not re-minted across suspends", w)
			}
		}
		if p.interruptStateSeen.Load() != 2 {
			t.Fatalf("rerun waits did not recover persisted state, saw %d", p.interruptStateSeen.Load())
		}

		// Reopen: a fresh runner compiled from the same definition resumes
		// from the staged checkpoint — the store is the only carrier.
		r2, err := compileG34(ctx, p, store, 0, false)
		if err != nil {
			t.Fatalf("recompile: %v", err)
		}
		out, err := r2.Invoke(compose.BatchResumeWithData(ctx,
			map[string]any{waitID2["waitA"]: "answer-A", waitID2["waitB"]: "answer-B"}),
			nil, compose.WithCheckPointID(cpID))
		if err != nil {
			t.Fatalf("resume: %v", err)
		}

		// Iteration ran exactly twice; the second iteration took the fast
		// branch because the post-handler stripped "arm".
		if out["iter"] != 2 {
			t.Fatalf("final iter %v, want 2", out["iter"])
		}
		if out["fast"] == nil {
			t.Fatalf("iteration 2 did not take the noWait branch: %v", out)
		}
		if p.bodyRuns.Load() != 2 {
			t.Fatalf("body post-handler ran %d times, want 2", p.bodyRuns.Load())
		}
		for _, w := range []string{"waitA", "waitB"} {
			// initial pass + malformed-resume rerun + resume pass
			if got := p.waits[w].Load(); got != 3 {
				t.Fatalf("%s executed %d times, want 3", w, got)
			}
		}

		// Resume data reached the waits that owned each address.
		joinIn, _ := p.answerJoin.Load().(map[string]any)
		wa, _ := joinIn["wa"].(map[string]any)
		wb, _ := joinIn["wb"].(map[string]any)
		if wa["answer"] != "answer-A" || wb["answer"] != "answer-B" {
			t.Fatalf("resume data misdelivered: wa=%v wb=%v", wa, wb)
		}
	})
}
