package inofy_test

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/ProjectViVy/inofy"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// A separate consumer compiles the exported schema. Strict decoding remains
// authoritative for duplicate keys, references, topology and catalog rules.
func TestDefinitionSchemaAgreesWithStrictDecoder(t *testing.T) {
	raw := inofy.DefinitionSchema()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("definition.json", doc); err != nil {
		t.Fatal(err)
	}
	schema, err := c.Compile("definition.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("testdata/definition/valid.json")
	if err != nil {
		t.Fatal(err)
	}
	var artifact map[string]any
	if err := json.Unmarshal(fixture, &artifact); err != nil {
		t.Fatal(err)
	}
	definition := artifact["definition"].(map[string]any)
	cases := []struct {
		name   string
		change func(map[string]any)
		valid  bool
	}{
		{"valid definition", func(map[string]any) {}, true},
		{"unknown field", func(d map[string]any) { d["surprise"] = true }, false},
		{"wrong version", func(d map[string]any) { d["schema_version"] = "inofy.workflow/v0" }, false},
		{"wrong nodes shape", func(d map[string]any) { d["graph"].(map[string]any)["nodes"] = "bad" }, false},
		{"unknown node field", func(d map[string]any) {
			d["graph"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["surprise"] = true
		}, false},
		{"ambiguous binding", func(d map[string]any) {
			d["graph"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["inputs"].(map[string]any)["query"].(map[string]any)["literal"] = "oops"
		}, false},
		{"missing pointer", func(d map[string]any) {
			delete(d["graph"].(map[string]any)["nodes"].([]any)[0].(map[string]any)["inputs"].(map[string]any)["query"].(map[string]any), "pointer")
		}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copyBytes, _ := json.Marshal(definition)
			var value map[string]any
			if err := json.Unmarshal(copyBytes, &value); err != nil {
				t.Fatal(err)
			}
			tc.change(value)
			got := schema.Validate(value) == nil
			if got != tc.valid {
				t.Fatalf("schema accepted=%t, want %t", got, tc.valid)
			}
			encoded, _ := json.Marshal(map[string]any{"definition": value})
			_, _, decodeErr := inofy.DecodeArtifact(encoded)
			if (decodeErr == nil) != tc.valid {
				t.Fatalf("decoder accepted=%t, want %t", decodeErr == nil, tc.valid)
			}
		})
	}
	first := inofy.DefinitionSchema()
	for range 32 {
		if !bytes.Equal(first, inofy.DefinitionSchema()) {
			t.Fatal("schema bytes must be stable across calls")
		}
	}
	first[0] = '!'
	if !json.Valid(inofy.DefinitionSchema()) {
		t.Fatal("caller mutated the exported schema")
	}
}
