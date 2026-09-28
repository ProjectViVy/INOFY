// Bounded repeat: a repeat node compiles to a cyclic Eino Graph whose
// serializable local state counts iterations and carries the current
// loop state packet. The inline body compiles through the same scope
// builder as the outer DAG (architecture §5.4, §7.2 step 7).
package einoruntime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"

	"github.com/ProjectViVy/inofy/internal/definition"
)

// repeatCtrl is the serializable controller state the cyclic graph
// checkpoint machinery owns (S06). Iteration is the count of completed
// body iterations; StatePacket is the packet emitted to the body as
// its "input" on the next pass.
type repeatCtrl struct {
	Iter        int
	StatePacket map[string]any
}

func init() {
	schema.RegisterName[*repeatCtrl]("inofy_repeat_ctrl")
}

// ctlPacket is the body-facing "input": the loop state as the packet
// value plus container metadata body nodes use to derive their logical
// path (<container>/<zero-based-iteration>/<node>).
func ctlPacket(state any, iter int, path string) map[string]any {
	return map[string]any{
		packetOutKey: state,
		"$iter":      iter,
		"$path":      path,
	}
}

// runtimePath returns the node's logical path: inside a repeat body
// the container path and zero-based iteration arrive in the input
// packet; outside it is the compiled doc path.
func runtimePath(in map[string]any, docPath, nodeID string) string {
	p, ok := in["input"].(map[string]any)
	if !ok {
		return docPath
	}
	cp, _ := p["$path"].(string)
	it, _ := p["$iter"].(int)
	if cp == "" {
		return docPath
	}
	return fmt.Sprintf("%s/%d/%s", cp, it, nodeID)
}

// buildRepeat compiles one repeat node into an Eino cyclic Graph:
//
//	START → init → body(Workflow) → ctl → branch{body, END}
//
// init resolves `initial` once in the outer scope. ctl validates each
// completed body output against state_schema, evaluates `until`
// against it, and either emits the final packet (done) or the next
// state packet. Reaching max_iterations without until produces
// iteration_limit (or the declared on_error fallback).
func (b *scopeBuilder) buildRepeat(id, path string, n map[string]any) (compose.AnyGraph, error) {
	if b.containerDepth > 0 {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path,
			Message: "repeat nesting beyond one level"}
	}
	maxIter := asIntField(n["max_iterations"])
	if maxIter <= 0 {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path + "/max_iterations",
			Message: "repeat requires max_iterations >= 1"}
	}
	initial, _ := n["initial"].(map[string]any)
	stateSchema, _ := n["state_schema"]
	var schemaRaw []byte
	if stateSchema != nil {
		raw, err := json.Marshal(stateSchema)
		if err != nil {
			return nil, &Error{Code: ErrInvalidDefinition, Path: path + "/state_schema", Err: err}
		}
		schemaRaw = raw
	}
	until, _ := n["until"].(map[string]any)
	if until == nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path + "/until",
			Message: "repeat requires an until predicate"}
	}
	bodyDoc, _ := n["body"].(map[string]any)
	if bodyDoc == nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path + "/body",
			Message: "repeat requires an inline body graph"}
	}
	outputs, _ := bodyDoc["outputs"].(map[string]any)
	onError, hasOnError := n["on_error"].(map[string]any)

	// Compile the body through the same scope builder; container depth
	// bounds nesting to one level.
	inner := &scopeBuilder{types: b.types, containerDepth: b.containerDepth + 1}
	bodyWf, _, err := inner.buildScope(bodyDoc, path+"/body")
	if err != nil {
		return nil, err
	}

	g := compose.NewGraph[map[string]any, map[string]any](
		compose.WithGenLocalState(func(ctx context.Context) *repeatCtrl {
			return &repeatCtrl{}
		}))

	// init: first pass only. Resolves `initial` bindings in the outer
	// scope into the seed state, validates it, and stores the packet so
	// ctl's first pass can bind body outputs to source "input".
	if err := g.AddLambdaNode("init", compose.InvokableLambda(
		func(ctx context.Context, in map[string]any) (map[string]any, error) {
			state, err := bindInputs(initial, in, path+"/initial")
			if err != nil {
				return nil, err
			}
			if len(schemaRaw) > 0 {
				if err := definition.ValidateValueJSON(schemaRaw, state); err != nil {
					return nil, &Error{Code: ErrSchemaMismatch, Path: path + "/initial",
						Err: err, Message: "initial state violates state_schema"}
				}
			}
			return map[string]any{"input": ctlPacket(state, 0, path)}, nil
		}),
		compose.WithStatePostHandler[map[string]any, *repeatCtrl](
			func(ctx context.Context, out map[string]any, st *repeatCtrl) (map[string]any, error) {
				if pkt, ok := out["input"].(map[string]any); ok {
					st.StatePacket = pkt
				}
				return out, nil
			})); err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path, Err: err, Message: "repeat init"}
	}
	if err := g.AddGraphNode("body", bodyWf); err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path + "/body", Err: err}
	}

	// ctl: after each completed body iteration. The pre-handler injects
	// the carried state packet so body outputs may bind source "input";
	// the post-handler counts iterations and stamps the next packet.
	if err := g.AddLambdaNode("ctl", compose.InvokableLambda(
		func(ctx context.Context, in map[string]any) (map[string]any, error) {
			resolved, err := bindInputs(outputs, in, path+"/body/outputs")
			if err != nil {
				return nil, err
			}
			if len(schemaRaw) > 0 {
				if err := definition.ValidateValueJSON(schemaRaw, resolved); err != nil {
					return nil, &Error{Code: ErrSchemaMismatch, Path: path + "/body/outputs",
						Err: err, Message: "body output violates state_schema"}
				}
			}
			done, err := evalPredicate(until, resolved, in, path+"/until")
			if err != nil {
				return nil, err
			}
			if done {
				return map[string]any{packetOutKey: resolved, "$done": true}, nil
			}
			return map[string]any{"input": ctlPacket(resolved, -1, path), "$done": false}, nil
		}),
		compose.WithStatePreHandler[map[string]any, *repeatCtrl](
			func(ctx context.Context, in map[string]any, st *repeatCtrl) (map[string]any, error) {
				if st.StatePacket != nil {
					cp := make(map[string]any, len(in)+1)
					for k, v := range in {
						cp[k] = v
					}
					cp["input"] = st.StatePacket
					return cp, nil
				}
				return in, nil
			}),
		compose.WithStatePostHandler[map[string]any, *repeatCtrl](
			func(ctx context.Context, out map[string]any, st *repeatCtrl) (map[string]any, error) {
				st.Iter++
				done, _ := out["$done"].(bool)
				if done {
					return out, nil
				}
				if st.Iter >= maxIter {
					if hasOnError {
						if lit, ok := onError["literal"]; ok {
							return map[string]any{packetOutKey: lit, "$done": true}, nil
						}
					}
					return nil, &Error{Code: ErrIterationLimit, Path: path,
						Message: fmt.Sprintf("repeat reached max_iterations=%d without until", maxIter)}
				}
				if pkt, ok := out["input"].(map[string]any); ok {
					pkt["$iter"] = st.Iter
					st.StatePacket = pkt
				}
				return out, nil
			})); err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path, Err: err, Message: "repeat ctl"}
	}

	if err := g.AddEdge(compose.START, "init"); err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path, Err: err}
	}
	if err := g.AddEdge("init", "body"); err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path, Err: err}
	}
	if err := g.AddEdge("body", "ctl"); err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path, Err: err}
	}
	if err := g.AddBranch("ctl", compose.NewGraphBranch(
		func(ctx context.Context, in map[string]any) (string, error) {
			done, _ := in["$done"].(bool)
			if done {
				return compose.END, nil
			}
			return "body", nil
		}, map[string]bool{"body": true, compose.END: true})); err != nil {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path, Err: err}
	}
	return g, nil
}

// asIntField reads a numeric field decoded via UseNumber or a native
// integer from a doc map.
func asIntField(v any) int {
	switch n := v.(type) {
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}
