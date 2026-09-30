package inofy

import (
	"encoding/json"
	"testing"
)

func ev(kind EventKind, path string, attempt int, data string) Event {
	var d json.RawMessage
	if data != "" {
		d = json.RawMessage(data)
	}
	return Event{Kind: kind, Path: path, Attempt: attempt, Data: d}
}

func TestProjectNodesRunningAttempt(t *testing.T) {
	got := ProjectNodes(
		[]Event{
			ev(EventNodeStarted, "/graph/nodes/a", 0, ""),
			ev(EventNodeAttempt, "/graph/nodes/a", 1, ""),
		}, nil)
	if len(got) != 1 {
		t.Fatalf("want 1 node, got %d", len(got))
	}
	n := got[0]
	if n.Path != "/graph/nodes/a" || n.State != NodeRunning || n.Attempts != 1 {
		t.Fatalf("unexpected projection: %+v", n)
	}
	if len(n.UnresolvedAttempts) != 1 || n.UnresolvedAttempts[0] != 1 {
		t.Fatalf("attempt 1 must be unresolved: %+v", n.UnresolvedAttempts)
	}
}

func TestProjectNodesCompleted(t *testing.T) {
	got := ProjectNodes(
		[]Event{
			ev(EventNodeStarted, "/graph/nodes/a", 0, ""),
			ev(EventNodeAttempt, "/graph/nodes/a", 1, ""),
			ev(EventNodeCompleted, "/graph/nodes/a", 1, ""),
		},
		[]ProtectedResult{{Path: "/graph/nodes/a", Attempt: 1, Output: json.RawMessage(`{"x":1}`)}},
	)
	n := got[0]
	if n.State != NodeCompleted || len(n.UnresolvedAttempts) != 0 {
		t.Fatalf("completed node must clear unresolved: %+v", n)
	}
	if n.Results != 1 || n.ResultBytes != int64(len(`{"x":1}`)) {
		t.Fatalf("result projection wrong: %+v", n)
	}
}

func TestProjectNodesWaiting(t *testing.T) {
	got := ProjectNodes(
		[]Event{
			ev(EventNodeAttempt, "/graph/nodes/a", 1, ""),
			ev(EventNodeWait, "/graph/nodes/a", 1, `{"request_id":"req-1"}`),
		}, nil)
	n := got[0]
	if n.State != NodeWaiting || n.WaitingOn != "req-1" {
		t.Fatalf("waiting projection wrong: %+v", n)
	}
	if len(n.UnresolvedAttempts) != 0 {
		t.Fatalf("waiting attempt is resolved: %+v", n.UnresolvedAttempts)
	}
}

func TestProjectNodesFailedClearsAllAttempts(t *testing.T) {
	// node_failed commits with attempt 0 once every retry is spent: it is
	// terminal for the whole call, so all pending attempts resolve.
	got := ProjectNodes(
		[]Event{
			ev(EventNodeAttempt, "/graph/nodes/a", 1, ""),
			ev(EventNodeAttempt, "/graph/nodes/a", 2, ""),
			ev(EventNodeFailed, "/graph/nodes/a", 0, ""),
		}, nil)
	n := got[0]
	if n.State != NodeFailed || n.Attempts != 2 {
		t.Fatalf("failed projection wrong: %+v", n)
	}
	if len(n.UnresolvedAttempts) != 0 {
		t.Fatalf("terminal failure must resolve attempts: %+v", n.UnresolvedAttempts)
	}
}

func TestProjectNodesDegraded(t *testing.T) {
	got := ProjectNodes(
		[]Event{
			ev(EventNodeAttempt, "/graph/nodes/a", 1, ""),
			ev(EventNodeDegraded, "/graph/nodes/a", 0, `{"cause":"budget_exceeded"}`),
		},
		[]ProtectedResult{{Path: "/graph/nodes/a", Output: json.RawMessage(`"fallback"`)}},
	)
	n := got[0]
	if n.State != NodeDegraded || n.Cause != "budget_exceeded" {
		t.Fatalf("degraded projection wrong: %+v", n)
	}
	if n.ResultBytes != int64(len(`"fallback"`)) || len(n.UnresolvedAttempts) != 0 {
		t.Fatalf("degraded must project fallback result: %+v", n)
	}
}

func TestProjectNodesSortedAndScoped(t *testing.T) {
	got := ProjectNodes(
		[]Event{
			ev(EventNodeStarted, "/graph/nodes/z", 0, ""),
			ev(EventNodeStarted, "/graph/nodes/a", 0, ""),
			ev(EventRunStarted, "", 0, ""),
			ev(EventSwitchDecision, "/graph/nodes/sw", 0, ""),
		}, nil)
	// Only authored-path lifecycle events project; run events and
	// engine-control events (never emitted today) are ignored.
	if len(got) != 2 {
		t.Fatalf("want 2 node projections, got %d", len(got))
	}
	if got[0].Path != "/graph/nodes/a" || got[1].Path != "/graph/nodes/z" {
		t.Fatalf("projections must sort by path: %+v", got)
	}
	if got[0].State != NodeRunning || got[1].State != NodeRunning {
		t.Fatalf("started nodes must project running: %+v", got)
	}
}
