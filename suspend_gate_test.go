package inofy

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ProjectViVy/inofy/internal/einoruntime"
)

// This test controls the executor-adapter boundary rather than adding a
// production scheduling hook. The sibling's runtime wrapper has entered,
// but its journal admission must wait until both human waits close the gate.
// Removing gate suspension, dropping its checkpoint address, or failing to
// restore it must fail this test.
func TestSuspensionDeferredSibling(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	def := Definition{SchemaVersion: SchemaVersionV1, Graph: Graph{
		Nodes: []Node{{ID: "w1", Kind: NodeKindCall, Type: "inofy.waiter@1"}, {ID: "w2", Kind: NodeKindCall, Type: "inofy.waiter@1"}, {ID: "s", Kind: NodeKindCall, Type: "inofy.value@1"}},
		Exits: []string{"w1", "w2", "s"},
	}}
	catalog, err := NewCatalog([]NodeDescriptor{
		{TypeID: "inofy.waiter@1", ImplementationID: "impl-w", SupportsWait: true, OutputSchema: json.RawMessage(`{"type":"object"}`)},
		{TypeID: "inofy.value@1", ImplementationID: "impl-1", OutputSchema: json.RawMessage(`{"type":"object"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	compile := func() *Program {
		t.Helper()
		p, diags, err := Compile(ctx, def, catalog, CompileOptions{ProgramID: "test-program"})
		if err != nil || len(diags) != 0 {
			t.Fatalf("compile: %v %#v", err, diags)
		}
		return p
	}
	p := compile()
	store := NewMemoryRunStore()
	request := RunRequest{Ref: ExecutionRef{RunID: "r", ProgramID: p.Meta().ProgramID}, Input: json.RawMessage(`{}`)}
	limits := effectiveLimits(Limits{})
	j := newRunJournal(request.Ref, store, limits)
	// Use the same durable admission boundaries as Run. Direct invocation
	// below is needed only to insert the test's executor-adapter barrier.
	admitData, err := json.Marshal(map[string]any{"limits": limits})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.commitAdmission(ctx, "r", 0, StateTransition{Expected: "", Target: RunAdmitted}, []Event{{Kind: EventRunAdmitted, Data: admitData}}, request.Input); err != nil {
		t.Fatal(err)
	}
	if err := j.commit(ctx, "r", 0, StateTransition{Expected: RunAdmitted, Target: RunRunning}, []Event{{Kind: EventRunStarted}}, nil); err != nil {
		t.Fatal(err)
	}
	exec := &deferredSiblingExecutor{arrived: make(chan struct{}, 2), release: make(chan struct{})}
	adapter := executorAdapter(j, exec)
	entered := make(chan struct{})
	waitsSettled := make(chan struct{})
	var waits atomic.Int64
	scheduled := func(ctx context.Context, c einoruntime.Call) (json.RawMessage, error) {
		if c.TypeID == "inofy.value@1" {
			close(entered)
			select {
			case <-waitsSettled:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		out, err := adapter(ctx, c)
		if c.TypeID == "inofy.waiter@1" && waits.Add(1) == 2 {
			close(waitsSettled)
		}
		return out, err
	}
	type outcome struct {
		res einoruntime.InvokeResult
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := p.rt.Invoke(ctx, request.Input, scheduled, einoruntime.RunOptions{CheckPointID: "r/0", Gate: &j.suspending})
		done <- outcome{res, err}
	}()
	await := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	await(entered)
	await(exec.arrived)
	await(exec.arrived)
	close(exec.release)
	var completed outcome
	select {
	case completed = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if completed.err != nil || completed.res.Suspend == nil {
		t.Fatalf("invoke: suspend=%v err=%v", completed.res.Suspend, completed.err)
	}
	gateID := ""
	for _, point := range completed.res.Suspend.Points {
		if point.Kind == einoruntime.InterruptGate && point.Ref == "/graph/nodes/s" {
			gateID = point.ID
		}
	}
	if gateID == "" {
		t.Fatalf("sibling gate missing: %#v", completed.res.Suspend.Points)
	}
	suspended, err := p.suspend(ctx, request, j, completed.res)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx, "r")
	if err != nil {
		t.Fatal(err)
	}
	if suspended.Status != RunWaiting || len(suspended.Waits) != 2 || len(state.Gates) != 1 || state.Gates[0] != gateID || exec.sibling.Load() != 0 {
		t.Fatalf("suspend: status=%s waits=%d gates=%v sibling=%d", suspended.Status, len(suspended.Waits), state.Gates, exec.sibling.Load())
	}
	if state.LatestCheckpoint == nil || len(state.LatestCheckpoint.Payload) == 0 {
		t.Fatal("missing durable checkpoint")
	}
	for _, ev := range store.Events("r") {
		if ev.Path == "/graph/nodes/s" && ev.Kind == EventNodeStarted {
			t.Fatal("sibling started before resume")
		}
	}
	answers := map[string]json.RawMessage{}
	for _, wait := range suspended.Waits {
		answers[wait.RequestID] = json.RawMessage(`{"ok":true}`)
	}
	// A fresh runtime must restore the saved gate from the committed store,
	// not from the previous Program's in-memory checkpoint staging buffer.
	restored := compile()
	result, err := restored.Run(ctx, RunRequest{Ref: request.Ref, Resume: &ResumeRequest{Answers: answers}}, Bindings{Nodes: exec, Runs: store})
	if err != nil || result.Status != RunSucceeded || exec.sibling.Load() != 1 || exec.continued.Load() != 2 {
		t.Fatalf("resume: status=%s sibling=%d continuations=%d err=%v", result.Status, exec.sibling.Load(), exec.continued.Load(), err)
	}
}

type deferredSiblingExecutor struct {
	arrived   chan struct{}
	release   chan struct{}
	sibling   atomic.Int64
	continued atomic.Int64
}

func (e *deferredSiblingExecutor) Execute(ctx context.Context, c NodeCall) (NodeReply, error) {
	if c.TypeID == "inofy.value@1" {
		e.sibling.Add(1)
		return NodeReply{Output: json.RawMessage(`{"ok":true}`)}, nil
	}
	if len(c.Continuation) > 0 {
		e.continued.Add(1)
		return NodeReply{Output: c.Continuation}, nil
	}
	e.arrived <- struct{}{}
	select {
	case <-e.release:
	case <-ctx.Done():
		return NodeReply{}, ctx.Err()
	}
	return NodeReply{Wait: &WaitRequest{RequestID: "req-" + c.Path, Kind: "human", Prompt: "approve?", ContinuationRef: "cont-" + c.Path}}, nil
}
