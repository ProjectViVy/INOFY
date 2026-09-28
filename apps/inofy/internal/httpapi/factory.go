package httpapi

import (
	"context"
	"fmt"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/dispatch"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
)

// Factory builds the dispatcher's ProgramFactory: rebuild the
// program from the run's immutable source snapshot — never from
// mutable workflow state — and bind it to the App's executor and
// SQLite authority.
func Factory(store *storage.Store, catalog inofy.Catalog, nodes inofy.NodeExecutor) dispatch.ProgramFactory {
	return func(runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error) {
		source, input, _, _, err := store.RunSource(context.Background(), runID)
		if err != nil {
			return nil, inofy.Bindings{}, inofy.RunRequest{}, err
		}
		art, _, err := inofy.DecodeArtifact(source)
		if err != nil {
			return nil, inofy.Bindings{}, inofy.RunRequest{}, err
		}
		prog, diags, err := inofy.Compile(context.Background(), art.Definition, catalog, inofy.CompileOptions{})
		if err != nil {
			return nil, inofy.Bindings{}, inofy.RunRequest{},
				fmt.Errorf("compile admitted snapshot: %v (%d diagnostics)", err, len(diags))
		}
		req := inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: runID, ProgramDigest: prog.Meta().ProgramDigest},
			Input: input,
		}
		return prog, inofy.Bindings{Nodes: nodes, Runs: store}, req, nil
	}
}
