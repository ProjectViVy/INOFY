// inofy-normal-v1: named canonical encoding (architecture §4.3). Not
// RFC 8785 — an INOFY-owned encoding: sorted object keys, graph nodes
// sorted by id, edges by (from, port, to), exits and select candidates
// sorted as sets, authored order preserved for switch cases and
// predicate args. Numbers decode to finite binary64; integral values
// beyond the JavaScript safe range and non-finite values are rejected;
// negative zero becomes zero. Strings keep their code points, encoding
// compatible with encoding/json + SetEscapeHTML(false), no indentation,
// no trailing newline.
package definition

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
)

// safeIntLimit is the largest exactly-representable JS integer.
const safeIntLimit = int64(9007199254740991)

// NormalizeDefinitionDoc canonicalizes a decoded definition document.
// The document is mutated: semantically unordered arrays are sorted in
// place before encoding.
func NormalizeDefinitionDoc(doc map[string]any) ([]byte, error) {
	sortDefinitionArrays(doc)
	return Canonical(doc)
}

// NormalizeArtifactDoc canonicalizes an artifact envelope; only the
// definition side receives structural array sorting.
func NormalizeArtifactDoc(doc map[string]any) ([]byte, error) {
	if def, ok := doc["definition"].(map[string]any); ok {
		sortDefinitionArrays(def)
	}
	return Canonical(doc)
}

// Canonical encodes a generic JSON value in inofy-normal-v1 form.
func Canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// sortDefinitionArrays walks the definition document and sorts every
// semantically unordered array: graph nodes by id, edges by
// (from, port, to), exits and candidates as sets. Repeat bodies are
// graphs and get the same treatment.
func sortDefinitionArrays(m map[string]any) {
	if nodes, ok := m["nodes"].([]any); ok {
		slices.SortFunc(nodes, func(a, b any) int {
			ai, _ := asString(asObjectOr(a)["id"])
			bi, _ := asString(asObjectOr(b)["id"])
			return cmpString(ai, bi)
		})
	}
	if edges, ok := m["edges"].([]any); ok {
		slices.SortFunc(edges, func(a, b any) int {
			am, bm := asObjectOr(a), asObjectOr(b)
			if c := cmpString(strOr(am["from"]), strOr(bm["from"])); c != 0 {
				return c
			}
			if c := cmpString(strOr(am["port"]), strOr(bm["port"])); c != 0 {
				return c
			}
			return cmpString(strOr(am["to"]), strOr(bm["to"]))
		})
	}
	if exits, ok := m["exits"].([]any); ok {
		slices.SortFunc(exits, func(a, b any) int {
			return cmpString(strOr(a), strOr(b))
		})
	}
	if cands, ok := m["candidates"].([]any); ok {
		slices.SortFunc(cands, func(a, b any) int {
			am, bm := asObjectOr(a), asObjectOr(b)
			if c := cmpString(strOr(am["source"]), strOr(bm["source"])); c != 0 {
				return c
			}
			return cmpString(strOr(am["pointer"]), strOr(bm["pointer"]))
		})
	}
	for _, mv := range m {
		walkJSON(mv, sortDefinitionArrays)
	}
}

func walkJSON(v any, fn func(map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		fn(t)
		for _, mv := range t {
			walkJSON(mv, fn)
		}
	case []any:
		for _, e := range t {
			walkJSON(e, fn)
		}
	}
}

func asObjectOr(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func strOr(v any) string {
	s, _ := v.(string)
	return s
}

func cmpString(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writeJSONString(buf, t)
	case json.Number:
		if err := writeNumber(buf, t.String()); err != nil {
			return err
		}
	case float64:
		if err := writeFloat(buf, t); err != nil {
			return err
		}
	case int:
		buf.WriteString(strconv.Itoa(t))
	case int64:
		buf.WriteString(strconv.FormatInt(t, 10))
	case map[string]any:
		buf.WriteByte('{')
		keys := sortedKeys(t)
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeJSONString(buf, k)
			buf.WriteByte(':')
			if err := writeCanonical(buf, t[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	default:
		return fmt.Errorf("inofy-normal-v1: unsupported Go type %T", v)
	}
	return nil
}

func writeNumber(buf *bytes.Buffer, s string) error {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if math.IsInf(f, 0) { // range overflow parses to ±Inf
			return fmt.Errorf("inofy-normal-v1: non_finite number %q", s)
		}
		return fmt.Errorf("inofy-normal-v1: invalid number %q", s)
	}
	return writeFloat(buf, f)
}

// writeFloat emits finite binary64 in the canonical form: integral
// values within the safe range emit integer form; non-finite and
// out-of-range integral values are rejected; -0 becomes 0.
func writeFloat(buf *bytes.Buffer, f float64) error {
	if math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("inofy-normal-v1: non_finite number")
	}
	if f == math.Trunc(f) {
		if f > float64(safeIntLimit) || f < -float64(safeIntLimit) {
			return fmt.Errorf("inofy-normal-v1: unsafe_integer %v", f)
		}
		buf.WriteString(strconv.FormatInt(int64(f), 10))
		return nil
	}
	buf.WriteString(formatFloatShort(f))
	return nil
}

// formatFloatShort emits the shortest round-trip form with normalized
// exponent styling (e.g. "1e-7", not "1e-07").
func formatFloatShort(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	if i := indexByte(s, 'e'); i >= 0 {
		mant, exp := s[:i], s[i+1:]
		sign := ""
		if len(exp) > 0 && (exp[0] == '+' || exp[0] == '-') {
			sign = string(exp[0])
			exp = exp[1:]
		}
		for len(exp) > 1 && exp[0] == '0' {
			exp = exp[1:]
		}
		if sign == "+" {
			sign = ""
		}
		return mant + "e" + sign + exp
	}
	return s
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// writeJSONString mirrors encoding/json string output with
// SetEscapeHTML(false): control characters escape as \uXXXX or the
// short forms; U+2028/U+2029 still escape (encoder parity); all other
// UTF-8 passes through unmodified.
func writeJSONString(buf *bytes.Buffer, s string) {
	const hex = "0123456789abcdef"
	buf.WriteByte('"')
	for i := 0; i < len(s); {
		b := s[i]
		if b < 0x80 {
			switch b {
			case '"', '\\':
				buf.WriteByte('\\')
				buf.WriteByte(b)
			case '\n':
				buf.WriteString(`\n`)
			case '\r':
				buf.WriteString(`\r`)
			case '\t':
				buf.WriteString(`\t`)
			default:
				if b < 0x20 {
					buf.WriteString(`\u00`)
					buf.WriteByte(hex[b>>4])
					buf.WriteByte(hex[b&0xf])
				} else {
					buf.WriteByte(b)
				}
			}
			i++
			continue
		}
		// Multi-byte UTF-8: pass through, except the two JSON-unsafe
		// line separators the encoder always escapes.
		if b == 0xe2 && i+2 < len(s) && s[i+1] == 0x80 && (s[i+2] == 0xa8 || s[i+2] == 0xa9) {
			if s[i+2] == 0xa8 {
				buf.WriteString(`\u2028`)
			} else {
				buf.WriteString(`\u2029`)
			}
			i += 3
			continue
		}
		buf.WriteByte(b)
		i++
	}
	buf.WriteByte('"')
}
