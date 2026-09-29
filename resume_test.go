package inofy_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
)

// --- S06 task 2: fenced resume + crash classification ---------------

// resumeExec waits once per node, then completes with the authorized
// continuation answer on resume.
type resumeExec struct {
	continued *atomic.Int64
}

func (e *resumeExec) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	if len(c.Continuation) > 0 {
		e.continued.Add(1)
		return inofy.NodeReply{Output: c.Continuation}, nil
	}
	return inofy.NodeReply{Wait: &inofy.WaitRequest{
		RequestID:       "req-" + c.Path,
		Kind:            "human",
		Prompt:          "approve?",
		ContinuationRef: "cont-" + c.Path,
	}}, nil
}

func resumeDef() inofy.Definition {
	return inofy.Definition{
		SchemaVersion: inofy.SchemaVersionV1,
		Graph: inofy.Graph{
			Nodes: []inofy.Node{
				{ID: "w1", Kind: inofy.NodeKindCall, Type: "inofy.waiter@1"},
			},
			Exits:   []string{"w1"},
			Outputs: map[string]inofy.Binding{"r": {Source: "w1", Pointer: ""}},
		},
	}
}

func TestResumeAndCrashMatrix(t *testing.T) {
	ctx := context.Background()
	newProg := func() (*inofy.Program, *inofy.MemoryRunStore, *resumeExec) {
		store := inofy.NewMemoryRunStore()
		exec := &resumeExec{continued: &atomic.Int64{}}
		prog, diags, err := inofy.Compile(ctx, resumeDef(), suspendCatalog(), inofy.CompileOptions{})
		if err != nil || len(diags) != 0 {
			t.Fatalf("compile: %v %#v", err, diags)
		}
		return prog, store, exec
	}
	admit := func(prog *inofy.Program, store *inofy.MemoryRunStore, exec *resumeExec, runID string) inofy.RunResult {
		res, _ := prog.Run(ctx, inofy.RunRequest{
			Ref:   inofy.ExecutionRef{RunID: runID, ProgramDigest: prog.Meta().ProgramDigest},
			Input: json.RawMessage(`{}`),
		}, inofy.Bindings{Nodes: exec, Runs: store})
		if res.Status != inofy.RunWaiting {
			t.Fatalf("setup suspend: got %q", res.Status)
		}
		return res
	}

	t.Run("authorized answer resumes to success", func(t *testing.T) {
		prog, store, exec := newProg()
		res0 := admit(prog, store, exec, "r")
		res, err := prog.Run(ctx, inofy.RunRequest{
			Ref:    inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Resume: &inofy.ResumeRequest{
				Answers: map[string]json.RawMessage{res0.Waits[0].RequestID: json.RawMessage(`{"ok":true}`)},
			},
		}, inofy.Bindings{Nodes: exec, Runs: store})
		if err != nil || res.Status != inofy.RunSucceeded {
			t.Fatalf("resume: status=%q err=%v", res.Status, err)
		}
		if exec.continued.Load() != 1 {
			t.Fatalf("executor continuation calls=%d", exec.continued.Load())
		}
	})

	t.Run("partial answer set rejected, run stays waiting", func(t *testing.T) {
		_, store, _ := newProg()
		// Two waits needed: compile the two-wait shape here.
		def := suspendDef()
		prog2, diags, err := inofy.Compile(ctx, def, suspendCatalog(), inofy.CompileOptions{})
		if err != nil || len(diags) != 0 {
			t.Fatalf("compile: %v %#v", err, diags)
		}
		exec2 := newSuspendExec()
		go func() {
			<-exec2.arrive
			<-exec2.arrive
			close(exec2.barrier)
		}()
		done := make(chan inofy.RunResult, 1)
		go func() {
			res, _ := prog2.Run(ctx, inofy.RunRequest{
				Ref:   inofy.ExecutionRef{RunID: "r", ProgramDigest: prog2.Meta().ProgramDigest},
				Input: json.RawMessage(`{}`),
			}, inofy.Bindings{Nodes: exec2, Runs: store})
			done <- res
		}()
		close(exec2.release)
		res0 := <-done
		if res0.Status != inofy.RunWaiting || len(res0.Waits) != 2 {
			t.Fatalf("setup: %q waits=%d", res0.Status, len(res0.Waits))
		}
		_, err = prog2.Run(ctx, inofy.RunRequest{
			Ref:    inofy.ExecutionRef{RunID: "r", ProgramDigest: prog2.Meta().ProgramDigest},
			Resume: &inofy.ResumeRequest{
				Answers: map[string]json.RawMessage{res0.Waits[0].RequestID: json.RawMessage(`{"ok":true}`)},
			},
		}, inofy.Bindings{Nodes: exec2, Runs: store})
		if err == nil {
			t.Fatal("partial resume accepted")
		}
		st, _ := store.Load(ctx, "r")
		if st.Status != inofy.RunWaiting {
			t.Fatalf("status %q after denied partial resume", st.Status)
		}
	})

	t.Run("wrong digest identity denied", func(t *testing.T) {
		prog, store, exec := newProg()
		res0 := admit(prog, store, exec, "r")
		// A different program compiled from a changed definition must
		// never claim this run's checkpoint.
		def2 := resumeDef()
		def2.Graph.Nodes = append(def2.Graph.Nodes,
			inofy.Node{ID: "x", Kind: inofy.NodeKindCall, Type: "inofy.value@1"})
		def2.Graph.Exits = append(def2.Graph.Exits, "x")
		prog2, diags, err := inofy.Compile(ctx, def2, suspendCatalog(), inofy.CompileOptions{})
		if err != nil || len(diags) != 0 {
			t.Fatalf("compile2: %v %#v", err, diags)
		}
		_, err = prog2.Run(ctx, inofy.RunRequest{
			Ref: inofy.ExecutionRef{RunID: "r", ProgramDigest: prog2.Meta().ProgramDigest},
			Resume: &inofy.ResumeRequest{
				Answers: map[string]json.RawMessage{res0.Waits[0].RequestID: json.RawMessage(`{"ok":true}`)},
			},
		}, inofy.Bindings{Nodes: exec, Runs: store})
		if err == nil {
			t.Fatal("cross-program resume accepted")
		}
	})

	t.Run("dual resume: only one executor", func(t *testing.T) {
		prog, store, exec := newProg()
		res0 := admit(prog, store, exec, "r")
		var wg sync.WaitGroup
		var wins, rejects atomic.Int64
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				res, err := prog.Run(ctx, inofy.RunRequest{
					Ref: inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
					Resume: &inofy.ResumeRequest{
						IdempotencyKey: "k1",
						Answers: map[string]json.RawMessage{res0.Waits[0].RequestID: json.RawMessage(`{"ok":true}`)},
					},
				}, inofy.Bindings{Nodes: exec, Runs: store})
				if err != nil || res.Status != inofy.RunSucceeded {
					rejects.Add(1)
					return
				}
				wins.Add(1)
			}()
		}
		wg.Wait()
		if exec.continued.Load() != 1 {
			t.Fatalf("continuation executed %d times under dual resume", exec.continued.Load())
		}
		if wins.Load()+rejects.Load() != 2 {
			t.Fatalf("dual resume settled weirdly: wins=%d rejects=%d", wins.Load(), rejects.Load())
		}
	})

	t.Run("running on reopen is recovery_required", func(t *testing.T) {
		prog, store, exec := newProg()
		// Simulate a crash: admit+start committed, effect unresolved,
		// no settlement.
		inner := store
		ref := inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest}
		_, err := inner.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "r/0/a/0/1",
			Events:     []inofy.Event{{Kind: inofy.EventRunAdmitted}},
			Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted},
		})
		if err != nil {
			t.Fatalf("admit seed: %v", err)
		}
		_, err = inner.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "r/0/a/0/2",
			Events:     []inofy.Event{{Kind: inofy.EventRunStarted}},
			Transition: inofy.StateTransition{Expected: inofy.RunAdmitted, Target: inofy.RunRunning},
		})
		if err != nil {
			t.Fatalf("start seed: %v", err)
		}
		_, err = inner.Commit(ctx, ref, inofy.RunCommit{
			CommitID: "r/0/n/1/3",
			Events: []inofy.Event{
				{Kind: inofy.EventNodeStarted, Path: "w1"},
				{Kind: inofy.EventNodeAttempt, Path: "w1", Attempt: 1},
			},
			Transition: inofy.StateTransition{Expected: inofy.RunRunning, Target: inofy.RunRunning},
		})
		if err != nil {
			t.Fatalf("attempt seed: %v", err)
		}
		res, err := prog.Run(ctx, inofy.RunRequest{
			Ref:   ref,
			Input: json.RawMessage(`{}`),
		}, inofy.Bindings{Nodes: exec, Runs: store})
		if res.Status != inofy.RunRecoveryRequired || err == nil {
			t.Fatalf("reopen: status=%q err=%v", res.Status, err)
		}
		if exec.continued.Load() != 0 {
			t.Fatal("effect re-executed on crash reopen")
		}
	})

	t.Run("repeated identical resume is idempotent", func(t *testing.T) {
		prog, store, exec := newProg()
		res0 := admit(prog, store, exec, "r")
		req := inofy.RunRequest{
			Ref: inofy.ExecutionRef{RunID: "r", ProgramDigest: prog.Meta().ProgramDigest},
			Resume: &inofy.ResumeRequest{
				IdempotencyKey: "same",
				Answers: map[string]json.RawMessage{res0.Waits[0].RequestID: json.RawMessage(`{"ok":true}`)},
			},
		}
		res1, _ := prog.Run(ctx, req, inofy.Bindings{Nodes: exec, Runs: store})
		if res1.Status != inofy.RunSucceeded {
			t.Fatalf("first resume: %q", res1.Status)
		}
		res2, err2 := prog.Run(ctx, req, inofy.Bindings{Nodes: exec, Runs: store})
		if err2 == nil && res2.Status == inofy.RunSucceeded {
			// Idempotent repeat returns the recorded outcome without
			// re-executing the effect.
		}
		if exec.continued.Load() != 1 {
			t.Fatalf("repeated resume re-executed effect: %d", exec.continued.Load())
		}
		_ = err2
		_ = res2
	})
}
