package inofy

import "sort"

// Catalog is an immutable snapshot of node descriptors frozen before
// compilation. Zero-value Catalogs are unusable; build with NewCatalog.
type Catalog struct {
	descriptors map[string]NodeDescriptor
	frozen      bool
}

// NewCatalog validates and freezes descriptors. Absent type or
// implementation IDs and duplicate type IDs are rejected.
func NewCatalog(descriptors []NodeDescriptor) (Catalog, error) {
	frozen := make(map[string]NodeDescriptor, len(descriptors))
	for _, d := range descriptors {
		if d.TypeID == "" {
			return Catalog{}, &Error{Code: ErrInvalidDefinition, Message: "node descriptor missing type id"}
		}
		if d.ImplementationID == "" {
			return Catalog{}, &Error{Code: ErrInvalidDefinition, Message: "node descriptor " + d.TypeID + " missing implementation id"}
		}
		if _, dup := frozen[d.TypeID]; dup {
			return Catalog{}, &Error{Code: ErrInvalidDefinition, Message: "duplicate node type " + d.TypeID}
		}
		frozen[d.TypeID] = d
	}
	return Catalog{descriptors: frozen, frozen: true}, nil
}

func (c Catalog) valid() bool { return c.frozen }

// Lookup resolves a call type against the frozen snapshot.
func (c Catalog) Lookup(typeID string) (NodeDescriptor, bool) {
	d, ok := c.descriptors[typeID]
	return d, ok
}

// Types lists registered type IDs in deterministic order.
func (c Catalog) Types() []string {
	out := make([]string, 0, len(c.descriptors))
	for id := range c.descriptors {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
