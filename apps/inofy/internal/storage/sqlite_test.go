package storage_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
	"github.com/ProjectViVy/inofy/definitions"
)

func admitCommit(runID string) inofy.RunCommit {
	data, _ := json.Marshal(map[string]any{
		"input_digest": "sha256:abc",
		"limits":       inofy.Limits{MaxAttemptsPerCall: 3},
	})
	return inofy.RunCommit{
		CommitID:   runID + "/0/admit/0/1",
		Events:     []inofy.Event{{Kind: inofy.EventRunAdmitted, Data: data}},
		Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted},
	}
}

func TestSQLiteAtomicCommitAndCAS(t *testing.T) {
	ctx := context.Background()
	open := func(t *testing.T) (*storage.Store, string) {
		dir := t.TempDir()
		s, err := storage.Open(ctx, dir+"/app.db")
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return s, dir + "/app.db"
	}
	ref := inofy.ExecutionRef{RunID: "r", ProgramDigest: "p1"}

	t.Run("fresh install, reopen keeps evidence", func(t *testing.T) {
		s, path := open(t)
		rcpt, err := s.Commit(ctx, ref, admitCommit("r"))
		if err != nil {
			t.Fatalf("admit: %v", err)
		}
		if rcpt.FirstSequence == 0 {
			t.Fatal("no sequence assigned")
		}
		s.Close()
		s2, err := storage.Open(ctx, path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		defer s2.Close()
		st, err := s2.Load(ctx, "r")
		if err != nil {
			t.Fatalf("load after reopen: %v", err)
		}
		if st.Status != inofy.RunAdmitted || st.InputDigest != "sha256:abc" {
			t.Fatalf("state lost: %#v", st)
		}
	})

	t.Run("same CommitID same body returns original receipt", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		c := admitCommit("r")
		r1, _ := s.Commit(ctx, ref, c)
		r2, err := s.Commit(ctx, ref, c)
		if err != nil {
			t.Fatalf("redelivery rejected: %v", err)
		}
		if r2 != r1 {
			t.Fatalf("duplicate got new receipt: %#v vs %#v", r1, r2)
		}
	})

	t.Run("same CommitID different body is idempotency_conflict", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		c := admitCommit("r")
		if _, err := s.Commit(ctx, ref, c); err != nil {
			t.Fatal(err)
		}
		c.Events = append(c.Events, inofy.Event{Kind: inofy.EventRunStarted})
		c.Transition = inofy.StateTransition{Expected: inofy.RunAdmitted, Target: inofy.RunRunning}
		if _, err := s.Commit(ctx, ref, c); err == nil {
			t.Fatal("changed body under same commit id accepted")
		}
	})

	t.Run("stale writer epoch rejected", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		if _, err := s.Commit(ctx, ref, admitCommit("r")); err != nil {
			t.Fatal(err)
		}
		start := inofy.RunCommit{
			CommitID:   "r/0/start/0/2",
			Events:     []inofy.Event{{Kind: inofy.EventRunStarted}},
			Transition: inofy.StateTransition{Expected: inofy.RunAdmitted, Target: inofy.RunRunning},
		}
		hi := ref
		hi.Epoch = 3
		if _, err := s.Commit(ctx, hi, start); err != nil {
			t.Fatalf("newer epoch claim: %v", err)
		}
		lo := ref
		lo.Epoch = 1
		bad := inofy.RunCommit{
			CommitID:   "r/0/x/0/3",
			Events:     []inofy.Event{{Kind: inofy.EventNodeStarted, Path: "a"}},
			Transition: inofy.StateTransition{Expected: inofy.RunRunning, Target: inofy.RunRunning},
		}
		if _, err := s.Commit(ctx, lo, bad); err == nil {
			t.Fatal("stale epoch writer committed")
		}
	})

	t.Run("failed commit before side effect leaves zero rows", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		bad := inofy.RunCommit{
			CommitID:   "x/0/a/0/1",
			Transition: inofy.StateTransition{Expected: inofy.RunWaiting, Target: inofy.RunRunning},
		}
		if _, err := s.Commit(ctx, ref, bad); err == nil {
			t.Fatal("non-admitted strict commit accepted")
		}
		if _, err := s.Load(ctx, "x"); err == nil {
			t.Fatal("phantom run persisted")
		}
	})

	t.Run("waiting projection and unresolved ops round-trip", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		if _, err := s.Commit(ctx, ref, admitCommit("r")); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   "r/0/s/0/2",
			Events:     []inofy.Event{{Kind: inofy.EventRunStarted}},
			Transition: inofy.StateTransition{Expected: inofy.RunAdmitted, Target: inofy.RunRunning},
		}); err != nil {
			t.Fatal(err)
		}
		waits, _ := json.Marshal(map[string]any{
			"waits":      []inofy.WaitRequest{{RequestID: "req-a", Kind: "human"}},
			"interrupts": map[string]string{"req-a": "int-1"},
			"gates":      []string{"g-1"},
		})
		env := &inofy.CheckpointEnvelope{Checksum: "cs", Payload: []byte("cp")}
		if _, err := s.Commit(ctx, ref, inofy.RunCommit{
			CommitID: "r/0/w/1/3",
			Events: []inofy.Event{
				{Kind: inofy.EventNodeAttempt, Path: "w1", Attempt: 1},
				{Kind: inofy.EventNodeWait, Path: "w1", Attempt: 1},
				{Kind: inofy.EventRunWaiting, Data: waits},
			},
			Transition: inofy.StateTransition{Expected: inofy.RunRunning, Target: inofy.RunWaiting},
			Checkpoint: env,
		}); err != nil {
			t.Fatal(err)
		}
		st, err := s.Load(ctx, "r")
		if err != nil {
			t.Fatal(err)
		}
		if st.Status != inofy.RunWaiting || len(st.Waits) != 1 || st.Interrupts["req-a"] != "int-1" {
			t.Fatalf("waiting projection lost: %#v", st)
		}
		if st.LatestCheckpoint == nil || string(st.LatestCheckpoint.Payload) != "cp" {
			t.Fatal("checkpoint not persisted")
		}
	})

	t.Run("concurrent draft CAS one winner", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		svc := definitions.NewService(s)
		a := artifactT()
		d1, err := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, a)
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		var wins, conflicts atomic.Int64
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := svc.SaveDraft(ctx, "wf", d1.ETag, a)
				if err != nil {
					conflicts.Add(1)
					return
				}
				wins.Add(1)
			}()
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("CAS winners=%d conflicts=%d", wins.Load(), conflicts.Load())
		}
	})

	t.Run("concurrent publish allocates each artifact once", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		svc := definitions.NewService(s)
		d, err := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifactT())
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		var revs sync.Map
		var wg sync.WaitGroup
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, err := svc.Publish(ctx, "wf", d.ETag, catalogT())
				if err == nil {
					revs.Store(r.Revision, true)
				}
			}()
		}
		wg.Wait()
		var n int
		revs.Range(func(_, _ any) bool { n++; return true })
		if n != 1 {
			t.Fatalf("distinct revisions allocated: %d", n)
		}
	})

	t.Run("archived workflow keeps revision, blocks publish and new runs", func(t *testing.T) {
		s, _ := open(t)
		defer s.Close()
		svc := definitions.NewService(s)
		d, _ := svc.SaveDraft(ctx, "wf", definitions.ETagAbsent, artifactT())
		r, err := svc.Publish(ctx, "wf", d.ETag, catalogT())
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Archive(ctx, "wf"); err != nil {
			t.Fatal(err)
		}
		if _, err := svc.GetRevision(ctx, "wf", r.Revision); err != nil {
			t.Fatalf("referenced revision deleted: %v", err)
		}
		d2, _ := s.GetDraft(ctx, "wf")
		if _, err := svc.Publish(ctx, "wf", d2.ETag, catalogT()); err == nil {
			t.Fatal("archived workflow published")
		}
	})
}

func artifactT() inofy.Artifact {
	return inofy.Artifact{
		Definition: inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes: []inofy.Node{
					{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1"},
				},
				Edges:   []inofy.Edge{},
				Exits:   []string{"a"},
				Outputs: map[string]inofy.Binding{"r": {Source: "a", Pointer: "/answer"}},
			},
		},
		Presentation: inofy.Presentation{Title: "t"},
	}
}

func catalogT() inofy.Catalog {
	c, err := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.value@1", ImplementationID: "impl-a"},
	})
	if err != nil {
		panic(err)
	}
	return c
}
