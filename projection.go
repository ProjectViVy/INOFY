package inofy

import (
	"encoding/json"
	"sort"
)

// NodeProjection is the per-node observability rollup a host can derive
// from one run's committed journal replay. It is a pure function of the
// event/result streams: the host owns storage sequence and wall-clock
// timestamps, INOFY owns the lifecycle semantics (§8.1/§8.2).
type NodeProjection struct {
	Path               string    `json:"path"`
	State              NodeState `json:"state"`
	Attempts           int       `json:"attempts"`
	Results            int       `json:"results"`
	ResultBytes        int64     `json:"result_bytes"`
	Cause              string    `json:"cause,omitempty"`
	WaitingOn          string    `json:"waiting_on,omitempty"`
	UnresolvedAttempts []int     `json:"unresolved_attempts,omitempty"`
}

// ProjectNodes replays committed events and protected results into a
// deterministic per-node view, sorted by path. Events must arrive in
// journal order. Run-level events and engine-control events carry no
// lifecycle meaning and are ignored; nodes with no events do not appear
// (hosts infer not_started from the definition).
//
// Terminal semantics: completed, wait, failed and degraded resolve the
// attempt the event names (attempt 0 names none, so an unknown outcome
// keeps its attempt open for host reconciliation — §8.5). Earlier
// retried attempts carry no terminal event and remain unresolved; that
// mirrors the RunStore ledger exactly.
func ProjectNodes(events []Event, results []ProtectedResult) []NodeProjection {
	nodes := make(map[string]*NodeProjection)
	pending := make(map[string]map[int]bool)
	node := func(path string) *NodeProjection {
		n, ok := nodes[path]
		if !ok {
			n = &NodeProjection{Path: path, State: NodeNotStarted}
			nodes[path] = n
			pending[path] = make(map[int]bool)
		}
		return n
	}
	resolve := func(path string, attempt int) {
		delete(pending[path], attempt)
	}
	for _, ev := range events {
		if ev.Path == "" {
			continue
		}
		switch ev.Kind {
		case EventNodeStarted:
			node(ev.Path).State = NodeRunning
		case EventNodeAttempt:
			n := node(ev.Path)
			if ev.Attempt > n.Attempts {
				n.Attempts = ev.Attempt
			}
			pending[ev.Path][ev.Attempt] = true
			if n.State == NodeNotStarted {
				n.State = NodeRunning
			}
		case EventNodeWait:
			n := node(ev.Path)
			n.State = NodeWaiting
			resolve(ev.Path, ev.Attempt)
			var wr WaitRequest
			if json.Unmarshal(ev.Data, &wr) == nil {
				n.WaitingOn = wr.RequestID
			}
		case EventNodeCompleted:
			n := node(ev.Path)
			n.State = NodeCompleted
			n.WaitingOn = ""
			resolve(ev.Path, ev.Attempt)
		case EventNodeDegraded:
			n := node(ev.Path)
			n.State = NodeDegraded
			n.WaitingOn = ""
			resolve(ev.Path, ev.Attempt)
			var meta struct {
				Cause string `json:"cause"`
			}
			if json.Unmarshal(ev.Data, &meta) == nil {
				n.Cause = meta.Cause
			}
		case EventNodeFailed:
			n := node(ev.Path)
			n.State = NodeFailed
			n.WaitingOn = ""
			resolve(ev.Path, ev.Attempt)
		}
	}
	lastAttempt := make(map[string]int)
	for _, r := range results {
		n := node(r.Path)
		n.Results++
		// The latest attempt's output is the observable result; attempts
		// sort by number, ties keep the later commit.
		if n.Results == 1 || r.Attempt >= lastAttempt[r.Path] {
			n.ResultBytes = int64(len(r.Output))
			lastAttempt[r.Path] = r.Attempt
		}
	}
	out := make([]NodeProjection, 0, len(nodes))
	for _, n := range nodes {
		for a := range pending[n.Path] {
			n.UnresolvedAttempts = append(n.UnresolvedAttempts, a)
		}
		sort.Ints(n.UnresolvedAttempts)
		out = append(out, *n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
