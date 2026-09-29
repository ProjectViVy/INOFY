package inofy_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	inofy "github.com/ProjectViVy/inofy"
)

// --- S06 task 1: quiescent wait + atomic visibility -----------------

// suspendExec synchronizes the two waiters behind an arrival barrier
// so both waits are in flight when the first commits — deterministic
// two-wait quiescence. The sibling blocks on release to prove an
// in-flight effect settles before the waiting commit lands.
type suspendExec struct {
	arrive  chan struct{}
	barrier chan struct{}
	release chan struct{}
	sRan    *atomic.Int64
}

func newSuspendExec() *suspendExec {
	return &suspendExec{
		arrive:  make(chan struct{}, 2),
		barrier: make(chan struct{}),
		release: make(chan struct{}),
		sRan:    &atomic.Int64{},
	}
}

func (e *suspendExec) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	switch c.TypeID {
	case "inofy.waiter@1":
		e.arrive <- struct{}{}
		select {
		case <-e.barrier:
		case <-ctx.Done():
			return inofy.NodeReply{}, ctx.Err()
		}
		return inofy.NodeReply{Wait: &inofy.WaitRequest{
			RequestID:       "req-" + c.Path,
			Kind:            "human",
			Prompt:          "approve?",
			ContinuationRef: "cont-" + c.Path,
		}}, nil
	default:
		select {
		case <-e.release:
		case <-ctx.Done():
			return inofy.NodeReply{}, ctx.Err()
		}
		e.sRan.Add(1)
		return inofy.NodeReply{Output: json.RawMessage(`{"ok":true}`)}, nil
	}
}

// waitForEvents polls the store until n events of kind exist or the
// deadline passes; used to interleave the sibling release with the
// committed wait boundary.
func waitForEvents(store *inofy.MemoryRunStore, runID string, kind inofy.EventKind, n int) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		for _, ev := range store.Events(runID) {
			if ev.Kind == kind {
				count++
			}
		}
		if count >= n {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

func suspendDef() inofy.Definition {
	return inofy.Definition{
		SchemaVersion: inofy.SchemaVersionV1,
		Graph: inofy.Graph{
			Nodes: []inofy.Node{
				{ID: "w1", Kind: inofy.NodeKindCall, Type: "inofy.waiter@1"},
				{ID: "w2", Kind: inofy.NodeKindCall, Type: "inofy.waiter@1"},
				{ID: "s", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
			},
			Exits: []string{"w1", "w2", "s"},
		},
	}
}

func suspendCatalog() inofy.Catalog {
	c, _ := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.waiter@1", ImplementationID: "impl-w",
			SupportsWait: true, OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{TypeID: "inofy.value@1", ImplementationID: "impl-1",
			OutputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	return c
}

func TestSuspensionBarrier(t *testing.T) {
	ctx := context.Background()

	t.Run("two waits quiesce; in-flight sibling commits; no new effect starts", func(t *testing.T) {
		exec := newSuspendExec()
		sRan := exec.sRan
		store := inofy.NewMemoryRunStore()
		prog, diags, err := inofy.Compile(ctx, suspendDef(), suspendCatalog(), inofy.CompileOptions{})
		if err != nil || len(diags) != 0 {
			t.Fatalf("compile: %v %#v", err, diags)
		}
		done := make(chan inofy.RunResult, 1)
		go func() {
			res, _ := prog.Run(ctx, inofy.RunRequest{
				Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
				Input: json.RawMessage(`{}`),
			}, inofy.Bindings{Nodes: exec, Runs: store})
			done <- res
		}()
		// Both waits must be in flight before either returns so both
		// prompts commit; the sibling stays mid-effect until then.
		for i := 0; i < 2; i++ {
			<-exec.arrive
		}
		close(exec.barrier)
		if !waitForEvents(store, "r", inofy.EventNodeWait, 2) {
			t.Fatal("two waits never committed")
		}
		close(exec.release)
		res := <-done
		if res.Status != inofy.RunWaiting {
			t.Fatalf("want waiting, got %q", res.Status)
		}
		if len(res.Waits) != 2 {
			t.Fatalf("want 2 outstanding waits, got %d", len(res.Waits))
		}
		var waits, waitingCommit int
		for _, ev := range store.Events("r") {
			switch ev.Kind {
			case inofy.EventNodeWait:
				waits++
			case inofy.EventRunWaiting:
				waitingCommit++
			}
		}
		if waits != 2 || waitingCommit != 1 {
			t.Fatalf("durable waits=%d run_waiting=%d", waits, waitingCommit)
		}
		st, err := store.Load(ctx, "r")
		if err != nil {
			t.Fatalf("load: %v", err)
		}
		if st.Status != inofy.RunWaiting {
			t.Fatalf("store status %q, want waiting", st.Status)
		}
		if st.LatestCheckpoint == nil || len(st.LatestCheckpoint.Payload) == 0 {
			t.Fatal("committed checkpoint envelope missing")
		}
		if sRan.Load() != 1 {
			t.Fatalf("sibling ran %d times, want exactly 1", sRan.Load())
		}
	})

	t.Run("failed checkpoint commit leaves run running, no waiting projection", func(t *testing.T) {
		exec := newSuspendExec()
		close(exec.barrier)
		close(exec.release)
		store := inofy.NewMemoryRunStore()
		inner := store
		fs := &faultStore{inner: inner, commits: &atomic.Int64{},
			failOn: func(c inofy.RunCommit) bool {
				return c.Transition.Target == inofy.RunWaiting
			}}
		prog, _, err := inofy.Compile(ctx, suspendDef(), suspendCatalog(), inofy.CompileOptions{})
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		res, _ := prog.Run(ctx, inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Input: json.RawMessage(`{}`),
		}, inofy.Bindings{Nodes: exec, Runs: fs})
		if res.Status != inofy.RunRecoveryRequired {
			t.Fatalf("want recovery_required, got %q", res.Status)
		}
		st, _ := inner.Load(ctx, "r")
		if st.Status != inofy.RunRunning {
			t.Fatalf("store status %q after failed waiting commit", st.Status)
		}
		if st.LatestCheckpoint != nil {
			t.Fatal("uncommitted checkpoint leaked into recovery state")
		}
	})
}
