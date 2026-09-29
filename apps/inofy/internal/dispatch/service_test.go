package dispatch_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/dispatch"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
)

type countingExec struct{ calls *atomic.Int64 }

func (e *countingExec) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	e.calls.Add(1)
	return inofy.NodeReply{Output: json.RawMessage(`{"answer":1}`)}, nil
}

// gateExec holds the run inside the executor until released.
type gateExec struct {
	calls *atomic.Int64
	gate  chan struct{}
}

func (e *gateExec) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	e.calls.Add(1)
	select {
	case <-e.gate:
	case <-ctx.Done():
		return inofy.NodeReply{}, ctx.Err()
	}
	return inofy.NodeReply{Output: json.RawMessage(`{"answer":1}`)}, nil
}

func appFixture(t *testing.T, dir string) (*storage.Store, *dispatch.Service, *countingExec) {
	t.Helper()
	s, err := storage.Open(context.Background(), dir+"/app.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	exec := &countingExec{calls: &atomic.Int64{}}
	d := dispatch.New(s, dispatch.Options{MaxActive: 4, MaxPending: 32}, func(runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error) {
		prog, diags, err := inofy.Compile(context.Background(), testDef(), testCatalog(), inofy.CompileOptions{})
		if err != nil || len(diags) != 0 {
			t.Fatalf("compile: %v %#v", err, diags)
		}
		return prog, inofy.Bindings{Nodes: exec, Runs: s}, inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: runID, ProgramDigest: prog.Meta().ProgramDigest},
			Input: json.RawMessage(`{}`),
		}, nil
	})
	return s, d, exec
}

func testDef() inofy.Definition {
	return inofy.Definition{
		SchemaVersion: inofy.SchemaVersionV1,
		Graph: inofy.Graph{
			Nodes:   []inofy.Node{{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1"}},
			Edges:   []inofy.Edge{},
			Exits:   []string{"a"},
			Outputs: map[string]inofy.Binding{"r": {Source: "a", Pointer: "/answer"}},
		},
	}
}

func testCatalog() inofy.Catalog {
	c, _ := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.value@1", ImplementationID: "impl-a"},
	})
	return c
}

func TestRestartClassificationAndQueueBounds(t *testing.T) {
	ctx := context.Background()

	t.Run("32 pending admitted; 33rd rejected without hidden row", func(t *testing.T) {
		dir := t.TempDir()
		s, err := storage.Open(ctx, dir+"/app.db")
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		gate := make(chan struct{})
		exec := &gateExec{calls: &atomic.Int64{}, gate: gate}
		d := dispatch.New(s, dispatch.Options{MaxActive: 4, MaxPending: 32}, func(runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error) {
			prog, _, _ := inofy.Compile(ctx, testDef(), testCatalog(), inofy.CompileOptions{})
			return prog, inofy.Bindings{Nodes: exec, Runs: s}, inofy.RunRequest{
				Ref:   inofy.ExecutionRef{RunID: runID, ProgramDigest: prog.Meta().ProgramDigest},
				Input: json.RawMessage(`{}`),
			}, nil
		})
		d.Start(ctx)
		defer d.Stop()
		// 4 workers block on the gate; the next 32 fill the pending
		// queue; the 33rd+ are rejected without a hidden row.
		var admitted, rejected int
		for i := 0; i < 38; i++ {
			id, err := d.Admit(ctx, "p1", "wf", 1, json.RawMessage(`{}`), json.RawMessage(`{}`), "k"+itoa(i))
			if err != nil {
				rejected++
				continue
			}
			_ = id
			admitted++
			if exec.calls.Load() >= 4 && rejected > 0 {
				break
			}
		}
		if rejected == 0 {
			t.Fatalf("queue never rejected: admitted=%d", admitted)
		}
		pending, _ := s.PendingCount(ctx)
		if pending > 32 {
			t.Fatalf("pending rows=%d > bound 32", pending)
		}
	})

	t.Run("admitted before wakeup dispatches exactly once after restart", func(t *testing.T) {
		dir := t.TempDir()
		s, d, _ := appFixture(t, dir)
		// Admit WITHOUT starting the dispatcher (crash between admit
		// and wakeup).
		id, err := d.Admit(ctx, "p1", "wf", 1, json.RawMessage(`{}`), json.RawMessage(`{}`), "k1")
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		s.Close()
		// Reopen: admitted row must dispatch once.
		s2, err := storage.Open(ctx, dir+"/app.db")
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		exec2 := &countingExec{calls: &atomic.Int64{}}
		d2 := dispatch.New(s2, dispatch.Options{MaxActive: 4, MaxPending: 32}, func(runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error) {
			prog, _, err := inofy.Compile(ctx, testDef(), testCatalog(), inofy.CompileOptions{})
			if err != nil {
				return nil, inofy.Bindings{}, inofy.RunRequest{}, err
			}
			return prog, inofy.Bindings{Nodes: exec2, Runs: s2}, inofy.RunRequest{
				Ref:   inofy.ExecutionRef{RunID: runID, ProgramDigest: prog.Meta().ProgramDigest},
				Input: json.RawMessage(`{}`),
			}, nil
		})
		d2.Start(ctx)
		defer d2.Stop()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			st, _ := s2.Load(ctx, id)
			if st.Status == inofy.RunSucceeded {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		st, _ := s2.Load(ctx, id)
		if st.Status != inofy.RunSucceeded {
			t.Fatalf("run did not dispatch to success: %q", st.Status)
		}
		if exec2.calls.Load() != 1 {
			t.Fatalf("effect ran %d times, want exactly once", exec2.calls.Load())
		}
	})

	t.Run("running on restart classifies recovery_required, never replays", func(t *testing.T) {
		dir := t.TempDir()
		s, err := storage.Open(ctx, dir+"/app.db")
		if err != nil {
			t.Fatal(err)
		}
		// Seed a running run directly (simulated crash mid-effect).
		ref := inofy.ExecutionRef{RunID: "r", ProgramDigest: "p"}
		admitData, _ := json.Marshal(map[string]any{"input_digest": "sha256:x", "limits": inofy.Limits{}})
		if _, err := s.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "r/0/a/0/1",
			Events:     []inofy.Event{{Kind: inofy.EventRunAdmitted, Data: admitData}},
			Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "r/0/s/0/2",
			Events:     []inofy.Event{{Kind: inofy.EventRunStarted}},
			Transition: inofy.StateTransition{Expected: inofy.RunAdmitted, Target: inofy.RunRunning},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Commit(ctx, ref, inofy.RunCommit{
			CommitID: "r/0/n/1/3",
			Events: []inofy.Event{
				{Kind: inofy.EventNodeStarted, Path: "a"},
				{Kind: inofy.EventNodeAttempt, Path: "a", Attempt: 1},
			},
			Transition: inofy.StateTransition{Expected: inofy.RunRunning, Target: inofy.RunRunning},
		}); err != nil {
			t.Fatal(err)
		}
		s.Close()
		s2, _ := storage.Open(ctx, dir+"/app.db")
		defer s2.Close()
		exec2 := &countingExec{calls: &atomic.Int64{}}
		d2 := dispatch.New(s2, dispatch.Options{MaxActive: 4, MaxPending: 32}, func(runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error) {
			prog, _, _ := inofy.Compile(ctx, testDef(), testCatalog(), inofy.CompileOptions{})
			return prog, inofy.Bindings{Nodes: exec2, Runs: s2}, inofy.RunRequest{
				Ref:   inofy.ExecutionRef{RunID: runID, ProgramDigest: prog.Meta().ProgramDigest},
				Input: json.RawMessage(`{}`),
			}, nil
		})
		d2.Start(ctx)
		defer d2.Stop()
		// Reconciliation marks it recovery_required synchronously.
		deadline := time.Now().Add(3 * time.Second)
		var st inofy.RecoveryState
		for time.Now().Before(deadline) {
			st, _ = s2.Load(ctx, "r")
			if st.Status == inofy.RunRecoveryRequired {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if st.Status != inofy.RunRecoveryRequired {
			t.Fatalf("running row on reopen = %q, want recovery_required", st.Status)
		}
		if exec2.calls.Load() != 0 {
			t.Fatalf("crashed effect replayed %d times", exec2.calls.Load())
		}
	})

	t.Run("waiting row stays quiescent across restart", func(t *testing.T) {
		dir := t.TempDir()
		s, _ := storage.Open(ctx, dir+"/app.db")
		ref := inofy.ExecutionRef{RunID: "w", ProgramDigest: "p"}
		admitData, _ := json.Marshal(map[string]any{"input_digest": "sha256:x", "limits": inofy.Limits{}})
		s.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "w/0/a/0/1",
			Events:     []inofy.Event{{Kind: inofy.EventRunAdmitted, Data: admitData}},
			Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted},
		})
		s.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "w/0/s/0/2",
			Events:     []inofy.Event{{Kind: inofy.EventRunStarted}},
			Transition: inofy.StateTransition{Expected: inofy.RunAdmitted, Target: inofy.RunRunning},
		})
		waitData, _ := json.Marshal(map[string]any{
			"waits":      []inofy.WaitRequest{{RequestID: "req", Kind: "human"}},
			"interrupts": map[string]string{"req": "i1"},
			"gates":      []string{},
		})
		s.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "w/0/w/1/3",
			Events:     []inofy.Event{{Kind: inofy.EventRunWaiting, Data: waitData}},
			Transition: inofy.StateTransition{Expected: inofy.RunRunning, Target: inofy.RunWaiting},
		})
		s.Close()
		s2, _ := storage.Open(ctx, dir+"/app.db")
		defer s2.Close()
		d2 := dispatch.New(s2, dispatch.Options{MaxActive: 4, MaxPending: 32}, func(runID string) (*inofy.Program, inofy.Bindings, inofy.RunRequest, error) {
			prog, _, _ := inofy.Compile(ctx, testDef(), testCatalog(), inofy.CompileOptions{})
			return prog, inofy.Bindings{Nodes: &countingExec{calls: &atomic.Int64{}}, Runs: s2}, inofy.RunRequest{
				Ref:   inofy.ExecutionRef{RunID: runID, ProgramDigest: prog.Meta().ProgramDigest},
				Input: json.RawMessage(`{}`),
			}, nil
		})
		d2.Start(ctx)
		time.Sleep(150 * time.Millisecond)
		d2.Stop()
		st, _ := s2.Load(ctx, "w")
		if st.Status != inofy.RunWaiting {
			t.Fatalf("waiting row woke: %q", st.Status)
		}
	})

	t.Run("same admission key replays one run", func(t *testing.T) {
		dir := t.TempDir()
		s, d, _ := appFixture(t, dir)
		defer s.Close()
		id1, err := d.Admit(ctx, "p1", "wf", 1, json.RawMessage(`{}`), json.RawMessage(`{}`), "dup-key")
		if err != nil {
			t.Fatal(err)
		}
		id2, err := d.Admit(ctx, "p1", "wf", 1, json.RawMessage(`{}`), json.RawMessage(`{}`), "dup-key")
		if err != nil {
			t.Fatal(err)
		}
		if id1 != id2 {
			t.Fatalf("same admission key allocated two runs: %q %q", id1, id2)
		}
	})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
