// Compiler and runner: an admitted INOFY scope compiles to an Eino
// compose.Workflow, and Invoke executes it once with an injected
// executor. Eino owns readiness and branch activation; predecessors
// pass whole packets by node ID and bindings resolve inside wrappers
// (architecture §7.2).
package einoruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/compose"

	"github.com/ProjectViVy/inofy/internal/definition"
)

// TypeInfo is the catalog surface the runtime needs per node type.
type TypeInfo struct {
	ImplementationID string
	Replay           string
	SupportsWait     bool
	InputSchema      json.RawMessage
	OutputSchema     json.RawMessage
}

// Limits bound a single run (§7.4 subset owned by the runtime).
type Limits struct {
	MaxActivations     int
	MaxAttemptsPerCall int
	NodeTimeoutMS      int64
}

// Call is one executor invocation for one node activation. Retry,
// timeout and fallback metadata are resolved here; the run-scoped
// boundary owns attempt counting and commit ordering.
type Call struct {
	Path             string
	TypeID           string
	ImplementationID string
	Config           json.RawMessage
	Input            json.RawMessage
	Attempt          int
	Replay           string
	MaxAttempts      int
	DelayMS          int64
	TimeoutMS        int64
	OnError          json.RawMessage
	OutputSchema     json.RawMessage
}

// Executor is the host's node invocation surface (a function value so
// internal runs stay binding-agnostic; the root package adapts
// inofy.NodeExecutor).
type Executor func(ctx context.Context, call Call) (json.RawMessage, error)

// Program is a compiled workflow plus its settlement spec. It is
// immutable and safe for concurrent Invoke calls.
type Program struct {
	runnable      compose.Runnable[map[string]any, map[string]any]
	outputs       map[string]map[string]any
	outputsSchema json.RawMessage
	nodes         map[string]map[string]any
	regions       map[string]map[string]map[string]bool
	limits        Limits
}

// runState is injected through the invoke context and mutable per run.
type runState struct {
	exec     Executor
	acts     atomic.Int64
	actLimit int
	timeout  time.Duration
	attempts int
	mu       sync.Mutex
	decision map[string]string // switch node ID → chosen port
	executed map[string]bool   // node IDs that acquired an activation
}

type runStateKey struct{}

// CompileProgram builds one scope graph into an Eino workflow and
// returns the runnable Program. The doc must already be admitted by
// definition validation; repeat nodes and Waits are S04/S06 and are
// rejected with ErrUnsupportedFeature.
func CompileProgram(ctx context.Context, defDoc map[string]any, types map[string]TypeInfo, limits Limits) (*Program, error) {
	graph, ok := defDoc["graph"].(map[string]any)
	if !ok {
		return nil, &Error{Code: ErrInvalidDefinition, Message: "definition has no graph"}
	}
	outs, _ := graph["outputs"].(map[string]any)
	outputs := make(map[string]map[string]any, len(outs))
	for k, v := range outs {
		outputs[k], _ = v.(map[string]any)
	}
	var outputsSchema json.RawMessage
	if s, ok := defDoc["outputs_schema"]; ok && s != nil {
		raw, err := json.Marshal(s)
		if err != nil {
			return nil, &Error{Code: ErrInvalidDefinition, Err: err, Message: "outputs_schema marshal"}
		}
		outputsSchema = raw
	}

	b := &scopeBuilder{types: types}
	regions, _ := regionIndex(graph)
	wf, nodes, err := b.buildScope(graph, "/graph")
	if err != nil {
		return nil, err
	}
	runnable, err := wf.Compile(ctx)
	if err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Err: err, Message: "eino workflow compile"}
	}
	return &Program{
		runnable:      runnable,
		outputs:       outputs,
		outputsSchema: outputsSchema,
		nodes:         nodes,
		regions:       regions,
		limits:        limits,
	}, nil
}

// Invoke runs the compiled workflow once. It returns the resolved,
// schema-checked outputs object, run diagnostics, and the first hard
// error. Nodes the branch decisions did not activate are reported as
// skipped diagnostics after settlement.
func (p *Program) Invoke(ctx context.Context, input json.RawMessage, exec Executor) (json.RawMessage, []definition.Finding, error) {
	in, err := decodeJSON(input)
	if err != nil {
		return nil, nil, &Error{Code: ErrInvalidDefinition, Err: err, Message: "run input is not valid JSON"}
	}
	rs := &runState{
		exec:     exec,
		actLimit: p.limits.MaxActivations,
		timeout:  time.Duration(p.limits.NodeTimeoutMS) * time.Millisecond,
		attempts: p.limits.MaxAttemptsPerCall,
		decision: map[string]string{},
		executed: map[string]bool{},
	}
	ctx = context.WithValue(ctx, runStateKey{}, rs)
	end, err := p.runnable.Invoke(ctx, map[string]any{"input": packetOf(in)})
	if err != nil {
		return nil, nil, err
	}
	out, diags, err := p.settle(end, rs, in)
	if err != nil {
		return nil, diags, err
	}
	return out, diags, nil
}

// settle maps END packets to declared outputs, marks skipped nodes,
// and validates the outputs schema (architecture §7.2 step 6).
func (p *Program) settle(end map[string]any, rs *runState, runInput any) (json.RawMessage, []definition.Finding, error) {
	in := map[string]any{"input": packetOf(runInput)}
	for k, v := range end {
		in[k] = v
	}
	resolved := make(map[string]any, len(p.outputs))
	for _, name := range sortedNodeKeys(p.outputs) {
		v, err := bindValue(p.outputs[name], in, "/outputs/"+name)
		if err != nil {
			return nil, nil, err
		}
		resolved[name] = v
	}
	// Nodes on unchosen branches are marked skipped from the recorded
	// decision log rather than arrival order.
	diags := skippedDiagnostics(p.regions, rs)
	if len(p.outputsSchema) > 0 {
		if err := definition.ValidateValueJSON(p.outputsSchema, resolved); err != nil {
			return nil, diags, &Error{Code: ErrSchemaMismatch, Path: "/outputs",
				Err: err, Message: "outputs do not match outputs_schema"}
		}
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		return nil, diags, &Error{Code: ErrInvalidDefinition, Err: err, Message: "outputs marshal"}
	}
	return raw, diags, nil
}

func (rs *runState) acquire(path string) error {
	if rs.actLimit > 0 && rs.acts.Add(1) > int64(rs.actLimit) {
		return &Error{Code: ErrBudgetExceeded, Path: path,
			Message: "activation bound exceeded"}
	}
	rs.mu.Lock()
	rs.executed[pathID(path)] = true
	rs.mu.Unlock()
	return nil
}

func (rs *runState) recordDecision(switchID, port string) {
	rs.mu.Lock()
	rs.decision[switchID] = port
	rs.mu.Unlock()
}

// scopeBuilder holds compile-time state for one graph scope.
type scopeBuilder struct {
	types          map[string]TypeInfo
	containerDepth int
}

// buildScope wires every node and edge of a scope into an Eino
// workflow. Returned nodes are the doc's node records keyed by ID.
func (b *scopeBuilder) buildScope(g map[string]any, path string) (*compose.Workflow[map[string]any, map[string]any], map[string]map[string]any, error) {
	wf := compose.NewWorkflow[map[string]any, map[string]any]()
	nodes := indexNodes(g)
	edges := indexEdges(g)
	regions, joins := regionIndex(g)

	for _, id := range sortedNodeKeys(nodes) {
		n := nodes[id]
		kind, _ := n["kind"].(string)
		nodePath := path + "/nodes/" + id
		var nn *compose.WorkflowNode
		if kind == "repeat" {
			sub, err := b.buildRepeat(id, nodePath, n)
			if err != nil {
				return nil, nil, err
			}
			nn = wf.AddGraphNode(id, sub)
		} else {
			lam, err := b.nodeLambda(id, nodePath, n)
			if err != nil {
				return nil, nil, err
			}
			nn = wf.AddLambdaNode(id, lam)
		}
		if kind == "select" {
			if err := b.wireSelect(nn, n, nodePath); err != nil {
				return nil, nil, err
			}
		} else {
			b.wirePreds(nn, id, n, edges, regions, joins)
		}
		b.wireRootInput(nn, id, edges, joins)
	}

	for _, id := range sortedNodeKeys(nodes) {
		n := nodes[id]
		kind, _ := n["kind"].(string)
		if kind != "switch" {
			continue
		}
		targets := branchTargets(edges, id)
		endSet := map[string]bool{}
		for _, t := range targets {
			endSet[t] = true
		}
		branch := compose.NewGraphMultiBranch(
			func(ctx context.Context, in map[string]any) (map[string]bool, error) {
				port, _ := in[packetPortKey].(string)
				set := map[string]bool{}
				for _, t := range targetsForPort(edges, id, port) {
					set[t] = true
				}
				return set, nil
			}, endSet)
		wf.AddBranch(id, branch)
	}

	exits, _ := g["exits"].([]any)
	if len(exits) == 0 {
		return nil, nil, &Error{Code: ErrInvalidDefinition, Path: path,
			Message: "scope declares no exits"}
	}
	for _, e := range exits {
		exitID, _ := e.(string)
		if _, ok := nodes[exitID]; !ok {
			return nil, nil, &Error{Code: ErrInvalidDefinition, Path: path,
				Message: fmt.Sprintf("exit %q is not a node", exitID)}
		}
		wf.End().AddInput(exitID, compose.ToField(exitID))
	}
	return wf, nodes, nil
}

// nodeLambda builds the node wrapper by kind.
func (b *scopeBuilder) nodeLambda(id, path string, n map[string]any) (*compose.Lambda, error) {
	kind, _ := n["kind"].(string)
	switch kind {
	case "call":
		return b.callLambda(id, path, n)
	case "switch":
		return b.switchLambda(id, path, n)
	case "select":
		return b.selectLambda(id, path, n)
	}
	return nil, &Error{Code: ErrInvalidDefinition, Path: path,
		Message: "unknown node kind " + kind}
}

// callLambda wraps one executor call with activation accounting,
// binding resolution, timeout, retry, and the on_error fallback.
func (b *scopeBuilder) callLambda(id, path string, n map[string]any) (*compose.Lambda, error) {
	typ, _ := n["type"].(string)
	t, ok := b.types[typ]
	if !ok {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path,
			Message: "type " + typ + " has no catalog entry"}
	}
	inputs, _ := n["inputs"].(map[string]any)
	var cfg json.RawMessage
	if c, ok := n["config"]; ok && c != nil {
		raw, err := json.Marshal(c)
		if err != nil {
			return nil, &Error{Code: ErrInvalidDefinition, Path: path + "/config", Err: err}
		}
		cfg = raw
	}
	nodeTimeoutMS := asIntField(n["timeout_ms"])
	retry := retryPolicy(n)
	onError, hasOnError := n["on_error"].(map[string]any)

	return compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		rs, _ := ctx.Value(runStateKey{}).(*runState)
		if rs == nil {
			return nil, &Error{Code: ErrInvalidDefinition, Path: path, Message: "no run state"}
		}
		path := runtimePath(in, path, id)
		if err := rs.acquire(path); err != nil {
			return nil, err
		}
		bound, err := bindInputs(inputs, in, path+"/inputs")
		if err != nil {
			return nil, err
		}
		if len(t.InputSchema) > 0 {
			if err := definition.ValidateValueJSON(t.InputSchema, bound); err != nil {
				return nil, &Error{Code: ErrSchemaMismatch, Path: path + "/inputs",
					Err: err, Message: "resolved inputs violate type input_schema"}
			}
		}
		inputRaw, err := json.Marshal(bound)
		if err != nil {
			return nil, &Error{Code: ErrInvalidDefinition, Path: path, Err: err}
		}
		var onErr json.RawMessage
		if hasOnError {
			if lit, ok := onError["literal"]; ok {
				raw, merr := json.Marshal(lit)
				if merr != nil {
					return nil, &Error{Code: ErrInvalidDefinition, Path: path,
						Err: merr, Message: "on_error literal is not JSON"}
				}
				onErr = raw
			}
		}
		out, err := rs.exec(ctx, Call{
			Path:             path,
			TypeID:           typ,
			ImplementationID: t.ImplementationID,
			Config:           cfg,
			Input:            inputRaw,
			Replay:           t.Replay,
			MaxAttempts:      firstPos(retry.maxAttempts, rs.attempts, 1),
			DelayMS:          retry.delayMS,
			TimeoutMS:        firstPos64(int64(nodeTimeoutMS), rs.timeout.Milliseconds(), 0),
			OnError:          onErr,
			OutputSchema:     t.OutputSchema,
		})
		if err != nil {
			return nil, err
		}
		decoded, err := decodeJSON(out)
		if err != nil {
			return nil, &Error{Code: ErrSchemaMismatch, Path: path, Err: err,
				Message: "node output is not valid JSON"}
		}
		if len(t.OutputSchema) > 0 {
			if err := definition.ValidateValueJSON(t.OutputSchema, decoded); err != nil {
				return nil, &Error{Code: ErrSchemaMismatch, Path: path,
					Err: err, Message: "node output violates type output_schema"}
			}
		}
		return packetOf(decoded), nil
	}), nil
}

// switchLambda resolves inputs, evaluates cases in order, and emits a
// packet carrying the chosen port for the Eino branch.
func (b *scopeBuilder) switchLambda(id, path string, n map[string]any) (*compose.Lambda, error) {
	inputs, _ := n["inputs"].(map[string]any)
	cases, _ := n["cases"].([]any)
	defPort, _ := n["default_port"].(string)
	return compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		rs, _ := ctx.Value(runStateKey{}).(*runState)
		path := runtimePath(in, path, id)
		if rs != nil {
			if err := rs.acquire(path); err != nil {
				return nil, err
			}
		}
		bound, err := bindInputs(inputs, in, path+"/inputs")
		if err != nil {
			return nil, err
		}
		chosen := defPort
		for i, cv := range cases {
			casePath := fmt.Sprintf("%s/cases/%d", path, i)
			co, _ := cv.(map[string]any)
			port, _ := co["port"].(string)
			when, _ := co["when"].(map[string]any)
			if when == nil {
				if chosen == "" {
					chosen = port
				}
				continue
			}
			ok, err := evalPredicate(when, bound, in, casePath+"/when")
			if err != nil {
				return nil, err
			}
			if ok {
				chosen = port
				break
			}
		}
		if chosen == "" {
			return nil, &Error{Code: ErrInvalidDefinition, Path: path,
				Message: "no case matched and default_port is empty"}
		}
		if rs != nil {
			rs.recordDecision(id, chosen)
		}
		return map[string]any{packetOutKey: bound, packetPortKey: chosen}, nil
	}), nil
}

// selectLambda converges region ends: exactly one candidate packet is
// present; its value resolves via the candidate's pointer. Zero
// candidates use the authored fallback, and multiple candidates are a
// hard error — never arrival order (§5.3).
func (b *scopeBuilder) selectLambda(id, path string, n map[string]any) (*compose.Lambda, error) {
	cands, _ := n["candidates"].([]any)
	fallback, hasFallback := n["fallback"].(map[string]any)
	type cand struct{ source, pointer string }
	var parsed []cand
	for _, cv := range cands {
		co, _ := cv.(map[string]any)
		src, _ := co["source"].(string)
		ptr, _ := co["pointer"].(string)
		parsed = append(parsed, cand{source: src, pointer: ptr})
	}
	return compose.InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		rs, _ := ctx.Value(runStateKey{}).(*runState)
		path := runtimePath(in, path, id)
		if rs != nil {
			if err := rs.acquire(path); err != nil {
				return nil, err
			}
		}
		var found *cand
		count := 0
		for i, c := range parsed {
			pv, ok := in[c.source]
			if !ok || pv == nil {
				continue
			}
			count++
			if count == 1 {
				found = &parsed[i]
			}
		}
		switch {
		case count > 1:
			return nil, &Error{Code: ErrSelectAmbiguous, Path: path,
				Message: "multiple select candidates produced packets"}
		case count == 1:
			packet, _ := in[found.source].(map[string]any)
			v, err := resolvePointer(packetValue(packet), found.pointer)
			if err != nil {
				return nil, &Error{Code: ErrBindingMissing, Path: path,
					Message: "select candidate " + found.source + ": " + err.Error()}
			}
			return packetOf(v), nil
		}
		if hasFallback {
			v, err := bindValue(fallback, in, path+"/fallback")
			if err != nil {
				return nil, err
			}
			return packetOf(v), nil
		}
		return nil, &Error{Code: ErrSelectZeroCandidate, Path: path,
			Message: "no select candidate produced a packet and no fallback is declared"}
	}), nil
}

// wirePreds maps every inbound non-port edge to a predecessor packet
// field. Edges arriving from outside a node's owning region (a port
// edge, or a dominating ancestor) are field mappings with
// WithNoDirectDependency: they carry data without creating an
// activation route (§7.2 constraint).
func (b *scopeBuilder) wirePreds(nn *compose.WorkflowNode, id string, n map[string]any, edges map[string][]edgeRec, regions map[string]map[string]map[string]bool, joins map[string]bool) {
	inbound := inboundEdges(edges, id)
	for _, e := range inbound {
		from := e.from
		portEdge := e.port != ""
		entryFromOutside := foreignEntry(id, from, regions)
		if portEdge || entryFromOutside {
			// ToField: carry the whole predecessor packet as data with
			// no activation dependency.
			nn.AddInputWithOptions(from,
				[]*compose.FieldMapping{compose.ToField(from)},
				compose.WithNoDirectDependency())
			continue
		}
		nn.AddInput(from, compose.ToField(from))
	}
}

// wireSelect inputs a select from each candidate source.
func (b *scopeBuilder) wireSelect(nn *compose.WorkflowNode, n map[string]any, path string) error {
	cands, _ := n["candidates"].([]any)
	for _, cv := range cands {
		co, _ := cv.(map[string]any)
		src, _ := co["source"].(string)
		if src == "" {
			return &Error{Code: ErrInvalidDefinition, Path: path + "/candidates",
				Message: "candidate without source"}
		}
		nn.AddInput(src, compose.ToField(src))
	}
	return nil
}

// wireRootInput gives every node access to the run input. Root nodes
// take a real START dependency; gated nodes take a non-triggering
// field mapping (§7.2).
func (b *scopeBuilder) wireRootInput(nn *compose.WorkflowNode, id string, edges map[string][]edgeRec, joins map[string]bool) {
	if len(inboundEdges(edges, id)) == 0 {
		nn.AddInput(compose.START)
		return
	}
	nn.AddInputWithOptions(compose.START,
		[]*compose.FieldMapping{compose.MapFields("input", "input")},
		compose.WithNoDirectDependency())
}

// --- scope indexing ---

type edgeRec struct {
	from, to, port string
}

func indexNodes(g map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, nv := range anySlice(g["nodes"]) {
		if n, ok := nv.(map[string]any); ok {
			if id, ok := n["id"].(string); ok {
				out[id] = n
			}
		}
	}
	return out
}

// indexEdges returns edges grouped by source ("from:<id>") and by
// target ("to:<id>").
func indexEdges(g map[string]any) map[string][]edgeRec {
	out := map[string][]edgeRec{}
	for _, ev := range anySlice(g["edges"]) {
		e, ok := ev.(map[string]any)
		if !ok {
			continue
		}
		r := edgeRec{
			from: str(e["from"]),
			to:   str(e["to"]),
			port: str(e["port"]),
		}
		out["from:"+r.from] = append(out["from:"+r.from], r)
		out["to:"+r.to] = append(out["to:"+r.to], r)
	}
	return out
}

func inboundEdges(edges map[string][]edgeRec, id string) []edgeRec {
	return edges["to:"+id]
}

// regionIndex computes each switch's per-port regions and the set of
// join nodes. A region is the forward cone from a port-labelled edge
// target, cut at the switch's join (matching admission rulings).
func regionIndex(g map[string]any) (map[string]map[string]map[string]bool, map[string]bool) {
	nodes := indexNodes(g)
	edges := indexEdges(g)
	regions := map[string]map[string]map[string]bool{}
	joins := map[string]bool{}
	fwd := map[string][]string{}
	for id := range nodes {
		for _, e := range edges["from:"+id] {
			fwd[id] = append(fwd[id], e.to)
		}
	}
	for id, n := range nodes {
		if str(n["kind"]) != "switch" {
			continue
		}
		join := str(n["join"])
		regions[id] = map[string]map[string]bool{}
		for _, e := range edges["from:"+id] {
			if e.port == "" {
				continue
			}
			seen := map[string]bool{}
			queue := []string{e.to}
			for len(queue) > 0 {
				cur := queue[0]
				queue = queue[1:]
				if cur == join || seen[cur] {
					continue
				}
				seen[cur] = true
				queue = append(queue, fwd[cur]...)
			}
			regions[id][e.port] = seen
		}
		if join != "" {
			joins[join] = true
		}
	}
	return regions, joins
}

// foreignEntry reports whether an edge x→y enters a region from a node
// outside it (a dominating ancestor, or the switch's own port edge is
// handled separately).
func foreignEntry(toID, fromID string, regions map[string]map[string]map[string]bool) bool {
	for switchID, ports := range regions {
		if fromID == switchID {
			continue
		}
		for _, members := range ports {
			if members[toID] && !members[fromID] {
				return true
			}
		}
	}
	return false
}

func branchTargets(edges map[string][]edgeRec, switchID string) []string {
	var out []string
	for _, e := range edges["from:"+switchID] {
		if e.port != "" {
			out = append(out, e.to)
		}
	}
	return out
}

func targetsForPort(edges map[string][]edgeRec, switchID, port string) []string {
	var out []string
	for _, e := range edges["from:"+switchID] {
		if e.port == port {
			out = append(out, e.to)
		}
	}
	return out
}

type retryCfg struct {
	maxAttempts int
	delayMS     int64
}

func retryPolicy(n map[string]any) retryCfg {
	r, _ := n["retry"].(map[string]any)
	return retryCfg{
		maxAttempts: asIntField(r["max_attempts"]),
		delayMS:     int64(asIntField(r["delay_ms"])),
	}
}

func firstPos(v, fallback, floor int) int {
	if v > 0 {
		return v
	}
	if fallback > 0 {
		return fallback
	}
	return floor
}

func firstPos64(v, fallback, floor int64) int64 {
	if v > 0 {
		return v
	}
	if fallback > 0 {
		return fallback
	}
	return floor
}

func isDeadline(err error) bool {
	for e := err; e != nil; {
		if e == context.DeadlineExceeded {
			return true
		}
		type unwrapped interface{ Unwrap() error }
		u, ok := e.(unwrapped)
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// skippedDiagnostics marks authored nodes inactive after settlement
// from the recorded switch decisions (§7.2 step 6 — the validated
// decision log, not arrival order). A nested switch inside an unchosen
// port contributes nothing extra: its regions are already inside the
// outer region.
func skippedDiagnostics(regions map[string]map[string]map[string]bool, rs *runState) []definition.Finding {
	var out []definition.Finding
	seen := map[string]bool{}
	for _, switchID := range sortedRegionKeys(regions) {
		chosen, decided := rs.decision[switchID]
		for _, port := range sortedPortKeys(regions[switchID]) {
			if decided && port == chosen {
				continue
			}
			for _, member := range sortedMemberKeys(regions[switchID][port]) {
				if rs.executed[member] || seen[member] {
					continue
				}
				seen[member] = true
				out = append(out, definition.Finding{
					Check: "topology", Path: "/graph/nodes/" + member,
					Code:    "node_skipped",
					Message: "node not activated by the recorded branch decisions",
				})
			}
		}
	}
	return out
}

func pathID(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

var _ = sort.Strings

func anySlice(v any) []any {
	s, _ := v.([]any)
	return s
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
