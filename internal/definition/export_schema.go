package definition

import (
	"encoding/json"
	"slices"
)

// DefinitionSchema projects the decoder's executable vocabulary into JSON
// Schema for authoring clients. The strict decoder and semantic validator
// remain authoritative for duplicate keys, references and topology.
func DefinitionSchema() json.RawMessage {
	def := object(defFields, []string{"schema_version", "graph"}, map[string]any{
		"schema_version": map[string]any{"const": "inofy.workflow/v1"},
		"inputs_schema":  map[string]any{"type": "object"},
		"outputs_schema": map[string]any{"type": "object"},
		"graph":          schemaRef("graph"),
		"limits":         object(limitsFields, nil, integerProperties(limitsFields)),
	})
	def["$defs"] = map[string]any{
		"graph": object(graphFields, []string{"nodes", "edges", "exits"}, map[string]any{
			"nodes":   array(schemaRef("node")),
			"edges":   array(schemaRef("edge")),
			"exits":   array(map[string]any{"type": "string"}),
			"outputs": bindingMap(),
		}),
		"node":    nodeSchema(),
		"edge":    object(edgeFields, []string{"from", "to"}, stringProperties(edgeFields)),
		"binding": bindingSchema(),
		"predicate": object(fields("op", "left", "right", "pointer", "args", "arg"), []string{"op"}, map[string]any{
			"op":      map[string]any{"enum": []string{"eq", "ne", "lt", "lte", "gt", "gte", "exists", "all", "any", "not"}},
			"left":    schemaRef("binding"),
			"right":   schemaRef("binding"),
			"pointer": map[string]any{"type": "string"},
			"args":    array(schemaRef("predicate")),
			"arg":     schemaRef("predicate"),
		}),
	}
	raw, _ := json.Marshal(def) // All schema values above are JSON primitives.
	return raw
}

func schemaRef(name string) map[string]any { return map[string]any{"$ref": "#/$defs/" + name} }

func array(item any) map[string]any { return map[string]any{"type": "array", "items": item} }

func object(allowed map[string]bool, required []string, properties map[string]any) map[string]any {
	props := make(map[string]any, len(allowed))
	for name := range allowed {
		if shape, ok := properties[name]; ok {
			props[name] = shape
		} else {
			props[name] = map[string]any{}
		}
	}
	result := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) != 0 {
		result["required"] = required
	}
	return result
}

func stringProperties(allowed map[string]bool) map[string]any {
	props := make(map[string]any, len(allowed))
	for name := range allowed {
		props[name] = map[string]any{"type": "string"}
	}
	return props
}

func integerProperties(allowed map[string]bool) map[string]any {
	props := make(map[string]any, len(allowed))
	for name := range allowed {
		props[name] = map[string]any{"type": "integer", "minimum": 0}
	}
	return props
}

func bindingMap() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": schemaRef("binding")}
}

func bindingSchema() map[string]any {
	return map[string]any{"oneOf": []any{
		object(map[string]bool{"literal": bindingFields["literal"]}, []string{"literal"}, nil),
		object(map[string]bool{"source": bindingFields["source"], "pointer": bindingFields["pointer"]},
			[]string{"source", "pointer"}, stringProperties(bindingFields)),
	}}
}

func nodeSchema() map[string]any {
	variants := make([]any, 0, len(nodeFields))
	kinds := make([]string, 0, len(nodeFields))
	for kind := range nodeFields {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	for _, kind := range kinds {
		shape := nodeFields[kind]
		props := map[string]any{
			"id":             map[string]any{"type": "string", "minLength": 1, "pattern": "^[^$/][^/]*$"},
			"kind":           map[string]any{"const": kind},
			"type":           map[string]any{"type": "string"},
			"config":         map[string]any{},
			"inputs":         bindingMap(),
			"timeout_ms":     map[string]any{"type": "integer", "minimum": 0},
			"retry":          object(fields("max_attempts", "delay_ms"), []string{"max_attempts"}, integerProperties(fields("max_attempts", "delay_ms"))),
			"on_error":       object(fields("mode", "value"), []string{"mode"}, map[string]any{"mode": map[string]any{"enum": []string{"fail", "fallback"}}}),
			"cases":          array(object(fields("port", "when"), []string{"port", "when"}, map[string]any{"port": map[string]any{"type": "string"}, "when": schemaRef("predicate")})),
			"default_port":   map[string]any{"type": "string"},
			"join":           map[string]any{"type": "string"},
			"switch":         map[string]any{"type": "string"},
			"candidates":     array(object(fields("source", "pointer"), []string{"source", "pointer"}, stringProperties(fields("source", "pointer")))),
			"fallback":       schemaRef("binding"),
			"initial":        bindingMap(),
			"state_schema":   map[string]any{"type": "object"},
			"body":           schemaRef("graph"),
			"max_iterations": map[string]any{"type": "integer", "minimum": 1},
			"until":          schemaRef("predicate"),
		}
		variants = append(variants, object(shape.allowed, append([]string{"id", "kind"}, shape.required...), props))
	}
	return map[string]any{"oneOf": variants}
}
