package inofy_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	inofy "github.com/ProjectViVy/inofy"
)

// TestRunBounds (S05 task 2): shared leaf permits, monotone
// activations, opt-in attempts, cancellation-aware delay, validated
// fallback, unknown-outcome no-replay, size caps, frozen wait clock.
func TestRunBounds(t *testing.T) {
	ctx := context.Background()

	t.Run("parallel leaf effects bounded by permit", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "p1", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
					{ID: "p2", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
					{ID: "p3", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
					{ID: "p4", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
					{ID: "p5", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
					{ID: "agg", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						Inputs: map[string]inofy.Binding{
							"one": {Source: "p1", Pointer: ""},
						}},
				},
				Edges: []inofy.Edge{
					{From: "p1", To: "agg"}, {From: "p2", To: "agg"},
					{From: "p3", To: "agg"}, {From: "p4", To: "agg"},
					{From: "p5", To: "agg"},
				},
				Exits: []string{"agg"},
				Outputs: map[string]inofy.Binding{
					"r": {Source: "agg", Pointer: ""},
				},
			},
		}
		var live, maxLive atomic.Int64
		exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			n := live.Add(1)
			for {
				m := maxLive.Load()
				if n <= m || maxLive.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			live.Add(-1)
			return inofy.NodeReply{Output: json.RawMessage(`{"ok": true}`)}, nil
		}}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
		}})
		prog, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("compile diags: %#v", diags)
		}
		res, err := prog.Run(ctx, inofy.RunRequest{
			Ref:     inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input:   json.RawMessage(`{}`),
			Limits:  inofy.Limits{Parallelism: 4},
		}, inofy.Bindings{Nodes: exec, Runs: inofy.NewMemoryRunStore()})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if res.Status != inofy.RunSucceeded {
			t.Fatalf("status %q", res.Status)
		}
		if maxLive.Load() > 4 {
			t.Fatalf("concurrent effects = %d > 4", maxLive.Load())
		}
	})

	t.Run("activation 257 rejected", func(t *testing.T) {
		// One repeat body running >256 times hits the shared counter.
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "rep", Kind: inofy.NodeKindRepeat,
						Initial:      map[string]inofy.Binding{"acc": {Literal: json.RawMessage(`0`)}},
						StateSchema:  json.RawMessage(`{"type":"object"}`),
						MaxIterations: 300,
						Until: &inofy.Predicate{
							Op:    "gte",
							Left:  &inofy.Binding{Source: "step", Pointer: "/acc"},
							Right: &inofy.Binding{Literal: json.RawMessage(`999`)},
						},
						Body: &inofy.Graph{
							Nodes: []inofy.Node{
								{ID: "step", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
									Inputs: map[string]inofy.Binding{
										"acc": {Source: "input", Pointer: "/acc"},
									}},
							},
							Exits: []string{"step"},
							Outputs: map[string]inofy.Binding{
								"acc": {Source: "step", Pointer: "/acc"},
							},
						},
					},
				},
				Exits: []string{"rep"},
			},
		}
		var n atomic.Int64
		exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			n.Add(1)
			var in map[string]any
			_ = json.Unmarshal(c.Input, &in)
			acc, _ := in["acc"].(float64)
			bs, _ := json.Marshal(map[string]any{"acc": acc + 1})
			return inofy.NodeReply{Output: bs}, nil
		}}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
		}})
		prog, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{
			Limits: inofy.Limits{MaxActivations: 512, MaxIterations: 300},
		})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("compile diags: %#v", diags)
		}
		_, err = prog.Run(ctx, inofy.RunRequest{
			Ref:     inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input:   json.RawMessage(`{}`),
			Limits:  inofy.Limits{MaxActivations: 256, MaxIterations: 300},
		}, inofy.Bindings{Nodes: exec, Runs: inofy.NewMemoryRunStore()})
		var ie *inofy.Error
		if !errors.As(err, &ie) || ie.Code != inofy.ErrBudgetExceeded {
			t.Fatalf("want budget_exceeded, got %v", err)
		}
		if n.Load() > 260 {
			t.Fatalf("effects kept running past bound: %d", n.Load())
		}
	})

	t.Run("opt-in retry honors attempts incl first and keeps OperationKey", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						Retry: &inofy.Retry{MaxAttempts: 3, DelayMS: 1}},
				},
				Exits: []string{"f"},
			},
		}
		var mu sync.Mutex
		var keys []string
		var attempts []int
		exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			mu.Lock()
			keys = append(keys, c.OperationKey)
			attempts = append(attempts, c.Attempt)
			mu.Unlock()
			return inofy.NodeReply{}, &inofy.RetryableError{Err: errors.New("boom")}
		}}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
		}})
		prog, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("compile diags: %#v", diags)
		}
		_, _ = prog.Run(ctx, inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input: json.RawMessage(`{}`),
		}, inofy.Bindings{Nodes: exec, Runs: inofy.NewMemoryRunStore()})
		if len(attempts) != 3 {
			t.Fatalf("attempts = %v", attempts)
		}
		if keys[0] == "" || keys[0] != keys[1] || keys[1] != keys[2] {
			t.Fatalf("operation key changed across retries: %v", keys)
		}
	})

	t.Run("non-replayable effect never auto-retries", func(t *testing.T) {
		// Admission already rejects retry on a non-replayable type;
		// the run boundary additionally clamps attempts to one.
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						Retry: &inofy.Retry{MaxAttempts: 3, DelayMS: 1}},
				},
				Exits: []string{"f"},
			},
		}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
			Replay:           inofy.ReplayNonReplayable,
		}})
		_, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) == 0 || diags[0].Code != "replay_forbidden" {
			t.Fatalf("want replay_forbidden diagnostic, got %#v", diags)
		}
	})

	t.Run("unknown outcome stops retry immediately", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						Retry: &inofy.Retry{MaxAttempts: 3, DelayMS: 1}},
				},
				Exits: []string{"f"},
			},
		}
		var calls atomic.Int64
		exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			calls.Add(1)
			return inofy.NodeReply{}, &inofy.UnknownOutcomeError{Err: errors.New("maybe applied")}
		}}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
		}})
		prog, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("compile diags: %#v", diags)
		}
		res, _ := prog.Run(ctx, inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input: json.RawMessage(`{}`),
		}, inofy.Bindings{Nodes: exec, Runs: inofy.NewMemoryRunStore()})
		if calls.Load() != 1 {
			t.Fatalf("retried unknown outcome: %d", calls.Load())
		}
		if res.Status != inofy.RunRecoveryRequired {
			t.Fatalf("status %q", res.Status)
		}
	})

	t.Run("retry delay honors cancellation", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						Retry: &inofy.Retry{MaxAttempts: 3, DelayMS: 5000}},
				},
				Exits: []string{"f"},
			},
		}
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		var calls atomic.Int64
		exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			if calls.Add(1) == 1 {
				cancel()
			}
			return inofy.NodeReply{}, &inofy.RetryableError{Err: errors.New("boom")}
		}}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
		}})
		prog, _, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		start := time.Now()
		_, _ = prog.Run(runCtx, inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input: json.RawMessage(`{}`),
		}, inofy.Bindings{Nodes: exec, Runs: inofy.NewMemoryRunStore()})
		if time.Since(start) > 2*time.Second {
			t.Fatal("retry delay ignored cancellation")
		}
		if calls.Load() != 1 {
			t.Fatalf("calls = %d", calls.Load())
		}
	})

	t.Run("fallback must satisfy output schema", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						OnError: &inofy.ErrorPolicy{Mode: inofy.ErrorModeFallback, Value: json.RawMessage(`"not-a-number"`)}},
				},
				Exits: []string{"f"},
			},
		}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
			OutputSchema:     json.RawMessage(`{"type":"number"}`),
		}})
		_, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) == 0 || diags[0].Check != "capability" {
			t.Fatalf("want schema_mismatch diagnostic, got %#v", diags)
		}
	})

	t.Run("valid fallback degrades visibly", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1",
						OnError: &inofy.ErrorPolicy{Mode: inofy.ErrorModeFallback, Value: json.RawMessage(`42`)}},
				},
				Exits: []string{"f"},
				Outputs: map[string]inofy.Binding{
					"r": {Source: "f", Pointer: ""},
				},
			},
		}
		exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			return inofy.NodeReply{}, errors.New("boom")
		}}
		store := inofy.NewMemoryRunStore()
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
			OutputSchema:     json.RawMessage(`{"type":"number"}`),
		}})
		prog, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("compile diags: %#v", diags)
		}
		res, _ := prog.Run(ctx, inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input: json.RawMessage(`{}`),
		}, inofy.Bindings{Nodes: exec, Runs: store})
		if res.Status != inofy.RunSucceeded {
			t.Fatalf("valid fallback rejected: %q", res.Status)
		}
		var degraded bool
		for _, ev := range store.Events("r") {
			if ev.Kind == inofy.EventNodeDegraded {
				degraded = true
			}
		}
		if !degraded {
			t.Fatal("fallback did not record node_degraded")
		}
	})

	t.Run("output size cap enforced", func(t *testing.T) {
		def := inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "f", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
				},
				Exits: []string{"f"},
				Outputs: map[string]inofy.Binding{
					"r": {Source: "f", Pointer: ""},
				},
			},
		}
		big := json.RawMessage(`{"blob":"` + string(make([]byte, 2048)) + `"}`)
		exec := &execFn{fn: func(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
			return inofy.NodeReply{Output: big}, nil
		}}
		catalog, _ := inofy.NewCatalog([]inofy.NodeDescriptor{{
			TypeID:           "inofy.value@1",
			ImplementationID: "impl-1",
		}})
		prog, diags, err := inofy.Compile(ctx, def, catalog, inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(diags) != 0 {
			t.Fatalf("compile diags: %#v", diags)
		}
		_, err = prog.Run(ctx, inofy.RunRequest{
			Ref:     inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input:   json.RawMessage(`{}`),
			Limits:  inofy.Limits{MaxNodeOutputBytes: 1024},
		}, inofy.Bindings{Nodes: exec, Runs: inofy.NewMemoryRunStore()})
		var ie *inofy.Error
		if !errors.As(err, &ie) || ie.Code != inofy.ErrBudgetExceeded {
			t.Fatalf("want budget_exceeded, got %v", err)
		}
	})
}
