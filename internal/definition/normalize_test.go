// inofy-normal-v1 canonical value vectors (architecture §4.3, gate G1).
package definition_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/internal/definition"
)

type vectorCase struct {
	Name       string         `json:"name"`
	Doc        map[string]any `json:"doc,omitempty"`
	Normalized string         `json:"normalized,omitempty"`
	Reject     string         `json:"reject,omitempty"`
	Note       string         `json:"note,omitempty"`
}

func loadVectors(t *testing.T) []vectorCase {
	t.Helper()
	raw, err := os.ReadFile("../../testdata/definition/normal_v1.json")
	if err != nil {
		t.Fatalf("read normal_v1.json: %v", err)
	}
	var suite struct {
		Algorithm string       `json:"algorithm"`
		Cases     []vectorCase `json:"cases"`
	}
	dec := json.NewDecoder(bytesReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&suite); err != nil {
		t.Fatalf("parse normal_v1.json: %v", err)
	}
	if suite.Algorithm != "inofy-normal-v1" {
		t.Fatalf("fixture algorithm %q", suite.Algorithm)
	}
	return suite.Cases
}

func TestNormalV1ValueVectors(t *testing.T) {
	for _, vc := range loadVectors(t) {
		t.Run(vc.Name, func(t *testing.T) {
			if vc.Reject != "" {
				if _, err := definition.NormalizeDefinitionDoc(vc.Doc); err == nil {
					t.Fatalf("doc accepted; want reject %q", vc.Reject)
				} else if got := err.Error(); !contains(got, vc.Reject) {
					t.Fatalf("want reject containing %q, got %q", vc.Reject, got)
				}
				return
			}
			got, err := definition.NormalizeDefinitionDoc(vc.Doc)
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if string(got) != vc.Normalized {
				t.Fatalf("normalized bytes differ:\n got %s\nwant %s", got, vc.Normalized)
			}
			// Stability: re-decode normalized bytes and normalize again.
			var again map[string]any
			dec := json.NewDecoder(bytesReader(got))
			dec.UseNumber()
			if err := dec.Decode(&again); err != nil {
				t.Fatalf("re-decode: %v", err)
			}
			got2, err := definition.NormalizeDefinitionDoc(again)
			if err != nil {
				t.Fatalf("re-normalize: %v", err)
			}
			if string(got2) != vc.Normalized {
				t.Fatalf("unstable: second normalize produced %s", got2)
			}
		})
	}
}

// TestNormalV1Structural pins semantic-order invariance: only semantically
// unordered containers are sorted.
func TestNormalV1Structural(t *testing.T) {
	base := `{
		"schema_version": "inofy.workflow/v1",
		"graph": {
			"nodes": [
				{"id": "a", "kind": "call", "type": "t@1"},
				{"id": "b", "kind": "call", "type": "t@1"}
			],
			"edges": [
				{"from": "a", "to": "b"},
				{"from": "b", "to": "a"}
			],
			"exits": ["a", "b"]
		}
	}`

	normOf := func(doc string) []byte {
		t.Helper()
		var m map[string]any
		dec := json.NewDecoder(bytesReader([]byte(doc)))
		dec.UseNumber()
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out, err := definition.NormalizeDefinitionDoc(m)
		if err != nil {
			t.Fatalf("normalize: %v", err)
		}
		return out
	}

	// Reordered object keys, nodes and edges normalize identically.
	shuffled := `{
		"graph": {
			"edges": [
				{"from": "b", "to": "a"},
				{"from": "a", "to": "b"}
			],
			"exits": ["b", "a"],
			"nodes": [
				{"kind": "call", "type": "t@1", "id": "b"},
				{"kind": "call", "type": "t@1", "id": "a"}
			]
		},
		"schema_version": "inofy.workflow/v1"
	}`
	if a, b := normOf(base), normOf(shuffled); string(a) != string(b) {
		t.Fatalf("order-insensitive doc differs:\n%s\n%s", a, b)
	}

	// Switch case order is semantic: swapping cases must change bytes.
	casesA := `{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"s","kind":"switch","inputs":{},"cases":[{"port":"p","when":{"op":"exists","pointer":"/a"}},{"port":"q","when":{"op":"exists","pointer":"/b"}}],"default_port":"d","join":"j"}],"edges":[],"exits":["s"]}}`
	casesB := `{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"s","kind":"switch","inputs":{},"cases":[{"port":"q","when":{"op":"exists","pointer":"/b"}},{"port":"p","when":{"op":"exists","pointer":"/a"}}],"default_port":"d","join":"j"}],"edges":[],"exits":["s"]}}`
	if string(normOf(casesA)) == string(normOf(casesB)) {
		t.Fatal("reordered switch cases produced identical bytes")
	}

	// Absent vs explicit-null literal differ (omitted binding ≠ null).
	nullDoc := `{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{"q":{"literal":null}}}],"edges":[],"exits":["a"]}}`
	absentDoc := `{"schema_version":"inofy.workflow/v1","graph":{"nodes":[{"id":"a","kind":"call","type":"t@1","inputs":{}}],"edges":[],"exits":["a"]}}`
	if string(normOf(nullDoc)) == string(normOf(absentDoc)) {
		t.Fatal("explicit null literal equals absent binding")
	}
}

func TestNormalizeJSONValue(t *testing.T) {
	got, err := inofy.NormalizeJSONValue(json.RawMessage(`{"b":2,"a":1}`))
	if err != nil || string(got) != `{"a":1,"b":2}` {
		t.Fatalf("canonical value = %s, err=%v", got, err)
	}
	if _, err := inofy.NormalizeJSONValue(json.RawMessage(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("duplicate object key accepted")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }
