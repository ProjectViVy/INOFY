// Portable JSON Schema profile (architecture §4.1): the v0.1 UI can
// render object properties/required/additionalProperties, primitive
// types, arrays/items, enum and simple numeric/string bounds. Anything
// outside that profile is rejected explicitly; remote references are
// never fetched.
package definition

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// schemaKeywords is the portable profile's allowed keyword set.
var schemaKeywords = fields(
	"type", "properties", "required", "additionalProperties",
	"items", "enum", "const",
	"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
	"minLength", "maxLength", "pattern",
	"minItems", "maxItems", "minProperties", "maxProperties",
	"title", "description", "default", "format",
	"$ref", "$defs", "definitions",
)

var schemaTypes = map[string]bool{
	"object": true, "array": true, "string": true,
	"number": true, "integer": true, "boolean": true, "null": true,
}

// schemaContainers are keywords whose values hold nested schemas.
var schemaContainers = map[string]bool{
	"properties": true, "$defs": true, "definitions": true,
}

// checkSchemaDoc validates one schema document against the portable
// profile. Only local references ("#..." pointers) are allowed.
func checkSchemaDoc(v any, path []string, f *[]Finding) {
	m, ok := asObject(v)
	if !ok {
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "schema must be an object"})
		return
	}
	checkSchemaObj(m, path, f)
}

func checkSchemaObj(m map[string]any, path []string, f *[]Finding) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := m[k]
		kp := appendPath(path, k)
		if !schemaKeywords[k] {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(kp), Code: "unsupported_schema",
				Message: "schema keyword " + strconv.Quote(k) + " is outside the portable profile"})
			continue
		}
		switch k {
		case "$ref":
			s, _ := asString(v)
			if !strings.HasPrefix(s, "#") {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(kp), Code: "remote_schema_ref",
					Message: "schema references must be local (\"#/...\"); remote loading is disabled"})
			}
		case "type":
			checkSchemaType(v, kp, f)
		case "properties", "$defs", "definitions":
			sub, ok := asObject(v)
			if !ok {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(kp), Code: "wrong_type",
					Message: k + " must be an object"})
				continue
			}
			for _, pk := range sortedKeys(sub) {
				checkSchemaValue(sub[pk], appendPath(kp, pk), f)
			}
		case "items", "additionalProperties":
			checkSchemaValue(v, kp, f)
		case "required":
			arr, ok := asArray(v)
			if !ok {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(kp), Code: "wrong_type",
					Message: "required must be an array of strings"})
				continue
			}
			for i, rv := range arr {
				if _, ok := asString(rv); !ok {
					*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(kp, strconv.Itoa(i))),
						Code: "wrong_type", Message: "required entries must be strings"})
				}
			}
		case "enum", "const":
			// any JSON value is legal; dup-key scan already ran.
		default:
			// bounds keywords: leave value checking to the compiler.
		}
	}
}

// checkSchemaValue accepts either a schema object or a boolean schema.
func checkSchemaValue(v any, path []string, f *[]Finding) {
	switch v.(type) {
	case bool:
		return
	case map[string]any:
		checkSchemaObj(v.(map[string]any), path, f)
	default:
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "schema must be an object or boolean"})
	}
}

func checkSchemaType(v any, path []string, f *[]Finding) {
	switch t := v.(type) {
	case string:
		if !schemaTypes[t] {
			*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "invalid_value",
				Message: "schema type " + strconv.Quote(t) + " is not a JSON primitive type"})
		}
	case []any:
		for i, tv := range t {
			s, ok := asString(tv)
			if !ok || !schemaTypes[s] {
				*f = append(*f, Finding{Check: "schema", Path: joinPath(appendPath(path, strconv.Itoa(i))),
					Code: "invalid_value", Message: "schema type entries must be JSON primitive types"})
			}
		}
	default:
		*f = append(*f, Finding{Check: "schema", Path: joinPath(path), Code: "wrong_type",
			Message: "type must be a string or array of strings"})
	}
}

// compileSchema compiles one schema document with the shared
// santhosh-tekuri compiler. No fetcher is registered, so remote $refs
// cannot resolve — a belt under the profile check.
func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource("inofy://schema/doc", doc); err != nil {
		return nil, err
	}
	return c.Compile("inofy://schema/doc")
}

// validateValue checks instance v (generic JSON form) against schema raw.
func validateValue(raw json.RawMessage, v any) error {
	sch, err := compileSchema(raw)
	if err != nil {
		return err
	}
	return sch.Validate(v)
}

// newDecoderBytes returns a UseNumber decoder for raw JSON bytes.
func newDecoderBytes(raw []byte) *json.Decoder {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec
}

// marshalBack re-encodes a generic document value into JSON bytes. The
// doc came from a UseNumber decoder, so numbers stay exact.
func marshalBack(v any) (json.RawMessage, error) {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return json.RawMessage(strings.TrimSuffix(buf.String(), "\n")), nil
}
