package definition

import (
	"reflect"
	"testing"
)

func TestCatalogContractOnlyIncludesGraphCallNodes(t *testing.T) {
	doc := map[string]any{
		"graph": map[string]any{
			"nodes": []any{
				map[string]any{
					"id":   "outer",
					"kind": "call",
					"type": "test.outer@1",
					"config": map[string]any{
						"example": map[string]any{"kind": "call", "type": "test.config-only@1"},
					},
				},
				map[string]any{
					"id":   "repeat",
					"kind": "repeat",
					"body": map[string]any{
						"nodes": []any{map[string]any{
							"id": "inner", "kind": "call", "type": "test.inner@1",
						}},
					},
				},
			},
		},
	}

	contract, err := CatalogContractDoc(doc, []TypeRef{
		{TypeID: "test.config-only@1", ImplementationID: "config-only"},
		{TypeID: "test.inner@1", ImplementationID: "inner"},
		{TypeID: "test.outer@1", ImplementationID: "outer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	entries := contract["types"].([]any)
	got := make([]string, 0, len(entries))
	for _, raw := range entries {
		got = append(got, raw.(map[string]any)["type_id"].(string))
	}
	if want := []string{"test.inner@1", "test.outer@1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("catalog contract types = %v, want %v", got, want)
	}
}
