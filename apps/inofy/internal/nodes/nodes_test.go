package nodes_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/nodes"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
)

func callOp(typeID string, impl string, cfg, in json.RawMessage, opKey string) inofy.NodeCall {
	return inofy.NodeCall{
		Path:             "n",
		TypeID:           typeID,
		ImplementationID: impl,
		Config:           cfg,
		Input:            in,
		OperationKey:     opKey,
		Attempt:          1,
	}
}

func TestAppNodeCatalog(t *testing.T) {
	ctx := context.Background()

	t.Run("value node echoes config as output", func(t *testing.T) {
		ex := nodes.New(nodes.Dependencies{})
		rep, err := ex.Execute(ctx, callOp("inofy.value@1", "value", json.RawMessage(`{"value":{"answer":42}}`), nil, "k1"))
		if err != nil || rep.Wait != nil {
			t.Fatalf("value: %v %+v", err, rep)
		}
		var out map[string]any
		json.Unmarshal(rep.Output, &out)
		if out["answer"] != float64(42) {
			t.Fatalf("out: %s", rep.Output)
		}
	})

	t.Run("template node renders input", func(t *testing.T) {
		ex := nodes.New(nodes.Dependencies{})
		rep, err := ex.Execute(ctx, callOp("inofy.template@1", "template",
			json.RawMessage(`{"template":"hi {{.name}}","output_key":"greeting"}`),
			json.RawMessage(`{"name":"dev"}`), "k2"))
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		json.Unmarshal(rep.Output, &out)
		if out["greeting"] != "hi dev" {
			t.Fatalf("out: %s", rep.Output)
		}
	})

	t.Run("model node calls OpenAI-compatible endpoint via connection id", func(t *testing.T) {
		var calls atomic.Int64
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer sk-live" {
				t.Errorf("auth header: %q", got)
			}
			w.Header().Set("Content-Type", "application/json")
			// Non-streaming chat completion response.
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello back"},"index":0,"finish_reason":"stop"}]}`))
		}))
		defer fake.Close()

		ex := nodes.New(nodes.Dependencies{
			Connections: nodes.NewRegistry(map[string]nodes.Connection{
				"conn-1": {Kind: "openai", BaseURL: fake.URL, Model: "gpt-test", SecretEnv: "TEST_MODEL_KEY"},
			}),
			Secrets: func(name string) (string, bool) {
				if name == "TEST_MODEL_KEY" {
					return "sk-live", true
				}
				return "", false
			},
		})
		rep, err := ex.Execute(ctx, callOp("inofy.model.openai@1", "openai",
			json.RawMessage(`{"connection_id":"conn-1","messages":[{"role":"user","content":"hi"}]}`),
			nil, "k3"))
		if err != nil {
			t.Fatalf("model: %v", err)
		}
		var out struct {
			Content string `json:"content"`
		}
		json.Unmarshal(rep.Output, &out)
		if out.Content != "hello back" {
			t.Fatalf("content: %s", rep.Output)
		}
		if calls.Load() != 1 {
			t.Fatalf("provider calls: %d", calls.Load())
		}
	})

	t.Run("model node takes messages from bound input", func(t *testing.T) {
		var gotBody []byte
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBody, _ = io.ReadAll(r.Body)
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"from input"},"index":0,"finish_reason":"stop"}]}`))
		}))
		defer fake.Close()

		ex := nodes.New(nodes.Dependencies{
			Connections: nodes.NewRegistry(map[string]nodes.Connection{
				"c-in": {Kind: "openai", BaseURL: fake.URL, Model: "m", SecretEnv: "K"},
			}),
			Secrets: func(string) (string, bool) { return "sk-x", true },
		})
		rep, err := ex.Execute(ctx, callOp("inofy.model.openai@1", "model",
			json.RawMessage(`{"connection_id":"c-in"}`),
			json.RawMessage(`{"messages":[{"role":"user","content":"asked by caller"}]}`), "k9"))
		if err != nil {
			t.Fatalf("model input binding: %v", err)
		}
		var out struct{ Content string }
		if err := json.Unmarshal(rep.Output, &out); err != nil || out.Content != "from input" {
			t.Fatalf("out: %s", rep.Output)
		}
		if !bytes.Contains(gotBody, []byte("asked by caller")) {
			t.Fatalf("provider saw wrong messages: %s", gotBody)
		}
	})

	t.Run("missing secret fails before any provider effect", func(t *testing.T) {
		var called atomic.Bool
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called.Store(true)
			w.WriteHeader(500)
		}))
		defer fake.Close()
		ex := nodes.New(nodes.Dependencies{
			Connections: nodes.NewRegistry(map[string]nodes.Connection{
				"c2": {Kind: "openai", BaseURL: fake.URL, Model: "m", SecretEnv: "MISSING"},
			}),
			Secrets: func(string) (string, bool) { return "", false },
		})
		_, err := ex.Execute(ctx, callOp("inofy.model.openai@1", "openai",
			json.RawMessage(`{"connection_id":"c2","messages":[]}`), nil, "k4"))
		if err == nil || called.Load() {
			t.Fatal("provider called without credentials")
		}
		var ie *inofy.Error
		if !errors.As(err, &ie) || strings.Contains(err.Error(), "sk-") {
			t.Fatalf("leaky or wrong error: %v", err)
		}
	})

	t.Run("ambiguous provider timeout classifies outcome_unknown", func(t *testing.T) {
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond)
			w.Write([]byte(`{"choices":[{"message":{"content":"late"},"index":0}]}`))
		}))
		defer fake.Close()
		ex := nodes.New(nodes.Dependencies{
			Connections: nodes.NewRegistry(map[string]nodes.Connection{
				"c3": {Kind: "openai", BaseURL: fake.URL, Model: "m", SecretEnv: "K", Timeout: 30 * time.Millisecond},
			}),
			Secrets: func(string) (string, bool) { return "sk-x", true },
		})
		_, err := ex.Execute(ctx, callOp("inofy.model.openai@1", "openai",
			json.RawMessage(`{"connection_id":"c3","messages":[{"role":"user","content":"hi"}]}`), nil, "k5"))
		var ie *inofy.Error
		if !errors.As(err, &ie) || ie.Code != inofy.ErrOutcomeUnknown {
			t.Fatalf("timeout classified: %v", err)
		}
	})

	t.Run("wait node returns durable wait request", func(t *testing.T) {
		ex := nodes.New(nodes.Dependencies{})
		rep, err := ex.Execute(ctx, callOp("inofy.wait@1", "wait",
			json.RawMessage(`{"prompt":"approve?","request_id":"r9"}`), nil, "k6"))
		if err != nil || rep.Wait == nil {
			t.Fatalf("wait: %v %+v", err, rep)
		}
		if rep.Wait.RequestID == "" || rep.Wait.ContinuationRef == "" {
			t.Fatalf("wait identity: %+v", rep.Wait)
		}
	})

	t.Run("duplicate operation key replays committed output", func(t *testing.T) {
		dir := t.TempDir()
		st, err := storage.Open(ctx, dir+"/app.db")
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		// Seed a run and commit one node execution.
		runID, err := st.Admit(ctx, "p", "wf", 1,
			json.RawMessage(`{}`), json.RawMessage(`{}`), "ak")
		if err != nil {
			t.Fatal(err)
		}
		ref := inofy.ExecutionRef{RunID: runID}
		if _, err := st.Commit(ctx, ref, inofy.RunCommit{
			CommitID:   runID + "/0/c1",
			Transition: inofy.StateTransition{Expected: "", Target: inofy.RunAdmitted},
			Events:     []inofy.Event{{Kind: inofy.EventRunAdmitted}},
			Results: []inofy.ProtectedResult{{
				Path: "n", Attempt: 1,
				Output: json.RawMessage(`{"cached":true}`),
			}},
		}); err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		exec := nodes.New(nodes.Dependencies{Ledger: st})
		call := callOp("inofy.model.openai@1", "openai",
			json.RawMessage(`{"connection_id":"none","messages":[]}`), nil, "op-dup")
		call.Ref = ref
		call.Attempt = 1
		rep, err := exec.Execute(ctx, call)
		if err != nil {
			t.Fatalf("replay: %v", err)
		}
		var out map[string]any
		json.Unmarshal(rep.Output, &out)
		if out["cached"] != true {
			t.Fatalf("expected committed output, got %s", rep.Output)
		}
	})

	t.Run("catalog advertises pinned impl identities and replay classes", func(t *testing.T) {
		c := nodes.Catalog(nodes.Dependencies{})
		var have []string
		for _, id := range c.Types() {
			d, _ := c.Lookup(id)
			have = append(have, id+":"+d.ImplementationID)
		}
		got := strings.Join(have, ",")
		for _, want := range []string{"inofy.value@1", "inofy.template@1", "inofy.model.openai@1", "inofy.wait@1"} {
			if !strings.Contains(got, want) {
				t.Fatalf("missing %s in %s", want, got)
			}
		}
	})
}
