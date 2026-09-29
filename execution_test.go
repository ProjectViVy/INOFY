package inofy_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
)

// faultStore wraps MemoryRunStore and fails the nth Commit matching a
// predicate, letting tests target exact boundaries.
type faultStore struct {
	inner   *inofy.MemoryRunStore
	failOn  func(c inofy.RunCommit) bool
	burned  *atomic.Int64
	commits *atomic.Int64
}

func (f *faultStore) Commit(ctx context.Context, ref inofy.ExecutionRef, c inofy.RunCommit) (inofy.Receipt, error) {
	f.commits.Add(1)
	if f.failOn != nil && f.failOn(c) {
		if f.burned == nil || f.burned.Add(1) == 1 {
			return inofy.Receipt{}, errors.New("injected store failure")
		}
	}
	return f.inner.Commit(ctx, ref, c)
}

func (f *faultStore) Load(ctx context.Context, runID string) (inofy.RecoveryState, error) {
	return f.inner.Load(ctx, runID)
}

type countExec struct {
	n       atomic.Int64
	replies map[string]json.RawMessage
}

func (e *countExec) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	e.n.Add(1)
	if out, ok := e.replies[lastPathID(c.Path)]; ok {
		return inofy.NodeReply{Output: out}, nil
	}
	return inofy.NodeReply{Output: json.RawMessage(`{"ok": true}`)}, nil
}

func twoNodeDef() inofy.Definition {
	return inofy.Definition{
		SchemaVersion: inofy.SchemaVersionV1,
		Graph: inofy.Graph{
			Nodes: []inofy.Node{
				{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
				{ID: "b", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
			},
			Edges: []inofy.Edge{{From: "a", To: "b"}},
			Exits: []string{"b"},
			Outputs: map[string]inofy.Binding{
				"r": {Source: "b", Pointer: ""},
			},
		},
	}
}

func runDef(t *testing.T, store inofy.RunStore, exec inofy.NodeExecutor, def inofy.Definition) (inofy.RunResult, error) {
	t.Helper()
	catalog, err := inofy.NewCatalog([]inofy.NodeDescriptor{{
		TypeID:           "inofy.value@1",
		ImplementationID: "impl-1",
	}})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	prog, diags, err := inofy.Compile(context.Background(), def, catalog, inofy.CompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v (%v)", err, diags)
	}
	return prog.Run(context.Background(), inofy.RunRequest{
		Ref:   inofy.ExecutionRef{RunID: "run-1", ProgramDigest: prog.Meta().ProgramDigest},
		Input: json.RawMessage(`{}`),
	}, inofy.Bindings{Nodes: exec, Runs: store})
}

// TestEffectCommitBoundaries (S05 task 1): durable start/result
// evidence bounds every effect.
func TestEffectCommitBoundaries(t *testing.T) {
	ctx := context.Background()

	t.Run("start commit failure means zero invocations", func(t *testing.T) {
		var burned atomic.Int64
		store := &faultStore{inner: inofy.NewMemoryRunStore(), commits: &atomic.Int64{},
			failOn: func(c inofy.RunCommit) bool {
				for _, ev := range c.Events {
					if ev.Kind == inofy.EventNodeStarted {
						return true
					}
				}
				return false
			}, burned: &burned}
		exec := &countExec{}
		_, err := runDef(t, store, exec, twoNodeDef())
		if err == nil {
			t.Fatal("expected failure")
		}
		if exec.n.Load() != 0 {
			t.Fatalf("executor invoked %d times despite start-commit failure", exec.n.Load())
		}
	})

	t.Run("success then result commit failure yields recovery and no downstream", func(t *testing.T) {
		var burned atomic.Int64
		store := &faultStore{inner: inofy.NewMemoryRunStore(), commits: &atomic.Int64{},
			failOn: func(c inofy.RunCommit) bool {
				for _, ev := range c.Events {
					if ev.Kind == inofy.EventNodeCompleted {
						return true
					}
				}
				return false
			}, burned: &burned}
		exec := &countExec{}
		res, err := runDef(t, store, exec, twoNodeDef())
		if err == nil && res.Status != inofy.RunRecoveryRequired {
			t.Fatalf("want error or recovery_required, got %v %v", res.Status, err)
		}
		// Only the first node's effect ran; b never started.
		if exec.n.Load() != 1 {
			t.Fatalf("invocations = %d, want 1", exec.n.Load())
		}
	})

	t.Run("terminal commit failure is reported not swallowed", func(t *testing.T) {
		var burned atomic.Int64
		store := &faultStore{inner: inofy.NewMemoryRunStore(), commits: &atomic.Int64{},
			failOn: func(c inofy.RunCommit) bool {
				for _, ev := range c.Events {
					if ev.Kind == inofy.EventRunSucceeded {
						return true
					}
				}
				return false
			}, burned: &burned}
		exec := &countExec{}
		res, err := runDef(t, store, exec, twoNodeDef())
		if err == nil || res.Status != inofy.RunRecoveryRequired {
			t.Fatalf("want recovery_required + error, got %v %v", res.Status, err)
		}
	})

	t.Run("CommitID idempotent replay vs body change", func(t *testing.T) {
		store := inofy.NewMemoryRunStore()
		ref := inofy.ExecutionRef{RunID: "r", Epoch: 1, ProgramDigest: "d"}
		c := inofy.RunCommit{CommitID: "c1",
			Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted},
			Events:     []inofy.Event{{Kind: inofy.EventRunAdmitted}}}
		r1, err := store.Commit(ctx, ref, c)
		if err != nil {
			t.Fatalf("first commit: %v", err)
		}
		r2, err := store.Commit(ctx, ref, c)
		if err != nil || r2 != r1 {
			t.Fatalf("replay same body: %v %v", r2, err)
		}
		c.Events = append(c.Events, inofy.Event{Kind: inofy.EventRunStarted})
		_, err = store.Commit(ctx, ref, c)
		var ie *inofy.Error
		if !errors.As(err, &ie) || ie.Code != inofy.ErrIdempotencyConflict {
			t.Fatalf("changed body: %v", err)
		}
	})

	t.Run("stale epoch fails before writes", func(t *testing.T) {
		store := inofy.NewMemoryRunStore()
		ref := inofy.ExecutionRef{RunID: "r", Epoch: 7, ProgramDigest: "d"}
		_, err := store.Commit(ctx, ref, inofy.RunCommit{CommitID: "a",
			Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted}})
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		stale := ref
		stale.Epoch = 6
		_, err = store.Commit(ctx, stale, inofy.RunCommit{CommitID: "b",
			Transition: inofy.StateTransition{Expected: inofy.RunAdmitted, Target: inofy.RunRunning}})
		var ie *inofy.Error
		if !errors.As(err, &ie) || ie.Code != inofy.ErrStaleWriter {
			t.Fatalf("stale epoch: %v", err)
		}
	})

	t.Run("sibling unknown outcome dominates result", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "bad", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
					{ID: "ok", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
				},
				Edges: []inofy.Edge{},
				Exits: []string{"ok", "bad"},
			},
		}
		exec := &countExec{replies: map[string]json.RawMessage{
			"ok": json.RawMessage(`{"done": true}`),
		}}
		unknownExec := inofy.NodeExecutor(&execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			if lastPathID(c.Path) == "bad" {
				return inofy.NodeReply{}, &inofy.UnknownOutcomeError{Err: errors.New("remote may have applied")}
			}
			return exec.Execute(ctx, c)
		}})
		res, _ := runDef(t, inofy.NewMemoryRunStore(), unknownExec, def)
		if res.Status != inofy.RunRecoveryRequired {
			t.Fatalf("status = %q, want recovery_required", res.Status)
		}
	})
}

type execFn struct {
	fn func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error)
}

func (e *execFn) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	return e.fn(ctx, c)
}

// TestEphemeralStoreRestartUnavailable: a second store instance sees
// nothing — process-local only, no restart recovery.
func TestEphemeralStoreRestartUnavailable(t *testing.T) {
	ctx := context.Background()
	first := inofy.NewMemoryRunStore()
	res, err := runDef(t, first, &countExec{}, twoNodeDef())
	if err != nil || res.Status != inofy.RunSucceeded {
		t.Fatalf("run: %v %v", res.Status, err)
	}
	second := inofy.NewMemoryRunStore()
	_, err = second.Load(ctx, "run-1")
	if err == nil {
		t.Fatal("ephemeral store leaked state across instances")
	}
	st, err := first.Load(ctx, "run-1")
	if err != nil || st.Status != inofy.RunSucceeded {
		t.Fatalf("load: %v %v", st.Status, err)
	}
	if len(st.UnresolvedOperations) != 0 {
		t.Fatalf("unresolved ops after success: %v", st.UnresolvedOperations)
	}
}
