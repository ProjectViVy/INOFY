// Digest envelopes (architecture §4.3): SHA-256 over inofy-normal-v1
// bytes with a named-version prefix. DefinitionDigest covers only the
// semantic definition; ArtifactDigest additionally covers presentation;
// CatalogDigest covers only the used node descriptors plus their
// trusted implementation identities.
package definition

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
)

// DigestPrefix names the algorithm and version.
const DigestPrefix = "inofy-normal-v1:sha256:"

// DigestBytes hashes already-normalized bytes into the named envelope.
func DigestBytes(normalized []byte) string {
	sum := sha256.Sum256(normalized)
	return DigestPrefix + hex.EncodeToString(sum[:])
}

// CatalogDigestDoc covers the descriptors actually used by the
// definition — unrelated installed nodes never enter the digest.
func CatalogDigestDoc(defDoc map[string]any, types []TypeRef) (string, error) {
	used := map[string]bool{}
	collectUsedTypes(defDoc["graph"], used)

	byType := make(map[string]TypeRef, len(types))
	for _, t := range types {
		byType[t.TypeID] = t
	}

	entries := make([]any, 0, len(used))
	for typeID := range used {
		desc, ok := byType[typeID]
		if !ok {
			continue // unknown types are a capability diagnostic
		}
		entry, err := descriptorDoc(desc)
		if err != nil {
			return "", err
		}
		entries = append(entries, entry)
	}
	slices.SortFunc(entries, func(a, b any) int {
		return cmpString(strOr(asObjectOr(a)["type_id"]), strOr(asObjectOr(b)["type_id"]))
	})
	doc := map[string]any{"types": entries}
	norm, err := Canonical(doc)
	if err != nil {
		return "", err
	}
	return DigestBytes(norm), nil
}

// descriptorDoc projects the contract-bearing fields of one descriptor
// into generic form. Display metadata is non-semantic and excluded.
func descriptorDoc(t TypeRef) (map[string]any, error) {
	doc := map[string]any{
		"type_id":           t.TypeID,
		"implementation_id": t.ImplementationID,
	}
	decode := func(name string, raw json.RawMessage) error {
		if len(raw) == 0 {
			return nil
		}
		var v any
		d := newDecoderBytes(raw)
		if err := d.Decode(&v); err != nil {
			return err
		}
		doc[name] = v
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
		caps := slices.Clone(t.Capabilities)
		slices.Sort(caps)
		arr := make([]any, len(caps))
		for i, c := range caps {
			arr[i] = c
		}
		doc["capabilities"] = arr
	}
	if t.Replay != "" {
		doc["replay"] = t.Replay
	}
	if t.SupportsWait {
		doc["supports_wait"] = true
	}
	return doc, nil
}

// collectUsedTypes walks call nodes across the outer graph and nested
// repeat bodies.
func collectUsedTypes(v any, used map[string]bool) {
	walkJSON(v, func(m map[string]any) {
		if kind, _ := asString(m["kind"]); kind == "call" {
			if t, ok := asString(m["type"]); ok && t != "" {
				used[t] = true
			}
		}
	})
}
