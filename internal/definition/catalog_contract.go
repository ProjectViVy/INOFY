package definition

import (
	"encoding/json"
	"slices"
)

// CatalogContractDoc returns the direct, canonicalizable values for the
// descriptors used by a definition. Presentation metadata and unrelated
// installed types are excluded because they do not affect compilation.
func CatalogContractDoc(defDoc map[string]any, types []TypeRef) (map[string]any, error) {
	used := map[string]bool{}
	collectUsedTypes(defDoc["graph"], used)

	byType := make(map[string]TypeRef, len(types))
	for _, t := range types {
		byType[t.TypeID] = t
	}

	ids := make([]string, 0, len(used))
	for typeID := range used {
		if _, ok := byType[typeID]; ok {
			ids = append(ids, typeID)
		}
	}
	slices.Sort(ids)

	entries := make([]any, 0, len(ids))
	for _, typeID := range ids {
		entry, err := descriptorDoc(byType[typeID])
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return map[string]any{"types": entries}, nil
}

func collectUsedTypes(value any, used map[string]bool) {
	walkJSON(value, func(node map[string]any) {
		if kind, _ := asString(node["kind"]); kind == "call" {
			if typeID, ok := asString(node["type"]); ok && typeID != "" {
				used[typeID] = true
			}
		}
	})
}

func descriptorDoc(t TypeRef) (map[string]any, error) {
	doc := map[string]any{
		"type_id":           t.TypeID,
		"implementation_id": t.ImplementationID,
	}
	decode := func(name string, raw json.RawMessage) error {
		if len(raw) == 0 {
			return nil
		}
		var value any
		decoder := newDecoderBytes(raw)
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		doc[name] = value
		return nil
	}
	if err := decode("config_schema", t.ConfigSchema); err != nil {
		return nil, err
	}
	if err := decode("input_schema", t.InputSchema); err != nil {
		return nil, err
	}
	if err := decode("output_schema", t.OutputSchema); err != nil {
		return nil, err
	}
	if len(t.Capabilities) > 0 {
		capabilities := slices.Clone(t.Capabilities)
		slices.Sort(capabilities)
		values := make([]any, len(capabilities))
		for i, capability := range capabilities {
			values[i] = capability
		}
		doc["capabilities"] = values
	}
	if t.Replay != "" {
		doc["replay"] = t.Replay
	}
	if t.SupportsWait {
		doc["supports_wait"] = true
	}
	return doc, nil
}
