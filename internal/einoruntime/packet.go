// Packet plumbing for the Eino adapter: node outputs travel as whole
// packets and bindings resolve inside the receiving node's wrapper
// (architecture §7.2 step 4). A packet is the node's decoded output
// under the "out" key; a switch additionally carries its chosen port.
package einoruntime

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/ProjectViVy/inofy/internal/definition"
)

// Error is the internal run/compile error the root package adapts into
// the public inofy.Error contract.
type Error struct {
	Code    ErrorCode
	Path    string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("%s at %s: %s", e.Code, e.Path, e.Message)
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

type ErrorCode string

const (
	ErrBindingMissing      ErrorCode = "binding_missing"
	ErrSchemaMismatch      ErrorCode = "schema_mismatch"
	ErrNodeFailed          ErrorCode = "node_failed"
	ErrBudgetExceeded      ErrorCode = "budget_exceeded"
	ErrDeadlineExceeded    ErrorCode = "deadline_exceeded"
	ErrUnsupportedFeature  ErrorCode = "unsupported_feature"
	ErrInvalidDefinition   ErrorCode = "invalid_definition"
	ErrSelectZeroCandidate ErrorCode = "select_zero_candidate"
	ErrSelectAmbiguous     ErrorCode = "select_ambiguous"
)

const packetOutKey = "out"
const packetPortKey = "port"

func packetOf(v any) map[string]any {
	return map[string]any{packetOutKey: v}
}

func packetValue(packet map[string]any) any {
	return packet[packetOutKey]
}

// decodeJSON decodes raw JSON preserving integer precision (UseNumber).
func decodeJSON(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// resolvePointer applies an RFC 6901 JSON Pointer to a decoded value.
func resolvePointer(doc any, ptr string) (any, error) {
	if ptr == "" {
		return doc, nil
	}
	if !strings.HasPrefix(ptr, "/") {
		return nil, fmt.Errorf("invalid pointer %q", ptr)
	}
	cur := doc
	for _, seg := range strings.Split(ptr[1:], "/") {
		seg = strings.ReplaceAll(strings.ReplaceAll(seg, "~1", "/"), "~0", "~")
		switch c := cur.(type) {
		case map[string]any:
			v, ok := c[seg]
			if !ok {
				return nil, fmt.Errorf("pointer %q missing key %q", ptr, seg)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(c) {
				return nil, fmt.Errorf("pointer %q index %q out of range", ptr, seg)
			}
			cur = c[i]
		default:
			return nil, fmt.Errorf("pointer %q descends into a scalar", ptr)
		}
	}
	return cur, nil
}

// bindValue resolves one binding document against the node's input map.
// The input map holds predecessor packets under node IDs plus the root
// input under "input".
func bindValue(b map[string]any, in map[string]any, path string) (any, error) {
	if lit, has := b["literal"]; has {
		return lit, nil
	}
	src, _ := b["source"].(string)
	if src == "" {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path,
			Message: "binding declares neither literal nor source"}
	}
	pv, ok := in[src]
	if !ok || pv == nil {
		return nil, &Error{Code: ErrBindingMissing, Path: path,
			Message: fmt.Sprintf("binding source %q produced no packet", src)}
	}
	packet, ok := pv.(map[string]any)
	if !ok {
		return nil, &Error{Code: ErrBindingMissing, Path: path,
			Message: fmt.Sprintf("binding source %q is not a packet", src)}
	}
	ptr, _ := b["pointer"].(string)
	v, err := resolvePointer(packetValue(packet), ptr)
	if err != nil {
		return nil, &Error{Code: ErrBindingMissing, Path: path,
			Message: fmt.Sprintf("binding %s: %v", src, err)}
	}
	return v, nil
}

// bindInputs resolves a node's whole inputs object. The result is a
// plain JSON object handed to the executor or predicate evaluator.
func bindInputs(inputs map[string]any, in map[string]any, path string) (map[string]any, error) {
	out := make(map[string]any, len(inputs))
	for _, k := range sortedKeysAny(inputs) {
		b, ok := inputs[k].(map[string]any)
		if !ok {
			return nil, &Error{Code: ErrInvalidDefinition, Path: path + "/" + k,
				Message: "binding is not an object"}
		}
		v, err := bindValue(b, in, path+"/"+k)
		if err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, nil
}

// evalPredicate evaluates a §5.3 predicate. Operand bindings resolve
// against the node's input map (predecessor packets + root input);
// the exists pointer applies to the node's bound input object.
func evalPredicate(pred map[string]any, bound, in map[string]any, path string) (bool, error) {
	op, _ := pred["op"].(string)
	switch op {
	case "eq", "ne", "lt", "lte", "gt", "gte":
		left, err := evalOperand(pred["left"], in, path+"/left")
		if err != nil {
			return false, err
		}
		right, err := evalOperand(pred["right"], in, path+"/right")
		if err != nil {
			return false, err
		}
		return compare(op, left, right, path)
	case "exists":
		ptr, _ := pred["pointer"].(string)
		_, err := resolvePointer(bound, ptr)
		return err == nil, nil
	case "all":
		args, _ := pred["args"].([]any)
		for i, av := range args {
			ok, err := evalPredicate(asObj(av), bound, in, fmt.Sprintf("%s/args/%d", path, i))
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	case "any":
		args, _ := pred["args"].([]any)
		for i, av := range args {
			ok, err := evalPredicate(asObj(av), bound, in, fmt.Sprintf("%s/args/%d", path, i))
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case "not":
		ok, err := evalPredicate(asObj(pred["arg"]), bound, in, path+"/arg")
		if err != nil {
			return false, err
		}
		return !ok, nil
	}
	return false, &Error{Code: ErrInvalidDefinition, Path: path,
		Message: "unknown predicate op " + strconv.Quote(op)}
}

// evalOperand resolves a predicate operand: a binding document.
func evalOperand(v any, in map[string]any, path string) (any, error) {
	b, ok := v.(map[string]any)
	if !ok {
		return nil, &Error{Code: ErrInvalidDefinition, Path: path,
			Message: "predicate operand is not a binding"}
	}
	return bindValue(b, in, path)
}

func compare(op string, left, right any, path string) (bool, error) {
	switch op {
	case "eq", "ne":
		lc, err := definition.Canonical(left)
		if err != nil {
			return false, &Error{Code: ErrInvalidDefinition, Path: path, Err: err,
				Message: "non-normalizable operand"}
		}
		rc, err := definition.Canonical(right)
		if err != nil {
			return false, &Error{Code: ErrInvalidDefinition, Path: path, Err: err,
				Message: "non-normalizable operand"}
		}
		eq := string(lc) == string(rc)
		if op == "ne" {
			return !eq, nil
		}
		return eq, nil
	}
	lf, lok := asNumber(left)
	rf, rok := asNumber(right)
	if !lok || !rok {
		return false, &Error{Code: ErrSchemaMismatch, Path: path,
			Message: fmt.Sprintf("predicate %s requires numeric operands, got %T and %T", op, left, right)}
	}
	switch op {
	case "lt":
		return lf < rf, nil
	case "lte":
		return lf <= rf, nil
	case "gt":
		return lf > rf, nil
	case "gte":
		return lf >= rf, nil
	}
	return false, &Error{Code: ErrInvalidDefinition, Path: path, Message: "unknown comparison " + op}
}

func asNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case int64:
		return float64(n), true
	}
	return 0, false
}

func asObj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func sortedKeysAny(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func sortedNodeKeys(m map[string]map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}
