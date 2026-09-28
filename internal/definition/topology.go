// Catalog-facing and structural validation over an already-decoded
// definition document. S02 T1 lands the capability checks; T3 lands
// bounded graph admission (reachability, regions, activation bounds).
package definition

import (
	"encoding/json"
	"strconv"
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
// definition. S02 T1 leaves a stub; T3 implements reachability, branch
// regions, data-edge rules and activation bounds.
func ValidateTopologyDoc(doc map[string]any, limits LimitsRef) []Finding {
	return nil
}
