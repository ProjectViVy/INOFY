// Package definition owns the strict decoding, schema validation,
// topology checks and normalized digests for inofy artifacts
// (architecture §4.1). It operates on generic decoded documents so the
// root inofy package can export typed adapters without an import cycle.
package definition

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Finding is one diagnostic emitted by the decoder/validators. It maps
// 1:1 onto inofy.Diagnostic; Check is schema|topology|capability|budget
// (authority stays host-owned and is never produced here).
type Finding struct {
	Check   string
	Path    string
	Code    string
	Message string
}

// ArtifactDoc is the decoded artifact envelope in generic form. The
// definition side has passed every strict rule; presentation unknowns
// are preserved rather than rejected.
type ArtifactDoc struct {
	Definition   map[string]any `json:"definition"`
	Presentation map[string]any `json:"presentation"`
}

// DecodeArtifactDoc strictly decodes one artifact. Any finding rejects
// the artifact: the error is non-nil whenever findings are non-empty.
// Duplicate keys are detected by a token walk before generic decoding,
// because encoding/json silently keeps the later value.
func DecodeArtifactDoc(data []byte) (*ArtifactDoc, []Finding, error) {
	var findings []Finding
	if err := scanDocument(data, &findings); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}

	var envelope map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return nil, nil, errors.New("invalid JSON: trailing data")
	}

	// Envelope carries exactly {definition, presentation}.
	for k := range envelope {
		if k != "definition" && k != "presentation" {
			findings = append(findings, Finding{
				Check: "schema", Path: "/" + ptrEscape(k), Code: "unknown_field",
				Message: "envelope field " + strconv.Quote(k) + " is not executable or presentation",
			})
		}
	}
	rawDef, ok := envelope["definition"]
	if !ok {
		return nil, nil, errors.New("artifact is missing definition")
	}
	def, ok := rawDef.(map[string]any)
	if !ok {
		return nil, nil, errors.New("definition must be a JSON object")
	}

	pres, _ := envelope["presentation"].(map[string]any)

	checkDefinition(def, []string{"definition"}, &findings)

	if len(findings) != 0 {
		return nil, findings, errors.New("artifact failed strict validation")
	}
	return &ArtifactDoc{Definition: def, Presentation: pres}, nil, nil
}

// scanDocument walks every token once, recording duplicate object keys
// at their authored JSON-Pointer path. A syntax error stops the walk.
func scanDocument(data []byte, findings *[]Finding) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	return scanValue(dec, nil, findings)
}

func scanValue(dec *json.Decoder, path []string, findings *[]Finding) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if delim, ok := tok.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for dec.More() {
				ktok, err := dec.Token()
				if err != nil {
					return err
				}
				key, _ := ktok.(string)
				kp := appendPath(path, key)
				if seen[key] {
					*findings = append(*findings, Finding{
						Check: "schema", Path: joinPath(kp), Code: "duplicate_key",
						Message: "duplicate object key " + strconv.Quote(key),
					})
				}
				seen[key] = true
				if err := scanValue(dec, kp, findings); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // consume '}'
				return err
			}
		case '[':
			for i := 0; dec.More(); i++ {
				if err := scanValue(dec, appendPath(path, strconv.Itoa(i)), findings); err != nil {
					return err
				}
			}
			if _, err := dec.Token(); err != nil { // consume ']'
				return err
			}
		}
	}
	return nil
}

func ptrEscape(s string) string {
	s = strings.ReplaceAll(s, "~", "~0")
	return strings.ReplaceAll(s, "/", "~1")
}

func appendPath(path []string, elem string) []string {
	out := make([]string, 0, len(path)+1)
	out = append(out, path...)
	out = append(out, ptrEscape(elem))
	return out
}

func joinPath(path []string) string {
	return "/" + strings.Join(path, "/")
}

// --- definition document rules (§4.1 vocabulary) ---

var defFields = map[string]bool{
	"schema_version": true, "inputs_schema": true,
	"outputs_schema": true, "graph": true, "limits": true,
}

var graphFields = map[string]bool{
	"nodes": true, "edges": true, "exits": true, "outputs": true,
}

var edgeFields = map[string]bool{"from": true, "to": true, "port": true}

var limitsFields = map[string]bool{
	"max_nodes": true, "max_edges": true, "parallelism": true,
	"max_repeat_nesting": true, "max_iterations": true,
	"max_activations": true, "max_attempts_per_call": true,
	"node_timeout_ms": true, "run_timeout_ms": true,
	"max_definition_bytes": true, "max_node_input_bytes": true,
	"max_node_output_bytes": true, "max_output_bytes_total": true,
	"max_checkpoint_bytes": true, "max_predicate_depth": true,
	"max_concurrent_runs": true, "max_pending_admissions": true,
}

// nodeFields lists the allowed keys per node kind and the required ones
// beyond id/kind. Fields belonging to another kind are rejected.
var nodeFields = map[string]struct {
	allowed  map[string]bool
	required []string
}{
	"call": {
		allowed:  fields("id", "kind", "type", "config", "inputs", "timeout_ms", "retry", "on_error"),
		required: []string{"type"},
	},
	"switch": {
		allowed:  fields("id", "kind", "inputs", "cases", "default_port", "join"),
		required: []string{"inputs", "cases", "default_port", "join"},
	},
	"select": {
		allowed:  fields("id", "kind", "switch", "candidates", "fallback"),
		required: []string{"switch", "candidates"},
	},
	"repeat": {
		allowed:  fields("id", "kind", "initial", "state_schema", "body", "max_iterations", "until", "on_error"),
		required: []string{"initial", "state_schema", "body", "max_iterations", "until"},
	},
}

var bindingFields = map[string]bool{"literal": true, "source": true, "pointer": true}

func fields(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func asObject(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func asString(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

func asArray(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

func asInt(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, false
		}
		return i, true
	case int:
		return int64(n), true
	case float64:
		if n == float64(int64(n)) {
			return int64(n), true
		}
	}
	return 0, false
}

func unknownFields(m map[string]any, allowed map[string]bool, path []string, f *[]Finding) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		if !allowed[k] {
			*f = append(*f, Finding{
				Check: "schema", Path: joinPath(appendPath(path, k)), Code: "unknown_field",
				Message: "field " + strconv.Quote(k) + " is not part of this object's vocabulary",
			})
		}
	}
}

func missingFields(m map[string]any, required []string, path []string, f *[]Finding) {
	for _, k := range required {
		if _, ok := m[k]; !ok {
			*f = append(*f, Finding{
				Check: "schema", Path: joinPath(path), Code: "missing_field",
				Message: "object is missing required field " + strconv.Quote(k),
			})
		}
	}
}

func checkDefinition(def map[string]any, path []string, f *[]Finding) {
	unknownFields(def, defFields, path, f)
	sv, ok := def["schema_version"]
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
			Message: "definition is missing required field \"schema_version\""})
	} else if s, _ := asString(sv); s != "inofy.workflow/v1" {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "schema_version")),
			Code: "unsupported_version", Message: "schema_version must be \"inofy.workflow/v1\""})
	}
	for _, k := range []string{"inputs_schema", "outputs_schema"} {
		if raw, ok := def[k]; ok {
			checkSchemaDoc(raw, appendPath(path, k), f)
		}
	}
	if raw, ok := def["limits"]; ok {
		checkLimits(raw, appendPath(path, "limits"), f)
	}
	graphRaw, ok := def["graph"]
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
			Message: "definition is missing required field \"graph\""})
		return
	}
	graph, ok := asObject(graphRaw)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "graph")),
			Code: "wrong_type", Message: "graph must be an object"})
		return
	}
	checkGraph(graph, appendPath(path, "graph"), false, f)
}

func checkLimits(v any, path []string, f *[]Finding) {
	m, ok := asObject(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "limits must be an object"})
		return
	}
	unknownFields(m, limitsFields, path, f)
	for _, k := range sortedKeys(m) {
		i, ok := asInt(m[k])
		if !ok || i < 0 {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, k)),
				Code: "invalid_value", Message: "limit " + strconv.Quote(k) + " must be a non-negative integer"})
		}
	}
}

// ref records one binding source that must resolve to a node in scope.
type ref struct {
	source string
	path   []string
}

// scope accumulates one graph level (the outer graph or one repeat
// body) so references resolve within — and only within — that level.
type scope struct {
	ids       map[string]bool
	kinds     map[string]string
	nodes     map[string]map[string]any
	ports     map[string]map[string]bool // switch id -> declared ports
	nodePath  map[string][]string        // id -> authored node path
	order     []string
	refs      []ref
	edges     []map[string]any
	edgePaths [][]string
	inBody    bool
}

func newScope(inBody bool) *scope {
	return &scope{
		ids:      map[string]bool{},
		kinds:    map[string]string{},
		nodes:    map[string]map[string]any{},
		ports:    map[string]map[string]bool{},
		nodePath: map[string][]string{},
		inBody:   inBody,
	}
}

func checkGraph(g map[string]any, path []string, inBody bool, f *[]Finding) {
	unknownFields(g, graphFields, path, f)
	sc := newScope(inBody)

	nodesRaw, ok := g["nodes"]
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
			Message: "graph is missing required field \"nodes\""})
		return
	}
	nodes, ok := asArray(nodesRaw)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "nodes")),
			Code: "wrong_type", Message: "nodes must be an array"})
		return
	}
	for i, nv := range nodes {
		np := appendPath(appendPath(path, "nodes"), strconv.Itoa(i))
		n, ok := asObject(nv)
		if !ok {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(np), Code: "wrong_type",
				Message: "node must be an object"})
			continue
		}
		checkNode(n, np, sc, f)
	}

	edges, edgesOK := asArray(g["edges"])
	if _, present := g["edges"]; !present {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
			Message: "graph is missing required field \"edges\""})
	} else if !edgesOK {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "edges")),
			Code: "wrong_type", Message: "edges must be an array"})
	}
	for i, ev := range edges {
		ep := appendPath(appendPath(path, "edges"), strconv.Itoa(i))
		e, ok := asObject(ev)
		if !ok {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(ep), Code: "wrong_type",
				Message: "edge must be an object"})
			continue
		}
		checkEdge(e, ep, sc, f)
		sc.edges = append(sc.edges, e)
		sc.edgePaths = append(sc.edgePaths, ep)
	}

	exits, exitsOK := asArray(g["exits"])
	if _, present := g["exits"]; !present {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
			Message: "graph is missing required field \"exits\""})
	} else if !exitsOK {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "exits")),
			Code: "wrong_type", Message: "exits must be an array"})
	}
	for i, xv := range exits {
		xp := appendPath(appendPath(path, "exits"), strconv.Itoa(i))
		id, ok := asString(xv)
		if !ok {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(xp), Code: "wrong_type",
				Message: "exit must be a node id string"})
			continue
		}
		if !sc.ids[id] {
			*f = append(*f, Finding{Check: "topology", Path: joinPath(xp), Code: "unknown_node_ref",
				Message: "exit " + strconv.Quote(id) + " is not a node in this graph"})
		}
	}

	if out, ok := g["outputs"]; ok {
		m, ok := asObject(out)
		if !ok {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "outputs")),
				Code: "wrong_type", Message: "outputs must be an object"})
		} else {
			for _, k := range sortedKeys(m) {
				checkBinding(m[k], appendPath(appendPath(path, "outputs"), k), sc, f, true)
			}
		}
	}

	resolveRefs(sc, f)
	checkPortEdges(sc, f)
	checkJoinLinks(sc, f)
}

func checkNode(n map[string]any, np []string, sc *scope, f *[]Finding) {
	idv, hasID := n["id"]
	id, idOK := asString(idv)
	idUsable := false
	if !hasID {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(np), Code: "missing_field",
			Message: "node is missing required field \"id\""})
	} else if !idOK || id == "" {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "id")),
			Code: "invalid_value", Message: "node id must be a non-empty string"})
	} else {
		if strings.HasPrefix(id, "$") || strings.Contains(id, "/") {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "id")),
				Code: "reserved_id", Message: "authored ids cannot use the \"$\" prefix or contain \"/\""})
		}
		if sc.ids[id] {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "id")),
				Code: "duplicate_id", Message: "node id " + strconv.Quote(id) + " is not unique in this graph"})
		} else {
			sc.ids[id] = true
			sc.nodes[id] = n
			sc.order = append(sc.order, id)
			sc.nodePath[id] = np
			idUsable = true
		}
	}

	kindv, hasKind := n["kind"]
	kind, kindOK := asString(kindv)
	if !hasKind {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(np), Code: "missing_field",
			Message: "node is missing required field \"kind\""})
		return
	}
	if !kindOK {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "kind")),
			Code: "wrong_type", Message: "node kind must be a string"})
		return
	}
	shape, known := nodeFields[kind]
	if !known {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "kind")),
			Code: "unknown_kind", Message: "kind " + strconv.Quote(kind) + " is not call|switch|select|repeat"})
		return
	}
	if idUsable {
		sc.kinds[id] = kind
	}
	if sc.inBody && kind == "repeat" {
		*f = append(*f, Finding{Check: "topology", Path: joinPath(appendPath(np, "kind")),
			Code: "nested_repeat", Message: "v0.1 supports one repeat nesting level"})
	}

	unknownFields(n, shape.allowed, np, f)
	missingFields(n, shape.required, np, f)

	switch kind {
	case "call":
		if raw, ok := n["inputs"]; ok {
			checkBindingMap(raw, appendPath(np, "inputs"), sc, f)
		}
		if raw, ok := n["retry"]; ok {
			checkRetry(raw, appendPath(np, "retry"), f)
		}
		if raw, ok := n["on_error"]; ok {
			checkErrorPolicy(raw, appendPath(np, "on_error"), f)
		}
	case "switch":
		if raw, ok := n["inputs"]; ok {
			checkBindingMap(raw, appendPath(np, "inputs"), sc, f)
		}
		declared := map[string]bool{}
		if raw, ok := n["cases"]; ok {
			checkCases(raw, appendPath(np, "cases"), sc, declared, f)
		}
		if dp, ok := asString(n["default_port"]); ok && dp != "" {
			declared[dp] = true
		}
		if idUsable {
			sc.ports[id] = declared
		}
	case "select":
		if raw, ok := n["candidates"]; ok {
			checkCandidates(raw, appendPath(np, "candidates"), sc, f)
		}
		if raw, ok := n["fallback"]; ok {
			checkBinding(raw, appendPath(np, "fallback"), sc, f, false)
		}
	case "repeat":
		if raw, ok := n["initial"]; ok {
			// initial bindings resolve in the OUTER scope: the repeat
			// node itself lives in this graph.
			checkBindingMap(raw, appendPath(np, "initial"), sc, f)
		}
		if raw, ok := n["state_schema"]; ok {
			checkSchemaDoc(raw, appendPath(np, "state_schema"), f)
		}
		if raw, ok := n["body"]; ok && raw != nil {
			if bm, ok := asObject(raw); ok {
				checkGraph(bm, appendPath(np, "body"), true, f)
			} else {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "body")),
					Code: "wrong_type", Message: "repeat body must be a graph object"})
			}
		}
		if raw, ok := n["max_iterations"]; ok {
			if i, ok := asInt(raw); !ok || i < 1 {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "max_iterations")),
					Code: "invalid_value", Message: "max_iterations must be a positive integer"})
			}
		}
		if raw, ok := n["until"]; ok {
			checkPredicate(raw, appendPath(np, "until"), sc, f)
		}
		if raw, ok := n["on_error"]; ok {
			checkErrorPolicy(raw, appendPath(np, "on_error"), f)
		}
	}
	if tv, ok := n["timeout_ms"]; ok {
		if i, ok := asInt(tv); !ok || i < 0 {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(np, "timeout_ms")),
				Code: "invalid_value", Message: "timeout_ms must be a non-negative integer"})
		}
	}
}

func checkBindingMap(v any, path []string, sc *scope, f *[]Finding) {
	m, ok := asObject(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "binding map must be an object"})
		return
	}
	for _, k := range sortedKeys(m) {
		checkBinding(m[k], appendPath(path, k), sc, f, true)
	}
}

// checkBinding enforces the union rule: exactly one of literal or
// source; pointer requires source. Explicit null literal is a value —
// key presence, not non-nullness, is the test.
func checkBinding(v any, path []string, sc *scope, f *[]Finding, recordRef bool) {
	m, ok := asObject(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "binding must be an object"})
		return
	}
	unknownFields(m, bindingFields, path, f)
	_, hasLiteral := m["literal"]
	src, hasSource := m["source"]
	_, hasPointer := m["pointer"]

	switch {
	case hasLiteral && (hasSource || hasPointer):
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "ambiguous_binding",
			Message: "binding must be exactly one of a literal or a source reference"})
	case !hasLiteral && !hasSource:
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_binding",
			Message: "binding must declare literal or source"})
	case hasSource && !hasPointer:
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_binding",
			Message: "source binding requires a JSON pointer"})
	case hasPointer && !hasSource:
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_binding",
			Message: "pointer requires a source"})
	}
	if hasSource {
		s, ok := asString(src)
		if !ok || s == "" {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "source")),
				Code: "invalid_value", Message: "binding source must be a non-empty string"})
		} else if recordRef && s != "input" {
			sc.refs = append(sc.refs, ref{source: s, path: appendPath(path, "source")})
		}
	}
}

func checkCandidates(v any, path []string, sc *scope, f *[]Finding) {
	arr, ok := asArray(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "candidates must be an array"})
		return
	}
	if len(arr) == 0 {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "invalid_value",
			Message: "select requires at least one candidate"})
	}
	for i, cv := range arr {
		cp := appendPath(path, strconv.Itoa(i))
		c, ok := asObject(cv)
		if !ok {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(cp), Code: "wrong_type",
				Message: "candidate must be an object"})
			continue
		}
		unknownFields(c, fields("source", "pointer"), cp, f)
		src, hasSource := c["source"]
		_, hasPointer := c["pointer"]
		if !hasSource {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(cp), Code: "missing_field",
				Message: "candidate is missing required field \"source\""})
		} else if s, ok := asString(src); !ok || s == "" || s == "input" {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(cp, "source")),
				Code: "invalid_value", Message: "candidate source must name a node in this graph"})
		} else {
			sc.refs = append(sc.refs, ref{source: s, path: appendPath(cp, "source")})
		}
		if !hasPointer {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(cp), Code: "missing_field",
				Message: "candidate is missing required field \"pointer\""})
		}
	}
}

func checkCases(v any, path []string, sc *scope, declared map[string]bool, f *[]Finding) {
	arr, ok := asArray(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "cases must be an array"})
		return
	}
	if len(arr) == 0 {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "invalid_value",
			Message: "switch requires at least one case"})
	}
	for i, cv := range arr {
		cp := appendPath(path, strconv.Itoa(i))
		c, ok := asObject(cv)
		if !ok {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(cp), Code: "wrong_type",
				Message: "case must be an object"})
			continue
		}
		unknownFields(c, fields("port", "when"), cp, f)
		port, pok := asString(c["port"])
		if !pok || port == "" {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(cp), Code: "missing_field",
				Message: "case is missing required field \"port\""})
		} else {
			if declared[port] {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(cp, "port")),
					Code: "duplicate_port", Message: "port " + strconv.Quote(port) + " already declared by an earlier case"})
			}
			declared[port] = true
		}
		if w, ok := c["when"]; ok {
			checkPredicate(w, appendPath(cp, "when"), sc, f)
		} else {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(cp), Code: "missing_field",
				Message: "case is missing required field \"when\""})
		}
	}
}

var predicateOps = map[string]bool{
	"eq": true, "ne": true, "lt": true, "lte": true, "gt": true, "gte": true,
	"exists": true, "all": true, "any": true, "not": true,
}

func checkPredicate(v any, path []string, sc *scope, f *[]Finding) {
	m, ok := asObject(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "predicate must be an object"})
		return
	}
	unknownFields(m, fields("op", "left", "right", "pointer", "args", "arg"), path, f)
	op, ok := asString(m["op"])
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
			Message: "predicate is missing required field \"op\""})
		return
	}
	if !predicateOps[op] {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "op")),
			Code: "unknown_op", Message: "predicate op " + strconv.Quote(op) + " is not supported"})
		return
	}
	switch op {
	case "eq", "ne", "lt", "lte", "gt", "gte":
		for _, side := range []string{"left", "right"} {
			bv, ok := m[side]
			if !ok {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
					Message: "comparison predicate requires " + strconv.Quote(side)})
				continue
			}
			checkBinding(bv, appendPath(path, side), sc, f, true)
		}
	case "exists":
		if p, ok := asString(m["pointer"]); !ok || p == "" {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
				Message: "exists predicate requires a \"pointer\" string"})
		}
	case "all", "any":
		args, ok := asArray(m["args"])
		if !ok || len(args) == 0 {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
				Message: op + " predicate requires a non-empty \"args\" array"})
			break
		}
		for i, av := range args {
			checkPredicate(av, appendPath(appendPath(path, "args"), strconv.Itoa(i)), sc, f)
		}
	case "not":
		if av, ok := m["arg"]; ok {
			checkPredicate(av, appendPath(path, "arg"), sc, f)
		} else {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
				Message: "not predicate requires \"arg\""})
		}
	}
}

func checkRetry(v any, path []string, f *[]Finding) {
	m, ok := asObject(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "retry must be an object"})
		return
	}
	unknownFields(m, fields("max_attempts", "delay_ms"), path, f)
	if _, ok := m["max_attempts"]; !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
			Message: "retry requires \"max_attempts\""})
	} else if i, ok := asInt(m["max_attempts"]); !ok || i < 1 {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "max_attempts")),
			Code: "invalid_value", Message: "max_attempts must be a positive integer"})
	}
	if dv, ok := m["delay_ms"]; ok {
		if i, ok := asInt(dv); !ok || i < 0 {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "delay_ms")),
				Code: "invalid_value", Message: "delay_ms must be a non-negative integer"})
		}
	}
}

func checkErrorPolicy(v any, path []string, f *[]Finding) {
	m, ok := asObject(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "on_error must be an object"})
		return
	}
	unknownFields(m, fields("mode", "value"), path, f)
	mode, ok := asString(m["mode"])
	if !ok || (mode != "fail" && mode != "fallback") {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "mode")),
			Code: "invalid_value", Message: "on_error mode must be \"fail\" or \"fallback\""})
		return
	}
	if mode == "fallback" {
		if _, ok := m["value"]; !ok {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "missing_field",
				Message: "fallback requires a \"value\""})
		}
	} else if _, ok := m["value"]; ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, "value")),
			Code: "forbidden_value", Message: "value is only permitted with mode \"fallback\""})
	}
}

func checkEdge(e map[string]any, ep []string, sc *scope, f *[]Finding) {
	unknownFields(e, edgeFields, ep, f)
	for _, k := range []string{"from", "to"} {
		sv, ok := e[k]
		s, ok2 := asString(sv)
		if !ok || !ok2 || s == "" {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(ep), Code: "missing_field",
				Message: "edge is missing required field " + strconv.Quote(k)})
			continue
		}
		if !sc.ids[s] {
			*f = append(*f, Finding{Check: "topology", Path: joinPath(appendPath(ep, k)),
				Code: "unknown_node_ref", Message: "edge " + k + " " + strconv.Quote(s) + " is not a node in this graph"})
		}
	}
}

// checkPortEdges applies the port rules once nodes are known: switch
// edges require a declared port; edges from other kinds forbid one.
func checkPortEdges(sc *scope, f *[]Finding) {
	for i, e := range sc.edges {
		ep := sc.edgePaths[i]
		from, _ := asString(e["from"])
		pv, hasPort := e["port"]
		if sc.kinds[from] == "switch" {
			if !hasPort {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(ep), Code: "missing_port",
					Message: "edges out of a switch require a port label"})
				continue
			}
			port, _ := asString(pv)
			if !sc.ports[from][port] {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(ep, "port")),
					Code: "undeclared_port", Message: "port " + strconv.Quote(port) + " is not declared by switch " + strconv.Quote(from)})
			}
		} else if hasPort {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(ep, "port")),
				Code: "illegal_port", Message: "port labels are only valid on edges leaving a switch"})
		}
	}
}

// checkJoinLinks verifies switch.join and select.switch name nodes of
// the expected kind in the same graph, and that they are mutual.
func checkJoinLinks(sc *scope, f *[]Finding) {
	for _, id := range sc.order {
		node := sc.nodePath[id]
		nObj := sc.nodes[id]
		switch sc.kinds[id] {
		case "switch":
			j, ok := asString(nObj["join"])
			if !ok {
				continue // missing_field already reported
			}
			if sc.kinds[j] != "select" {
				*f = append(*f, Finding{Check: "topology", Path: joinPath(appendPath(node, "join")),
					Code: "bad_join", Message: "switch join must name a select node in this graph"})
			}
		case "select":
			s, ok := asString(nObj["switch"])
			if !ok {
				continue
			}
			if sc.kinds[s] != "switch" {
				*f = append(*f, Finding{Check: "topology", Path: joinPath(appendPath(node, "switch")),
					Code: "bad_join", Message: "select switch must name a switch node in this graph"})
				continue
			}
			if swObj := sc.nodes[s]; swObj != nil {
				if j, _ := asString(swObj["join"]); j != id {
					*f = append(*f, Finding{Check: "topology", Path: joinPath(appendPath(node, "switch")),
						Code: "bad_join", Message: "select " + strconv.Quote(id) + " is not the join of switch " + strconv.Quote(s)})
				}
			}
		}
	}
}

func resolveRefs(sc *scope, f *[]Finding) {
	for _, r := range sc.refs {
		if !sc.ids[r.source] {
			*f = append(*f, Finding{Check: "topology", Path: joinPath(r.path),
				Code: "unknown_node_ref", Message: "binding source " + strconv.Quote(r.source) + " is not a node in this graph"})
		}
	}
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
