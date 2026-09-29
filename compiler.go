package inofy

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"

	"github.com/ProjectViVy/inofy/internal/definition"
	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

// compilerVersion is the INOFY compiler identity bound into
// ProgramDigest (architecture §4.3).
const compilerVersion = "inofy/v0.1"

// Compile performs structural validation and compilation without I/O
// effects. Every call type resolves against the frozen catalog, the
// graph passes full definition validation (schema, semantics,
// topology), and each scope compiles to an Eino workflow. A failed
// validation reports diagnostics; a failed build reports an error —
// never a false success.
func Compile(ctx context.Context, def Definition, catalog Catalog, options CompileOptions) (*Program, []Diagnostic, error) {
	if !catalog.valid() {
		return nil, nil, &Error{
			Code:    ErrInvalidDefinition,
			Message: "catalog is a zero value; build it with NewCatalog",
		}
	}
	if def.SchemaVersion != SchemaVersionV1 {
		return nil, nil, &Error{
			Code:    ErrInvalidDefinition,
			Message: fmt.Sprintf("schema_version %q, want %q", def.SchemaVersion, SchemaVersionV1),
		}
	}
	if err := resolveCallTypes(def.Graph, &catalog, ""); err != nil {
		return nil, nil, err
	}
	if diags := ValidateDefinition(def, catalog, options); len(diags) > 0 {
		return nil, diags, nil
	}
	doc, err := defToDoc(def)
	if err != nil {
		return nil, nil, &Error{Code: ErrInvalidDefinition, Err: err,
			Message: "definition cannot be encoded for compilation"}
	}
	lim := effectiveLimits(options.Limits)
	rt, err := einoruntime.CompileProgram(ctx, doc, typeInfos(catalog), einoruntime.Limits{
		MaxActivations:     lim.MaxActivations,
		MaxAttemptsPerCall: lim.MaxAttemptsPerCall,
		NodeTimeoutMS:      lim.NodeTimeoutMS,
	})
	if err != nil {
		return nil, nil, adaptError(err)
	}
	meta, err := programMeta(def, catalog)
	if err != nil {
		return nil, nil, err
	}
	return &Program{meta: meta, rt: rt, compileLimits: lim}, nil, nil
}

// typeInfos projects the catalog's used descriptors into the runtime's
// neutral type surface.
func typeInfos(catalog Catalog) map[string]einoruntime.TypeInfo {
	out := map[string]einoruntime.TypeInfo{}
	for _, id := range catalog.Types() {
		d, ok := catalog.Lookup(id)
		if !ok {
			continue
		}
		out[id] = einoruntime.TypeInfo{
			ImplementationID: d.ImplementationID,
			Replay:           string(d.Replay),
			SupportsWait:     d.SupportsWait,
			InputSchema:      d.InputSchema,
			OutputSchema:     d.OutputSchema,
		}
	}
	return out
}

// programMeta binds the program identity: definition and catalog
// digests, compiler version, and the exact Eino build — all hashed
// into ProgramDigest in normal-v1 form.
func programMeta(def Definition, catalog Catalog) (ProgramMeta, error) {
	dd, err := DefinitionDigest(def)
	if err != nil {
		return ProgramMeta{}, err
	}
	cd, err := CatalogDigest(def, catalog)
	if err != nil {
		return ProgramMeta{}, err
	}
	meta := ProgramMeta{
		DefinitionDigest: dd,
		CatalogDigest:    cd,
		CompilerVersion:  compilerVersion,
		EinoBuild:        einoBuild(),
	}
	norm, err := definition.Canonical(map[string]any{
		"compiler_version":  meta.CompilerVersion,
		"eino_build":        meta.EinoBuild,
		"definition_digest": meta.DefinitionDigest,
		"catalog_digest":    meta.CatalogDigest,
	})
	if err != nil {
		return ProgramMeta{}, err
	}
	meta.ProgramDigest = definition.DigestBytes(norm)
	return meta, nil
}

// einoBuild reports the exact eino module version linked into the
// binary — the ProgramDigest binds the build identity, not a claim.
func einoBuild() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	for _, dep := range bi.Deps {
		if dep.Path == "github.com/cloudwego/eino" {
			return dep.Version
		}
	}
	return "unknown"
}

// adaptError maps internal runtime errors onto the public Error
// contract; foreign errors pass through unchanged.
func adaptError(err error) error {
	var ie *einoruntime.Error
	if errors.As(err, &ie) {
		return &Error{
			Code:    ErrorCode(ie.Code),
			Path:    ie.Path,
			Message: ie.Message,
			Err:     ie.Err,
		}
	}
	return err
}

// resolveCallTypes requires every call node's type to resolve against
// the frozen catalog (including inside repeat bodies).
func resolveCallTypes(g Graph, catalog *Catalog, prefix string) error {
	for _, n := range g.Nodes {
		path := prefix + n.ID
		switch n.Kind {
		case NodeKindCall:
			if n.Type == "" {
				return &Error{Code: ErrInvalidDefinition, Path: path, Message: "call node missing type"}
			}
			if _, ok := catalog.Lookup(n.Type); !ok {
				return &Error{Code: ErrUnknownNodeType, Path: path, Message: "call type " + n.Type + " not in catalog"}
			}
		case NodeKindRepeat:
			if n.Body != nil {
				if err := resolveCallTypes(*n.Body, catalog, path+"/"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
