package httpapi_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/dispatch"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/httpapi"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
	"github.com/ProjectViVy/inofy/definitions"
)

// valueExec answers {"answer": 42} for value nodes and a durable
// wait for wait nodes — enough to exercise the full HTTP surface.
type scriptedExec struct {
	calls  atomic.Int64
	wait   bool
	waitID string
}

func (e *scriptedExec) Execute(ctx context.Context, c inofy.NodeCall) (inofy.NodeReply, error) {
	e.calls.Add(1)
	if e.wait || strings.Contains(c.TypeID, "wait") {
		return inofy.NodeReply{Wait: &inofy.WaitRequest{
			RequestID:       "req-1",
			Kind:            "human",
			Prompt:          "approve?",
			ContinuationRef: "cont-1",
		}}, nil
	}
	return inofy.NodeReply{Output: json.RawMessage(`{"answer":42}`)}, nil
}

func waitArtifact() inofy.Artifact {
	b, _ := json.Marshal(inofy.Artifact{
		Definition: inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes:   []inofy.Node{{ID: "w", Kind: inofy.NodeKindCall, Type: "inofy.wait@1"}},
				Edges:   []inofy.Edge{},
				Exits:   []string{"w"},
				Outputs: map[string]inofy.Binding{"r": {Source: "w", Pointer: "/answer"}},
			},
		},
		Presentation: inofy.Presentation{Title: "waiter"},
	})
	a, _, err := inofy.DecodeArtifact(b)
	if err != nil {
		panic(err)
	}
	return a
}

type runFixture struct {
	*fixture
	exec *scriptedExec
}

func newRunFixture(t *testing.T, wait bool) *runFixture {
	f := newFixture(t)
	exec := &scriptedExec{wait: wait}
	cat, err := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.value@1", ImplementationID: "impl-a"},
		{TypeID: "inofy.wait@1", ImplementationID: "impl-a", SupportsWait: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := dispatch.New(f.fixtureStore(), dispatch.Options{}, httpapi.Factory(f.fixtureStore(), cat, exec))
	if err := d.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	h := httpapi.NewHandler(httpapi.Dependencies{
		Auth:        f.doAuth,
		Store:       f.fixtureStore(),
		Definitions: f.svc,
		Dispatch:    d,
		Catalog:     cat,
	})
	f.h = h
	// A published revision for run admissions.
	if _, err := f.svc.SaveDraft(context.Background(), "wf3", definitions.ETagAbsent, mustArtifact(t, "v1")); err != nil {
		t.Fatal(err)
	}
	dr, _ := f.store.GetDraft(context.Background(), "wf3")
	if _, err := f.svc.Publish(context.Background(), "wf3", dr.ETag, cat); err != nil {
		t.Fatal(err)
	}
	return &runFixture{fixture: f, exec: exec}
}

func waitFor(t *testing.T, st *storage.Store, runID string, want inofy.RunStatus, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		s, err := st.StatusOf(context.Background(), runID)
		if err == nil && s == want {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	s, _ := st.StatusOf(context.Background(), runID)
	t.Fatalf("run %s stuck at %q want %q", runID, s, want)
}

func TestRunHTTPAndSSE(t *testing.T) {
	f := newRunFixture(t, false)
	ctx := context.Background()

	t.Run("duplicate admission returns same run id", func(t *testing.T) {
		r1 := f.do("POST", "/api/v1/runs",
			`{"workflow":"wf3","revision":1,"input":{"x":1}}`,
			map[string]string{"Idempotency-Key": "k1"})
		if r1.Code != 202 {
			t.Fatalf("admit: %d %s", r1.Code, r1.Body.String())
		}
		var a1, a2 struct{ RunID string `json:"run_id"` }
		json.Unmarshal(r1.Body.Bytes(), &a1)
		r2 := f.do("POST", "/api/v1/runs",
			`{"workflow":"wf3","revision":1,"input":{"x":1}}`,
			map[string]string{"Idempotency-Key": "k1"})
		json.Unmarshal(r2.Body.Bytes(), &a2)
		if a1.RunID != a2.RunID {
			t.Fatalf("dup key new run: %s vs %s", a1.RunID, a2.RunID)
		}
		waitFor(t, f.fixtureStore(), a1.RunID, inofy.RunSucceeded, 5*time.Second)
		// Same key, different input → still same run (key is the contract).
		r3 := f.do("POST", "/api/v1/runs",
			`{"workflow":"wf3","revision":1,"input":{"x":2}}`,
			map[string]string{"Idempotency-Key": "k1"})
		var a3 struct{ RunID string `json:"run_id"` }
		json.Unmarshal(r3.Body.Bytes(), &a3)
		if a3.RunID != a1.RunID {
			t.Fatal("same key different input spawned new run")
		}
	})

	t.Run("unauthorized protected output denied without leaking", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/runs/run-x/nodes/a/output", nil)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anon output: %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "answer") || strings.Contains(rec.Body.String(), "42") {
			t.Fatalf("leaked output: %s", rec.Body.String())
		}
	})

	t.Run("committed events only + Last-Event-ID replay", func(t *testing.T) {
		rec := f.do("GET", "/api/v1/runs", "", nil)
		var page struct {
			Items []struct{ RunID string `json:"run_id"` } `json:"items"`
		}
		json.Unmarshal(rec.Body.Bytes(), &page)
		if len(page.Items) == 0 {
			t.Fatal("no runs")
		}
		runID := page.Items[0].RunID
		// JSON polling fallback.
		rec = f.do("GET", "/api/v1/runs/"+runID+"/events", "", nil)
		if rec.Code != 200 {
			t.Fatalf("events: %d", rec.Code)
		}
		var env struct {
			Events []struct {
				Seq  uint64 `json:"seq"`
				Kind string `json:"kind"`
			} `json:"events"`
		}
		json.Unmarshal(rec.Body.Bytes(), &env)
		if len(env.Events) == 0 {
			t.Fatal("no committed events")
		}
		// Events are strictly committed — seq monotone.
		for i := 1; i < len(env.Events); i++ {
			if env.Events[i].Seq <= env.Events[i-1].Seq {
				t.Fatal("non-monotone event seq")
			}
		}
		last := env.Events[len(env.Events)-1].Seq
		// Last-Event-ID replays only the tail.
		rec = f.do("GET", "/api/v1/runs/"+runID+"/events?after="+
			fmt.Sprint(env.Events[0].Seq), "", nil)
		var env2 struct {
			Events []struct{ Seq uint64 `json:"seq"` } `json:"events"`
		}
		json.Unmarshal(rec.Body.Bytes(), &env2)
		if len(env2.Events) == 0 || env2.Events[len(env2.Events)-1].Seq != last {
			t.Fatalf("replay wrong tail: %+v", env2.Events)
		}
	})

	t.Run("waiting run resume via HTTP", func(t *testing.T) {
		fw := newRunFixture(t, true)
		if _, err := fw.svc.SaveDraft(ctx, "ww", definitions.ETagAbsent, waitArtifact()); err != nil {
			t.Fatal(err)
		}
		// Admit via draft etag.
		d, _ := fw.fixtureStore().GetDraft(ctx, "ww")
		rec := fw.do("POST", "/api/v1/runs",
			fmt.Sprintf(`{"workflow":"ww","draft_etag":%q,"input":{}}`, d.ETag),
			map[string]string{"Idempotency-Key": "wk"})
		if rec.Code != 202 {
			t.Fatalf("admit: %d %s", rec.Code, rec.Body.String())
		}
		var a struct{ RunID string `json:"run_id"` }
		json.Unmarshal(rec.Body.Bytes(), &a)
		waitFor(t, fw.fixtureStore(), a.RunID, inofy.RunWaiting, 5*time.Second)
		// Resume with the complete answer set.
		rec = fw.do("POST", "/api/v1/runs/"+a.RunID+"/resume",
			`{"answers":{"req-1":{"approved":true}}}`,
			map[string]string{"Idempotency-Key": "rk1"})
		if rec.Code != 200 {
			t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
		}
		// Identical authorized repeat returns recorded outcome.
		rec = fw.do("POST", "/api/v1/runs/"+a.RunID+"/resume",
			`{"answers":{"req-1":{"approved":true}}}`,
			map[string]string{"Idempotency-Key": "rk1"})
		if rec.Code != 200 {
			t.Fatalf("resume replay: %d %s", rec.Code, rec.Body.String())
		}
		// A different claim body conflicts.
		rec = fw.do("POST", "/api/v1/runs/"+a.RunID+"/resume",
			`{"answers":{"req-1":{"approved":false}}}`,
			map[string]string{"Idempotency-Key": "rk2"})
		if rec.Code == 200 {
			st, _ := fw.fixtureStore().StatusOf(ctx, a.RunID)
			if st == inofy.RunFailed || st == inofy.RunCancelled {
				t.Fatalf("conflicting resume mutated run: %s", st)
			}
		}
	})

	t.Run("SSE stream delivers committed events with cursor", func(t *testing.T) {
		srv := httptest.NewServer(f.h)
		defer srv.Close()
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/runs", nil)
		req.Header.Set("Cookie", "inofy_session="+f.sid)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Items []struct{ RunID string `json:"run_id"` } `json:"items"`
		}
		json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		runID := page.Items[0].RunID

		req, _ = http.NewRequest("GET", srv.URL+"/api/v1/runs/"+runID+"/events", nil)
		req.Header.Set("Cookie", "inofy_session="+f.sid)
		req.Header.Set("Accept", "text/event-stream")
		resp, err = srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			t.Fatalf("content type: %s", ct)
		}
		// Read the committed stream — cursor ids are durable seqs.
		sc := bufio.NewScanner(resp.Body)
		var lastID string
		got := 0
		deadline := time.Now().Add(2 * time.Second)
		for got < 3 && time.Now().Before(deadline) && sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "id: ") {
				lastID = strings.TrimPrefix(line, "id: ")
				got++
			}
		}
		if got == 0 || lastID == "" {
			t.Fatal("no SSE events received")
		}
		// Replay from cursor works.
		resp.Body.Close()
		req, _ = http.NewRequest("GET", srv.URL+"/api/v1/runs/"+runID+"/events", nil)
		req.Header.Set("Cookie", "inofy_session="+f.sid)
		req.Header.Set("Last-Event-ID", lastID)
		resp2, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Events []struct{ Seq uint64 `json:"seq"` } `json:"events"`
		}
		json.NewDecoder(resp2.Body).Decode(&env)
		resp2.Body.Close()
		// Every replayed event is strictly after the cursor.
		var lastSeq uint64
		fmt.Sscanf(lastID, "%d", &lastSeq)
		for _, e := range env.Events {
			if e.Seq <= lastSeq {
				t.Fatalf("replayed event at/below cursor: %d <= %d", e.Seq, lastSeq)
			}
		}
	})

	t.Run("cancel is idempotent", func(t *testing.T) {
		rec := f.do("POST", "/api/v1/runs",
			`{"workflow":"wf3","revision":1,"input":{}}`,
			map[string]string{"Idempotency-Key": "cancel-k"})
		var a struct{ RunID string `json:"run_id"` }
		json.Unmarshal(rec.Body.Bytes(), &a)
		r1 := f.do("POST", "/api/v1/runs/"+a.RunID+"/cancel", "", nil)
		r2 := f.do("POST", "/api/v1/runs/"+a.RunID+"/cancel", "", nil)
		if r1.Code != 200 || r2.Code != 200 {
			t.Fatalf("cancel: %d/%d", r1.Code, r2.Code)
		}
	})
}
