package inofy_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
)

// A terminal node verdict must settle the attempt it names in the
// run store's unresolved ledger: a plain failure or a degraded
// fallback leaves no phantom operation behind, while an unknown
// outcome stays listed for host reconciliation (§8.5).
func runFailingNode(t *testing.T, execErr error, onError *inofy.ErrorPolicy) (inofy.RecoveryState, int64) {
	t.Helper()
	ctx := context.Background()
	def := inofy.Definition{
		SchemaVersion: inofy.SchemaVersionV1,
		Graph: inofy.Graph{
			Nodes: []inofy.Node{
				{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1", OnError: onError},
			},
			Exits: []string{"f"},
		},
	}
	catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
		TypeID:           "inofy.value@1",
		ImplementationID: "impl-1",
	}})
	prog, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
	if err != nil || len(diags) != 0 {
		t.Fatalf("compile: err=%v diags=%v", err, diags)
	}
	store := inofy.NewMemoryRunStore()
	var calls int64
	exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
		calls++
		return inofy.NodeReply{}, execErr
	}}
	// Run reports node/run verdicts via res.Status; a non-nil error is the
	// settled failure itself, not a driver failure.
	res, _ := prog.Run(ctx, inofy.RunRequest{
		Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
		Input: json.RawMessage(`{}`),
	}, inofy.Bindings{Nodes: exec, Runs: store})
	_ = res
	st, err := store.Load(ctx, "r")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return st, calls
}

func TestUnresolvedSettlesOnDefiniteFailure(t *testing.T) {
	st, calls := runFailingNode(t, errors.New("boom"), nil)
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if len(st.UnresolvedOperations) != 0 {
		t.Fatalf("definite failure left phantom unresolved ops: %+v", st.UnresolvedOperations)
	}
}

func TestUnresolvedSettlesOnDegradedFallback(t *testing.T) {
	st, calls := runFailingNode(t, errors.New("boom"), &inofy.ErrorPolicy{
		Mode:  inofy.ErrorModeFallback,
		Value: json.RawMessage(`"fallback"`),
	})
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if len(st.UnresolvedOperations) != 0 {
		t.Fatalf("degraded fallback left phantom unresolved ops: %+v", st.UnresolvedOperations)
	}
}

func TestUnresolvedPreservesUnknownOutcome(t *testing.T) {
	st, calls := runFailingNode(t, &inofy.UnknownOutcomeError{Err: errors.New("maybe applied")}, nil)
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
	if len(st.UnresolvedOperations) != 1 || st.UnresolvedOperations[0].Attempt != 1 {
		t.Fatalf("unknown outcome must stay unresolved for reconciliation: %+v", st.UnresolvedOperations)
	}
}
