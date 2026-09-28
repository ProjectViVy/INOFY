// Package definition_test pins the strict artifact decoder and the portable
// schema profile (architecture §4.1, gate G1). Diagnostics carry authored
// JSON-Pointer paths; every violation must fail at its exact location.
package definition_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/internal/definition"
)

func mustDecode(t *testing.T, data string) (*definition.ArtifactDoc, []definition.Finding, error) {
	t.Helper()
	return definition.DecodeArtifactDoc([]byte(data))
}

func TestDecodeArtifactStrict(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/definition/valid.json")
	if err != nil {
		t.Fatalf("read valid.json: %v", err)
	}
	doc, findings, err := definition.DecodeArtifactDoc(raw)
	if err != nil {
		t.Fatalf("valid artifact rejected: %v (findings %v)", err, findings)
	}
	if len(findings) != 0 {
		t.Fatalf("valid artifact produced findings: %v", findings)
	}
	if doc.Definition["schema_version"] != "inofy.workflow/v1" {
		t.Fatalf("schema_version lost: %v", doc.Definition["schema_version"])
	}

	// Presentation preserves unknown fields inside its namespace.
	pres, _ := doc.Presentation["custom_ui_hint"].(map[string]any)
	if pres["color"] != "blue" {
		t.Fatalf("presentation extra field dropped: %v", doc.Presentation)
	}

	// Re-encoding the decoded semantic document round-trips without losing
	// ports or bindings.
	again, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal decoded doc: %v", err)
	}
	doc2, findings2, err := definition.DecodeArtifactDoc(again)
	if err != nil || len(findings2) != 0 {
		t.Fatalf("round-trip decode failed: err=%v findings=%v", err, findings2)
	}
	if _, ok := doc2.Definition["graph"].(map[string]any); !ok {
		t.Fatal("round-trip lost the graph")
	}
}

// TestDecodeArtifactStrict_cases asserts each violation fails at an exact
// authored path. Path shape is a JSON Pointer from the artifact root.
func TestDecodeArtifactStrict_cases(t *testing.T) {
	// Load file-driven cases first; inline cases cover the strictness matrix.
	type fileCase struct {
		Name   string         `json:"name"`
		Doc    map[string]any `json:"doc"`
		Expect struct {
			Check string `json:"check"`
			Path  string `json:"path"`
			Code  string `json:"code"`
		} `json:"expect"`
	}
	raw, err := os.ReadFile("../../testdata/definition/invalid.json")
	if err != nil {
		t.Fatalf("read invalid.json: %v", err)
	}
	var suite struct {
		Cases []fileCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &suite); err != nil {
		t.Fatalf("parse invalid.json: %v", err)
	}
	for _, fc := range suite.Cases {
		docJSON, _ := json.Marshal(fc.Doc)
		_, findings, err := definition.DecodeArtifactDoc(docJSON)
		if err == nil {
			t.Fatalf("%s: invalid doc accepted", fc.Name)
		}
		want := definition.Finding{Check: fc.Expect.Check, Path: fc.Expect.Path, Code: fc.Expect.Code}
		if !hasFinding(findings, want) {
			t.Fatalf("%s: want finding %+v in %v", fc.Name, want, findings)
		}
	}

	cases := []struct {
		name  string
		doc   string
		check string
		path  string
		code  string
	}{
		{
			name: "duplicate_nested_binding_key",
			// literal duplicate inside inputs — the generic decoder must
			// catch it before encoding/json silently keeps the later value.
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{"q":{"literal":1},"q":{"literal":2}}}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/inputs/q", code: "duplicate_key",
		},
		{
			name:  "duplicate_envelope_key",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[],"edges":[],"exits":[]}},"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[],"edges":[],"exits":[]}}}`,
			check: "schema", path: "/definition", code: "duplicate_key",
		},
		{
			name:  "unknown_node_field_cross_kind",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","cases":[]}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/cases", code: "unknown_field",
		},
		{
			name:  "unknown_definition_field",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[],"edges":[],"exits":[]},"debug":true}}`,
			check: "schema", path: "/definition/debug", code: "unknown_field",
		},
		{
			name:  "reserved_id_dollar",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"$join","kind":"call","type":"t@1"}],"edges":[],"exits":["$join"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/id", code: "reserved_id",
		},
		{
			name:  "reserved_id_slash",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a/b","kind":"call","type":"t@1"}],"edges":[],"exits":["a/b"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/id", code: "reserved_id",
		},
		{
			name:  "bad_schema_version",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v0","graph":{"nodes":[],"edges":[],"exits":[]}}}`,
			check: "schema", path: "/definition/schema_version", code: "unsupported_version",
		},
		{
			name:  "remote_schema_ref_no_fetch",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","inputs_schema":{"$ref":"https://schemas.example.com/x.json"},"graph":{"nodes":[],"edges":[],"exits":[]}}}`,
			check: "schema", path: "/definition/inputs_schema/$ref", code: "remote_schema_ref",
		},
		{
			name:  "unsupported_schema_keyword",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","inputs_schema":{"oneOf":[{"type":"string"},{"type":"number"}]},"graph":{"nodes":[],"edges":[],"exits":[]}}}`,
			check: "schema", path: "/definition/inputs_schema/oneOf", code: "unsupported_schema",
		},
		{
			name:  "binding_literal_and_source",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{"q":{"literal":1,"source":"input","pointer":"/q"}}}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/inputs/q", code: "ambiguous_binding",
		},
		{
			name:  "binding_neither",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{"q":{}}}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/inputs/q", code: "missing_binding",
		},
		{
			name:  "binding_pointer_without_source",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{"q":{"pointer":"/q"}}}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/inputs/q", code: "missing_binding",
		},
		{
			name:  "binding_source_requires_pointer",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{"q":{"source":"input"}}}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/inputs/q", code: "missing_binding",
		},
		{
			name:  "edge_port_on_non_switch",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1"},{"id":"b","kind":"call","type":"t@1"}],"edges":[{"from":"a","to":"b","port":"p"}],"exits":["b"]}}}`,
			check: "schema", path: "/definition/graph/edges/0/port", code: "illegal_port",
		},
		{
			name:  "switch_edge_missing_port",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"sw","kind":"switch","inputs":{},"cases":[{"port":"p","when":{"op":"exists","pointer":"/x"}}],"default_port":"d","join":"sel"},{"id":"sel","kind":"select","switch":"sw","candidates":[{"source":"n","pointer":"/o"}]},{"id":"n","kind":"call","type":"t@1"}],"edges":[{"from":"sw","to":"n"}],"exits":["sel"]}}}`,
			check: "schema", path: "/definition/graph/edges/0", code: "missing_port",
		},
		{
			name:  "edge_port_not_declared",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"sw","kind":"switch","inputs":{},"cases":[{"port":"p","when":{"op":"exists","pointer":"/x"}}],"default_port":"d","join":"sel"},{"id":"sel","kind":"select","switch":"sw","candidates":[{"source":"n","pointer":"/o"}]},{"id":"n","kind":"call","type":"t@1"}],"edges":[{"from":"sw","to":"n","port":"zzz"}],"exits":["sel"]}}}`,
			check: "schema", path: "/definition/graph/edges/0/port", code: "undeclared_port",
		},
		{
			name:  "unknown_node_kind",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"loop","type":"t@1"}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/kind", code: "unknown_kind",
		},
		{
			name:  "switch_missing_join",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"sw","kind":"switch","inputs":{},"cases":[{"port":"p","when":{"op":"exists","pointer":"/x"}}],"default_port":"d"}],"edges":[],"exits":["sw"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0", code: "missing_field",
		},
		{
			name:  "select_missing_switch",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"sel","kind":"select","candidates":[{"source":"a","pointer":"/o"}]},{"id":"a","kind":"call","type":"t@1"}],"edges":[],"exits":["sel"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0", code: "missing_field",
		},
		{
			name:  "repeat_missing_until",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"r","kind":"repeat","body":{"nodes":[],"edges":[],"exits":[]},"max_iterations":4}],"edges":[],"exits":["r"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0", code: "missing_field",
		},
		{
			name:  "duplicate_node_id",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1"},{"id":"a","kind":"call","type":"t@1"}],"edges":[],"exits":["a"]}}}`,
			check: "schema", path: "/definition/graph/nodes/1/id", code: "duplicate_id",
		},
		{
			name:  "unknown_edge_field",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1"},{"id":"b","kind":"call","type":"t@1"}],"edges":[{"from":"a","to":"b","weight":2}],"exits":["b"]}}}`,
			check: "schema", path: "/definition/graph/edges/0/weight", code: "unknown_field",
		},
		{
			name:  "predicate_unknown_op",
			doc:   `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"sw","kind":"switch","inputs":{},"cases":[{"port":"p","when":{"op":"matches","left":{"source":"input","pointer":"/x"},"right":{"literal":"a.*"}}}],"default_port":"d","join":"sel"},{"id":"sel","kind":"select","switch":"sw","candidates":[{"source":"sw","pointer":"/o"}]}],"edges":[],"exits":["sel"]}}}`,
			check: "schema", path: "/definition/graph/nodes/0/cases/0/when/op", code: "unknown_op",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, findings, err := mustDecode(t, tc.doc)
			if err == nil {
				t.Fatalf("invalid doc accepted; findings=%v", findings)
			}
			want := definition.Finding{Check: tc.check, Path: tc.path, Code: tc.code}
			if !hasFinding(findings, want) {
				t.Fatalf("want finding %+v, got %v", want, findings)
			}
		})
	}

	// Explicit null literal is a value, not an absent binding.
	t.Run("null_literal_is_present", func(t *testing.T) {
		doc := `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{"q":{"literal":null}}}],"edges":[],"exits":["a"]}}}`
		_, findings, err := mustDecode(t, doc)
		if err != nil {
			t.Fatalf("null literal rejected: %v findings=%v", err, findings)
		}
	})

	// Presentation tolerates unknown fields; executable sections do not.
	t.Run("presentation_extra_preserved", func(t *testing.T) {
		doc := `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[],"edges":[],"exits":[]}},"presentation":{"theme":{"accent":1}}}`
		out, findings, err := mustDecode(t, doc)
		if err != nil {
			t.Fatalf("presentation extra rejected: %v", findings)
		}
		if _, ok := out.Presentation["theme"]; !ok {
			t.Fatal("presentation extra field not preserved")
		}
	})

	// Repeat body is a nested graph and gets its own paths.
	t.Run("repeat_body_violation_nested_path", func(t *testing.T) {
		doc := `{"definition":{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"r","kind":"repeat","body":{"nodes":[{"id":"in/nested","kind":"call","type":"t@1"}],"edges":[],"exits":["in/nested"]},"max_iterations":2,"until":{"op":"exists","pointer":"/x"}}],"edges":[],"exits":["r"]}}}`
		_, findings, err := mustDecode(t, doc)
		if err == nil {
			t.Fatal("nested reserved id accepted")
		}
		want := definition.Finding{Check: "schema", Path: "/definition/graph/nodes/0/body/nodes/0/id", Code: "reserved_id"}
		if !hasFinding(findings, want) {
			t.Fatalf("want %+v in %v", want, findings)
		}
	})

}

func hasFinding(findings []definition.Finding, want definition.Finding) bool {
	for _, f := range findings {
		if f.Check == want.Check && f.Path == want.Path && f.Code == want.Code {
			return true
		}
	}
	return false
}

// TestValidateDefinitionCapability exercises the catalog-facing checks:
// unknown types, config schema violations and ungranted capabilities.
func TestValidateDefinitionCapability(t *testing.T) {
	catalog, err := inofy.NewCatalog([]inofy.NodeDescriptor{
		{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
			ConfigSchema:     json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer","minimum":1}},"additionalProperties":false}`),
			InputSchema:      json.RawMessage(`{"type":"object","properties":{"limit":{"type":"integer"}}}`),
			OutputSchema:     json.RawMessage(`{"type":"object","properties":{"out":{"type":"string"}}}`),
			Capabilities:     []string{"compute"},
		},
		{
			TypeID:           "inofy.wait@1",
			ImplementationID: "impl-wait",
			SupportsWait:     true,
		},
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	opts := inofy.CompileOptions{Features: map[string]bool{"compute": true}}

	t.Run("clean_definition", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						Config: json.RawMessage(`{"limit":2}`),
						Inputs: map[string]inofy.Binding{"q": {Source: "input", Pointer: "/q"}}},
				},
				Exits: []string{"a"},
			},
		}
		if diags := inofy.ValidateDefinition(def, catalog, opts); len(diags) != 0 {
			t.Fatalf("valid definition produced diagnostics: %v", diags)
		}
	})

	t.Run("unknown_call_type", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph:         inofy.Graph{Nodes: []inofy.Node{{ID: "a", Kind: inofy.NodeKindCall, Type: "missing@9"}}, Exits: []string{"a"}},
		}
		diags := inofy.ValidateDefinition(def, catalog, opts)
		if !hasDiag(diags, inofy.Diagnostic{Check: inofy.CheckCapability, Path: "a", Code: "unknown_node_type"}) {
			t.Fatalf("want capability/unknown_node_type, got %v", diags)
		}
	})

	t.Run("config_schema_violation", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{Nodes: []inofy.Node{
				{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1", Config: json.RawMessage(`{"limit":0}`)},
			}, Exits: []string{"a"}},
		}
		diags := inofy.ValidateDefinition(def, catalog, opts)
		if !hasDiag(diags, inofy.Diagnostic{Check: inofy.CheckCapability, Path: "a", Code: "schema_mismatch"}) {
			t.Fatalf("want capability/schema_mismatch at a, got %v", diags)
		}
	})

	t.Run("capability_not_granted", func(t *testing.T) {
		diags := inofy.ValidateDefinition(inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph:         inofy.Graph{Nodes: []inofy.Node{{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1"}}, Exits: []string{"a"}},
		}, catalog, inofy.CompileOptions{})
		if !hasDiag(diags, inofy.Diagnostic{Check: inofy.CheckCapability, Path: "a", Code: "capability_missing"}) {
			t.Fatalf("want capability_missing, got %v", diags)
		}
	})

	t.Run("literal_input_schema_violation", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{Nodes: []inofy.Node{
				{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
					Inputs: map[string]inofy.Binding{"limit": {Literal: json.RawMessage(`"nope"`)}}},
			}, Exits: []string{"a"}},
		}
		diags := inofy.ValidateDefinition(def, catalog, opts)
		if !hasDiag(diags, inofy.Diagnostic{Check: inofy.CheckCapability, Path: "a/inputs/limit", Code: "schema_mismatch"}) {
			t.Fatalf("want capability/schema_mismatch at a/inputs/limit, got %v", diags)
		}
	})
}

func hasDiag(diags []inofy.Diagnostic, want inofy.Diagnostic) bool {
	for _, d := range diags {
		if d.Check == want.Check && d.Code == want.Code && (want.Path == "" || strings.HasPrefix(d.Path, want.Path)) {
			return true
		}
	}
	return false
}
