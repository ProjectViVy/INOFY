package inofy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ProjectViVy/inofy"
)

// An explicit empty JSON Pointer is a valid root binding and must
// round-trip through the typed artifact: decoding {source,pointer:""}
// and re-marshaling has to keep the key, or strict decode rejects the
// canonical form with missing_binding.
func TestBindingMarshalPreservesEmptyPointer(t *testing.T) {
	raw := `{"literal":null,"source":"node1","pointer":""}`
	var b inofy.Binding
	if err := json.Unmarshal([]byte(raw), &b); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"pointer":""`) {
		t.Fatalf("marshal dropped explicit empty pointer: %s", out)
	}

	var absent inofy.Binding
	if err := json.Unmarshal([]byte(`{"source":"node1"}`), &absent); err != nil {
		t.Fatalf("unmarshal absent: %v", err)
	}
	outAbsent, err := json.Marshal(absent)
	if err != nil {
		t.Fatalf("marshal absent: %v", err)
	}
	if strings.Contains(string(outAbsent), `"pointer"`) {
		t.Fatalf("marshal invented a pointer key: %s", outAbsent)
	}
}

// An authored root-pointer output stays stable through the
// decode -> canonical digest -> decode cycle SaveDraft performs.
func TestArtifactRoundTripPreservesRootPointerBinding(t *testing.T) {
	doc := `{"definition":{"schema_version":"inofy.workflow/v1","graph":{
		"nodes":[{"id":"a","kind":"call","type":"inofy.value@1"}],
		"edges":[],"exits":["a"],"outputs":{"answer":{"source":"a","pointer":""}}}},
		"presentation":{}}`
	a, diags, err := inofy.DecodeArtifact([]byte(doc))
	if err != nil || len(diags) != 0 {
		t.Fatalf("decode: %v %+v", err, diags)
	}
	out, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal artifact: %v", err)
	}
	if !strings.Contains(string(out), `"pointer":""`) {
		t.Fatalf("artifact marshal dropped empty pointer: %s", out)
	}
	if _, diags2, err := inofy.DecodeArtifact(out); err != nil || len(diags2) != 0 {
		t.Fatalf("re-decode rejected round-tripped binding: %v %+v", err, diags2)
	}
}
