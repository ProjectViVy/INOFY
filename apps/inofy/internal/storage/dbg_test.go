package storage_test

import (
	"context"
	"encoding/json"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
)

func TestDebugQueuedCommit(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir()+"/a.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.Admit(ctx, "p", "wf", 1, "sha256:x", "k")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"input_digest": "sha256:x", "limits": inofy.Limits{}})
	_, err = s.Commit(ctx, inofy.ExecutionRef{RunID: id, ProgramDigest: "p"}, inofy.RunCommit{
		CommitID:   id + "/0/admit/0/1",
		Events:     []inofy.Event{{Kind: inofy.EventRunAdmitted, Data: data}},
		Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted},
	})
	if err != nil {
		t.Fatalf("adopt commit: %v", err)
	}
	st, _ := s.Load(ctx, id)
	if st.Status != inofy.RunAdmitted {
		t.Fatalf("status %q", st.Status)
	}
}
