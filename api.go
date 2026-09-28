package inofy

import (
	"context"
	"fmt"
)

// Program is an immutable compiled artifact safe for concurrent Run
// invocations with different run IDs. It holds no run state, live
// credentials, host globals or per-run closures.
type Program struct {
	meta ProgramMeta
}

// ProgramMeta binds the program to its inputs: definition and catalog
// digests, the INOFY compiler/format version and the exact Eino build
// identity (architecture §4.3).
type ProgramMeta struct {
	DefinitionDigest string `json:"definition_digest"`
	CatalogDigest    string `json:"catalog_digest"`
	ProgramDigest    string `json:"program_digest"`
	CompilerVersion  string `json:"compiler_version"`
	EinoBuild        string `json:"eino_build"`
}

// Meta reports the frozen program identity.
func (p *Program) Meta() ProgramMeta { return p.meta }

// Run executes one admitted run. Identity and limits are rechecked
// against the compiled program before any work; a mismatch or a limit
// wider than the compile ceiling is rejected.
func (p *Program) Run(ctx context.Context, request RunRequest, bindings Bindings) (RunResult, error) {
	if p == nil || request.Ref.ProgramDigest != p.meta.ProgramDigest {
		return RunResult{Status: RunFailed}, &Error{
			Code:    ErrCheckpointIncompatible,
			Path:    request.Ref.RunID,
			Message: "run request does not match the compiled program identity",
		}
	}
	return RunResult{Status: RunFailed}, &Error{
		Code:    ErrUnsupportedFeature,
		Path:    request.Ref.RunID,
		Message: "execution reserved for S05",
	}
}

// Compile performs structural validation and compilation without I/O
// effects. It resolves every call type against the frozen catalog;
// graph translation itself is owned by S03 and reports
// unsupported_feature until then — never a false success.
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
	return nil, nil, &Error{
		Code:    ErrUnsupportedFeature,
		Message: "graph compilation reserved for S03",
	}
}

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
