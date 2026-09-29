// Catalog-facing and structural validation over an already-decoded
// definition document. S02 T1 lands the capability checks; T3 lands
// bounded graph admission (reachability, regions, activation bounds).
package definition

import (
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"
)

// TypeRef is the catalog-neutral projection of an inofy.NodeDescriptor
// the validators need; the root adapter converts descriptors.
type TypeRef struct {
	TypeID           string
	ImplementationID string
	ConfigSchema     json.RawMessage
	InputSchema      json.RawMessage
	OutputSchema     json.RawMessage
	Capabilities     []string
	Replay           string
	SupportsWait     bool
}

// maxPredicateDepth bounds all/any/not nesting (architecture §7.4).
const maxPredicateDepth = 8

// ValidateSemanticDoc runs the capability checks that need the frozen
// catalog: call type resolution, config/input/fallback schema
// conformance, declared capabilities and replay policy. Authority is
// never checked here — only hosts can complete that check.
func ValidateSemanticDoc(doc map[string]any, types []TypeRef, features map[string]bool) []Finding {
	byType := make(map[string]TypeRef, len(types))
	for _, t := range types {
		byType[t.TypeID] = t
	}
	var findings []Finding
	graph, _ := asObject(doc["graph"])
	if graph != nil {
		walkGraphSemantics(graph, "", &findings, byType, features)
	}
	return findings
}

// walkGraphSemantics visits every node at one graph level; rel prefixes
// nested repeat bodies ("loop/step" mirrors §5.4 logical keys).
func walkGraphSemantics(g map[string]any, rel string, f *[]Finding,
	byType map[string]TypeRef, features map[string]bool) {

	nodes, _ := asArray(g["nodes"])
	for _, nv := range nodes {
		n, ok := asObject(nv)
		if !ok {
			continue
		}
		id, _ := asString(n["id"])
		kind, _ := asString(n["kind"])
		nodeRel := rel + id
		np := nodeRel // semantic diagnostics path by logical node key

		switch kind {
		case "call":
			checkCallSemantics(n, np, f, byType, features)
		case "switch":
			checkPredicateDepths(n["cases"], np+"/cases", f)
		case "repeat":
			checkPredicateDepth(n["until"], np+"/until", 1, f)
			if body, ok := asObject(n["body"]); ok {
				walkGraphSemantics(body, nodeRel+"/", f, byType, features)
			}
		}
	}
}

func checkCallSemantics(n map[string]any, np string, f *[]Finding,
	byType map[string]TypeRef, features map[string]bool) {

	typeID, _ := asString(n["type"])
	if typeID == "" {
		return // missing_field was reported by the decoder
	}
	desc, ok := byType[typeID]
	if !ok {
		*f = append(*f, Finding{Check: "capability", Path: np, Code: "unknown_node_type",
			Message: "call type " + strconv.Quote(typeID) + " is not in the frozen catalog"})
		return
	}

	for _, cap := range desc.Capabilities {
		if !features[cap] {
			*f = append(*f, Finding{Check: "capability", Path: np, Code: "capability_missing",
				Message: "node type " + typeID + " requires capability " + strconv.Quote(cap)})
		}
	}

	if cfg, ok := n["config"]; ok && len(desc.ConfigSchema) > 0 {
		if err := validateValue(desc.ConfigSchema, cfg); err != nil {
			*f = append(*f, Finding{Check: "capability", Path: np, Code: "schema_mismatch",
				Message: "config violates " + typeID + " config schema: " + trimErr(err)})
		}
	}

	// Literal input bindings are checked against the declared property
	// schema when the input schema carries one; required properties must
	// have a binding.
	if inputs, ok := asObject(n["inputs"]); ok && len(desc.InputSchema) > 0 {
		var inputSchema map[string]any
		if err := json.Unmarshal(desc.InputSchema, &inputSchema); err == nil {
			props, _ := asObject(inputSchema["properties"])
			reqs, _ := asArray(inputSchema["required"])
			for _, rv := range reqs {
				r, _ := asString(rv)
				if _, bound := inputs[r]; !bound {
					*f = append(*f, Finding{Check: "capability", Path: np, Code: "missing_input",
						Message: "required input " + strconv.Quote(r) + " has no binding"})
				}
			}
			for _, k := range sortedKeys(inputs) {
				propSchema, ok := props[k]
				if !ok {
					continue
				}
				bind, _ := asObject(inputs[k])
				lit, isLiteral := bind["literal"]
				if !isLiteral {
					continue
				}
				raw, err := marshalBack(propSchema)
				if err != nil {
					continue
				}
				if err := validateValue(raw, lit); err != nil {
					*f = append(*f, Finding{Check: "capability", Path: np + "/inputs/" + ptrEscape(k),
						Code: "schema_mismatch", Message: "literal input violates " + typeID + " input schema: " + trimErr(err)})
				}
			}
		}
	}

	// Fallback values must satisfy the output schema (§5.5).
	if oe, ok := asObject(n["on_error"]); ok {
		if mode, _ := asString(oe["mode"]); mode == "fallback" && len(desc.OutputSchema) > 0 {
			if val, has := oe["value"]; has {
				if err := validateValue(desc.OutputSchema, val); err != nil {
					*f = append(*f, Finding{Check: "capability", Path: np + "/on_error", Code: "schema_mismatch",
						Message: "fallback value violates " + typeID + " output schema: " + trimErr(err)})
				}
			}
		}
	}

	// Retry requires the trusted replay policy to allow it (§5.5).
	if _, ok := n["retry"]; ok && desc.Replay == "non_replayable" {
		*f = append(*f, Finding{Check: "capability", Path: np + "/retry", Code: "replay_forbidden",
			Message: "type " + typeID + " is non-replayable; retry is not permitted"})
	}
}

// checkPredicateDepths walks the ordered case list of one switch.
func checkPredicateDepths(casesV any, path string, f *[]Finding) {
	arr, ok := asArray(casesV)
	if !ok {
		return
	}
	for i, cv := range arr {
		if c, ok := asObject(cv); ok {
			checkPredicateDepth(c["when"], path+"/"+strconv.Itoa(i)+"/when", 1, f)
		}
	}
}

// checkPredicateDepth bounds all/any/not nesting at maxPredicateDepth.
func checkPredicateDepth(v any, path string, depth int, f *[]Finding) {
	m, ok := asObject(v)
	if !ok {
		return
	}
	op, _ := asString(m["op"])
	switch op {
	case "all", "any":
		if depth > maxPredicateDepth {
			*f = append(*f, Finding{Check: "budget", Path: path, Code: "predicate_depth",
				Message: "predicate nesting exceeds " + strconv.Itoa(maxPredicateDepth)})
			return
		}
		args, _ := asArray(m["args"])
		for i, av := range args {
			checkPredicateDepth(av, path+"/args/"+strconv.Itoa(i), depth+1, f)
		}
	case "not":
		if depth > maxPredicateDepth {
			*f = append(*f, Finding{Check: "budget", Path: path, Code: "predicate_depth",
				Message: "predicate nesting exceeds " + strconv.Itoa(maxPredicateDepth)})
			return
		}
		checkPredicateDepth(m["arg"], path+"/arg", depth+1, f)
	}
}

func trimErr(err error) string {
	s := err.Error()
	const max = 160
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// Limits ceilings used by the topology/budget checks. The root adapter
// passes the effective limits (definition ∪ host ceilings).
type LimitsRef struct {
	MaxNodes           int
	MaxEdges           int
	MaxRepeatNesting   int
	MaxIterations      int
	MaxActivations     int
	MaxAttemptsPerCall int
	MaxPredicateDepth  int
	Parallelism        int
	MaxDefinitionBytes int64
}

// ValidateTopologyDoc runs bounded structural admission over a decoded
// definition: graph size, per-scope acyclicity, reachability of every
// node from an implicit root, reachability of a declared exit from
// every node, direct edges for every binding source, verified disjoint
// switch regions converging on their select, and a conservative
// activation bound including retries (§4.1, §5.2, §7.4).
func ValidateTopologyDoc(doc map[string]any, limits LimitsRef) []Finding {
	graph, _ := asObject(doc["graph"])
	if graph == nil {
		return nil // shape findings were already reported by the decoder
	}
	var f []Finding

	var nodes, edges int
	walkScopes(graph, "/graph", func(g map[string]any, _ string) {
		nodes += len(asArrayOr(g["nodes"]))
		edges += len(asArrayOr(g["edges"]))
	})
	if limits.MaxNodes > 0 && nodes > limits.MaxNodes {
		f = append(f, Finding{Check: "budget", Path: "/graph", Code: "too_many_nodes",
			Message: strconv.Itoa(nodes) + " nodes exceeds max_nodes " + strconv.Itoa(limits.MaxNodes)})
	}
	if limits.MaxEdges > 0 && edges > limits.MaxEdges {
		f = append(f, Finding{Check: "budget", Path: "/graph", Code: "too_many_edges",
			Message: strconv.Itoa(edges) + " edges exceeds max_edges " + strconv.Itoa(limits.MaxEdges)})
	}

	walkScopes(graph, "/graph", func(g map[string]any, rel string) {
		checkScope(g, rel, limits, &f)
	})
	activations := scopeActivations(graph, limits)
	if limits.MaxActivations > 0 && activations > limits.MaxActivations {
		f = append(f, Finding{Check: "budget", Path: "/graph", Code: "activation_bound",
			Message: "structural activation bound " + strconv.Itoa(activations) +
				" exceeds max_activations " + strconv.Itoa(limits.MaxActivations)})
	}
	return f
}

// walkScopes visits the outer graph and every repeat body. gp is the
// graph's own JSON-pointer path ("loop/" bodies nest under their node).
func walkScopes(g map[string]any, gp string, visit func(g map[string]any, path string)) {
	var rec func(g map[string]any, path string)
	rec = func(g map[string]any, path string) {
		visit(g, path)
		for _, nv := range asArrayOr(g["nodes"]) {
			n, ok := asObject(nv)
			if !ok {
				continue
			}
			if kind, _ := asString(n["kind"]); kind != "repeat" {
				continue
			}
			id, _ := asString(n["id"])
			if body, ok := asObject(n["body"]); ok {
				rec(body, path+"/nodes/"+ptrEscape(id)+"/body")
			}
		}
	}
	rec(g, gp)
}

func asArrayOr(v any) []any {
	a, _ := asArray(v)
	return a
}

// scopeView is one admitted graph level for structural checks.
type scopeView struct {
	nodes map[string]map[string]any
	order []string
	edges []scopeEdge
	exits map[string]bool
	fwd   map[string][]string
	rev   map[string][]string
	cycle map[string]bool
	doms  map[string]map[string]bool
	path  string // JSON-pointer-ish graph path for diagnostics
}

type scopeEdge struct {
	from, to, port string
}

func checkScope(g map[string]any, path string, limits LimitsRef, f *[]Finding) {
	sv := buildScope(g, path)
	sv.checkAcyclicity(f)
	sv.checkReachability(f)
	sv.checkDominators()
	sv.checkDataEdges(f)
	sv.checkOutputExits(g, f)
	sv.checkRegions(f)
}

func buildScope(g map[string]any, path string) *scopeView {
	sv := &scopeView{
		nodes: make(map[string]map[string]any),
		exits: make(map[string]bool),
		fwd:   make(map[string][]string),
		rev:   make(map[string][]string),
		path:  path,
	}
	for _, nv := range asArrayOr(g["nodes"]) {
		n, ok := asObject(nv)
		if !ok {
			continue
		}
		if id, ok := asString(n["id"]); ok && id != "" {
			sv.nodes[id] = n
			sv.order = append(sv.order, id)
		}
	}
	for _, xv := range asArrayOr(g["exits"]) {
		if x, ok := asString(xv); ok && sv.nodes[x] != nil {
			sv.exits[x] = true
		}
	}
	for _, ev := range asArrayOr(g["edges"]) {
		e, ok := asObject(ev)
		if !ok {
			continue
		}
		from, _ := asString(e["from"])
		to, _ := asString(e["to"])
		port, _ := asString(e["port"])
		if sv.nodes[from] == nil || sv.nodes[to] == nil {
			continue // unknown endpoints already reported at decode
		}
		sv.edges = append(sv.edges, scopeEdge{from: from, to: to, port: port})
		sv.fwd[from] = append(sv.fwd[from], to)
		sv.rev[to] = append(sv.rev[to], from)
	}
	return sv
}

// checkAcyclicity runs Kahn's algorithm; leftover nodes form the cycles
// that are illegal outside repeat bodies (the body itself is a DAG too).
func (sv *scopeView) checkAcyclicity(f *[]Finding) {
	indeg := make(map[string]int, len(sv.nodes))
	for _, id := range sv.order {
		indeg[id] = 0
	}
	for _, e := range sv.edges {
		indeg[e.to]++
	}
	var queue []string
	for _, id := range sv.order {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}
	seen := 0
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		seen++
		for _, m := range sv.fwd[n] {
			if indeg[m]--; indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	if seen == len(sv.order) {
		return
	}
	// Kahn leftovers include innocent nodes downstream of a cycle; only
	// nodes that can reach themselves are actual cycle members.
	sv.cycle = make(map[string]bool)
	var members []string
	for _, id := range sv.order {
		if indeg[id] == 0 {
			continue
		}
		for _, m := range sv.fwd[id] {
			if sv.reaches(m, id) {
				sv.cycle[id] = true
				members = append(members, id)
				break
			}
		}
	}
	slices.Sort(members)
	*f = append(*f, Finding{Check: "topology", Path: sv.path, Code: "cycle_outside_repeat",
		Message: "authored cycle through nodes " + strings.Join(members, ", ") +
			"; repetition is only legal via a repeat body"})
}

// checkReachability requires every node reachable from an implicit root
// and able to reach a declared exit (§4.1/§5.1).
func (sv *scopeView) checkReachability(f *[]Finding) {
	reach := sv.bfsFrom(func(n string) bool { return len(sv.rev[n]) == 0 }, sv.fwd)
	back := sv.bfsFrom(func(n string) bool { return sv.exits[n] }, sv.rev)
	for _, id := range sv.order {
		if sv.cycle[id] {
			continue // already reported by checkAcyclicity
		}
		if !reach[id] {
			*f = append(*f, Finding{Check: "topology", Path: sv.path + "/nodes/" + ptrEscape(id),
				Code:    "unreachable_node",
				Message: "node " + strconv.Quote(id) + " is not reachable from an implicit root"})
		}
		if !back[id] {
			*f = append(*f, Finding{Check: "topology", Path: sv.path + "/nodes/" + ptrEscape(id),
				Code:    "nonexit_dead_end",
				Message: "node " + strconv.Quote(id) + " cannot reach a declared exit"})
		}
	}
	// A scope with no resolvable exits is reported once.
	if len(sv.exits) == 0 && len(sv.order) > 0 {
		*f = append(*f, Finding{Check: "topology", Path: sv.path + "/exits",
			Code: "no_exit", Message: "graph declares no exit nodes"})
	}
}

// checkOutputExits requires output bindings to name declared exits
// (§4.2): settlement reads exit packets only.
func (sv *scopeView) checkOutputExits(g map[string]any, f *[]Finding) {
	outs, ok := asObject(g["outputs"])
	if !ok {
		return
	}
	for _, k := range sortedKeys(outs) {
		if src := bindingSource(outs[k]); src != "" && src != "input" && !sv.exits[src] {
			*f = append(*f, Finding{Check: "topology",
				Path: sv.path + "/outputs/" + ptrEscape(k), Code: "output_not_exit",
				Message: "output binding " + strconv.Quote(k) + " names " + strconv.Quote(src) +
					" which is not a declared exit"})
		}
	}
}

func (sv *scopeView) bfsFrom(seed func(string) bool, adj map[string][]string) map[string]bool {
	seen := make(map[string]bool, len(sv.nodes))
	var queue []string
	for _, id := range sv.order {
		if seed(id) {
			seen[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, m := range adj[n] {
			if !seen[m] {
				seen[m] = true
				queue = append(queue, m)
			}
		}
	}
	return seen
}

// checkDominators computes strict dominators with an implicit root
// feeding every indegree-0 node. A node d dominates s when every path
// from a root to s passes through d — the only sources allowed to feed
// data into s's branch regions (§4.1).
func (sv *scopeView) checkDominators() {
	dom := make(map[string]map[string]bool, len(sv.nodes))
	all := make(map[string]bool, len(sv.nodes))
	for _, id := range sv.order {
		all[id] = true
	}
	for _, id := range sv.order {
		if len(sv.rev[id]) == 0 {
			dom[id] = map[string]bool{id: true}
		} else {
			dom[id] = maps.Clone(all)
		}
	}
	for changed := true; changed; {
		changed = false
		for _, id := range sv.order {
			preds := sv.rev[id]
			if len(preds) == 0 {
				continue
			}
			inter := maps.Clone(dom[preds[0]])
			for _, p := range preds[1:] {
				for d := range inter {
					if !dom[p][d] {
						delete(inter, d)
					}
				}
			}
			inter[id] = true
			if !maps.Equal(inter, dom[id]) {
				dom[id] = inter
				changed = true
			}
		}
	}
	sv.doms = dom
}

// dominates reports whether d strictly dominates n.
func (sv *scopeView) dominates(d, n string) bool {
	return d != n && sv.doms[n][d]
}

// checkDataEdges requires a direct edge source -> consumer for every
// binding that names a node in this scope (§4.2). The reserved "input"
// source, graph output bindings (which name exits), and repeat until
// predicates (which read completed body output) are exempt.
func (sv *scopeView) checkDataEdges(f *[]Finding) {
	hasEdge := make(map[string]bool, len(sv.edges))
	for _, e := range sv.edges {
		hasEdge[e.from+"\x00"+e.to] = true
	}
	need := func(src, consumer, path string) {
		if src == "input" {
			return
		}
		if !hasEdge[src+"\x00"+consumer] {
			*f = append(*f, Finding{Check: "topology", Path: path, Code: "missing_direct_edge",
				Message: "binding source " + strconv.Quote(src) + " has no direct edge to " + strconv.Quote(consumer)})
		}
	}
	for _, id := range sv.order {
		n := sv.nodes[id]
		np := sv.path + "/nodes/" + ptrEscape(id)
		if inputs, ok := asObject(n["inputs"]); ok {
			for _, k := range sortedKeys(inputs) {
				if src := bindingSource(inputs[k]); src != "" {
					need(src, id, np+"/inputs/"+ptrEscape(k))
				}
			}
		}
		if cases, ok := asArray(n["cases"]); ok {
			for i, cv := range cases {
				c, _ := asObject(cv)
				for _, src := range predicateSources(c["when"]) {
					need(src, id, np+"/cases/"+strconv.Itoa(i)+"/when")
				}
			}
		}
		if cands, ok := asArray(n["candidates"]); ok {
			for i, cv := range cands {
				c, _ := asObject(cv)
				if src, _ := asString(c["source"]); src != "" {
					need(src, id, np+"/candidates/"+strconv.Itoa(i)+"/source")
				}
			}
		}
		if init, ok := asObject(n["initial"]); ok {
			for _, k := range sortedKeys(init) {
				if src := bindingSource(init[k]); src != "" {
					need(src, id, np+"/initial/"+ptrEscape(k))
				}
			}
		}
	}
}

func bindingSource(v any) string {
	m, ok := asObject(v)
	if !ok {
		return ""
	}
	s, _ := asString(m["source"])
	return s
}

// predicateSources collects every source reference inside a predicate.
func predicateSources(v any) []string {
	m, ok := asObject(v)
	if !ok {
		return nil
	}
	var out []string
	if src := bindingSource(m["left"]); src != "" {
		out = append(out, src)
	}
	if src := bindingSource(m["right"]); src != "" {
		out = append(out, src)
	}
	if src := bindingSource(m["arg"]); src != "" {
		out = append(out, src)
	}
	if args, ok := asArray(m["args"]); ok {
		for _, av := range args {
			out = append(out, predicateSources(av)...)
		}
	}
	return out
}

// checkRegions verifies disjoint switch port regions converging on the
// join select (§4.1, §5.2). Region(s,p) is every node reachable from a
// port-p edge of s without passing through join j.
func (sv *scopeView) checkRegions(f *[]Finding) {
	for _, id := range sv.order {
		n := sv.nodes[id]
		if kind, _ := asString(n["kind"]); kind != "switch" {
			continue
		}
		join, _ := asString(n["join"])
		if sv.nodes[join] == nil {
			continue // bad_join already reported at decode
		}
		var ports []string
		seenPort := make(map[string]bool)
		if cases, ok := asArray(n["cases"]); ok {
			for _, cv := range cases {
				if p, ok := asString(asObjectOr(cv)["port"]); ok && !seenPort[p] {
					seenPort[p] = true
					ports = append(ports, p)
				}
			}
		}
		if dp, ok := asString(n["default_port"]); ok && dp != "" && !seenPort[dp] {
			seenPort[dp] = true
			ports = append(ports, dp)
		}

		regions := make(map[string]map[string]bool, len(ports))
		allMembers := make(map[string]string) // node -> owning port
		for _, p := range ports {
			var seeds []string
			for _, e := range sv.edges {
				if e.from == id && e.port == p {
					seeds = append(seeds, e.to)
				}
			}
			if len(seeds) == 0 {
				*f = append(*f, Finding{Check: "topology", Path: sv.path + "/nodes/" + ptrEscape(id),
					Code:    "missing_port_target",
					Message: "switch " + strconv.Quote(id) + " port " + strconv.Quote(p) + " has no target"})
			}
			regions[p] = sv.regionFrom(seeds, join)
			for m := range regions[p] {
				if prev, dup := allMembers[m]; dup {
					*f = append(*f, Finding{Check: "topology", Path: sv.path + "/nodes/" + ptrEscape(m),
						Code: "region_overlap",
						Message: "node " + strconv.Quote(m) + " is reachable from ports " +
							strconv.Quote(prev) + " and " + strconv.Quote(p) + " of switch " + strconv.Quote(id)})
				} else {
					allMembers[m] = p
				}
			}
		}

		for _, p := range ports {
			for m := range regions[p] {
				if sv.exits[m] {
					*f = append(*f, Finding{Check: "topology", Path: sv.path + "/nodes/" + ptrEscape(m),
						Code: "region_contains_exit",
						Message: "exit " + strconv.Quote(m) + " sits inside region " +
							strconv.Quote(p) + " of switch " + strconv.Quote(id) +
							"; exits must follow the select"})
				}
				if !sv.reaches(m, join) {
					*f = append(*f, Finding{Check: "topology", Path: sv.path + "/nodes/" + ptrEscape(m),
						Code: "region_not_joined",
						Message: "region node " + strconv.Quote(m) + " cannot reach join " +
							strconv.Quote(join) + " of switch " + strconv.Quote(id)})
				}
			}
		}

		for _, e := range sv.edges {
			if e.to == id {
				continue // edges INTO the switch are unconstrained
			}
			owner, inRegion := allMembers[e.to]
			if inRegion {
				if e.from == id {
					continue // the port-labelled seed edge itself
				}
				if _, ok := allMembers[e.from]; !ok || allMembers[e.from] != owner {
					if e.from == join || sv.dominates(e.from, id) {
						continue // ancestor data enters without activation route
					}
					*f = append(*f, Finding{Check: "topology", Path: sv.path, Code: "region_entry_illegal",
						Message: "edge " + strconv.Quote(e.from) + " -> " + strconv.Quote(e.to) +
							" enters region " + strconv.Quote(owner) + " of switch " + strconv.Quote(id) +
							" from a node that does not dominate it"})
				}
				continue
			}
			if p, ok := allMembers[e.from]; ok && e.to != join {
				*f = append(*f, Finding{Check: "topology", Path: sv.path, Code: "region_crossing",
					Message: "edge " + strconv.Quote(e.from) + " -> " + strconv.Quote(e.to) +
						" leaves region " + strconv.Quote(p) + " of switch " + strconv.Quote(id) +
						" without passing join " + strconv.Quote(join)})
				continue
			}
			if e.to == join {
				if _, ok := allMembers[e.from]; !ok && e.from != id {
					*f = append(*f, Finding{Check: "topology", Path: sv.path, Code: "region_crossing",
						Message: "edge " + strconv.Quote(e.from) + " -> " + strconv.Quote(e.to) +
							" feeds select " + strconv.Quote(join) + " from outside the regions of switch " +
							strconv.Quote(id)})
				}
			}
		}
	}
}

// regionFrom returns nodes reachable from seeds without entering join.
func (sv *scopeView) regionFrom(seeds []string, join string) map[string]bool {
	region := make(map[string]bool)
	queue := append([]string(nil), seeds...)
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if n == join || region[n] || sv.nodes[n] == nil {
			continue
		}
		region[n] = true
		queue = append(queue, sv.fwd[n]...)
	}
	return region
}

// reaches reports whether from can reach target over forward edges.
func (sv *scopeView) reaches(from, target string) bool {
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, m := range sv.fwd[n] {
			if m == target {
				return true
			}
			if !seen[m] {
				seen[m] = true
				queue = append(queue, m)
			}
		}
	}
	return false
}

// scopeActivations computes the conservative structural activation
// bound: each node's max attempts, with repeat bodies multiplied by
// the iteration bound and the repeat's own attempts (§7.4).
func scopeActivations(g map[string]any, limits LimitsRef) int {
	total := 0
	for _, nv := range asArrayOr(g["nodes"]) {
		n, ok := asObject(nv)
		if !ok {
			continue
		}
		attempts := nodeAttempts(n, limits)
		total += attempts
		if kind, _ := asString(n["kind"]); kind != "repeat" {
			continue
		}
		iters, _ := asInt(n["max_iterations"])
		if limits.MaxIterations > 0 && iters > int64(limits.MaxIterations) {
			iters = int64(limits.MaxIterations)
		}
		if iters <= 0 {
			continue
		}
		if body, ok := asObject(n["body"]); ok {
			total += scopeActivations(body, limits) * int(iters) * attempts
		}
	}
	return total
}

func nodeAttempts(n map[string]any, limits LimitsRef) int {
	r, ok := asObject(n["retry"])
	if !ok {
		return 1
	}
	a, _ := asInt(r["max_attempts"])
	if a <= 0 {
		return 1
	}
	if limits.MaxAttemptsPerCall > 0 && a > int64(limits.MaxAttemptsPerCall) {
		a = int64(limits.MaxAttemptsPerCall)
	}
	return int(a)
}
