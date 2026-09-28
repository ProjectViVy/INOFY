package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	inofy "github.com/ProjectViVy/inofy"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/auth"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/httpapi"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
	"github.com/ProjectViVy/inofy/definitions"
)

type fixture struct {
	t       *testing.T
	svc     *definitions.Service
	h       http.Handler
	sid     string
	doAuth  *auth.Service
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	tok := filepath.Join(dir, "owner.token")
	if err := os.WriteFile(tok, []byte("owner-secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := auth.New(tok, auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.Open(context.Background(), dir+"/app.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	cat, err := inofy.NewCatalog([]inofy.NodeDescriptor{
		{TypeID: "inofy.value@1", ImplementationID: "impl-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := definitions.NewService(s)
	sid, err := a.Exchange("owner-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Dependencies{
		Auth:        a,
		Store:       s,
		Definitions: svc,
		Catalog:     cat,
	})
	return &fixture{t: t, svc: svc, h: h, sid: sid, doAuth: a}
}

func (f *fixture) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Cookie", "inofy_session="+f.sid)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func artifactJSON(title string) string {
	b, _ := json.Marshal(inofy.Artifact{
		Definition: inofy.Definition{
			SchemaVersion: inofy.SchemaVersionV1,
			Graph: inofy.Graph{
				Nodes:   []inofy.Node{{ID: "a", Kind: inofy.NodeKindCall, Type: "inofy.value@1"}},
				Edges:   []inofy.Edge{},
				Exits:   []string{"a"},
				Outputs: map[string]inofy.Binding{"r": {Source: "a", Pointer: "/answer"}},
			},
		},
		Presentation: inofy.Presentation{Title: title},
	})
	return string(b)
}

func TestAuthAndWorkflowHTTP(t *testing.T) {
	f := newFixture(t)

	t.Run("session exchange and revoke", func(t *testing.T) {
		rec := f.do("POST", "/api/v1/session", `{"token":"owner-secret-token"}`, nil)
		if rec.Code != 200 {
			t.Fatalf("session exchange: %d %s", rec.Code, rec.Body.String())
		}
		var c *http.Cookie
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == "inofy_session" {
				c = ck
			}
		}
		if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
			t.Fatalf("cookie attrs: %#v", c)
		}
		// Revoke a throwaway session — the fixture sid stays alive.
		tmp, _ := f.doAuth.Exchange("owner-secret-token")
		req := httptest.NewRequest("DELETE", "/api/v1/session", nil)
		req.Header.Set("Cookie", "inofy_session="+tmp)
		rec2 := httptest.NewRecorder()
		f.h.ServeHTTP(rec2, req)
		if rec2.Code != 200 {
			t.Fatalf("delete session: %d", rec2.Code)
		}
	})

	t.Run("unauthenticated draft GET denied", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/workflows/wf/draft", nil)
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anon draft get: %d", rec.Code)
		}
	})

	t.Run("create draft with If-None-Match then stale If-Match 412", func(t *testing.T) {
		rec := f.do("PUT", "/api/v1/workflows/wf/draft", artifactJSON("v1"),
			map[string]string{"If-None-Match": "*"})
		if rec.Code != 200 {
			t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
		}
		var env struct {
			ETag string `json:"etag"`
		}
		json.Unmarshal(rec.Body.Bytes(), &env)
		if env.ETag == "" {
			t.Fatal("no etag")
		}
		// Stale update fails.
		rec2 := f.do("PUT", "/api/v1/workflows/wf/draft", artifactJSON("v2"),
			map[string]string{"If-Match": "stale-tag"})
		if rec2.Code != http.StatusPreconditionFailed && rec2.Code != http.StatusConflict {
			t.Fatalf("stale etag got %d", rec2.Code)
		}
		// Correct update works.
		rec3 := f.do("PUT", "/api/v1/workflows/wf/draft", artifactJSON("v2"),
			map[string]string{"If-Match": env.ETag})
		if rec3.Code != 200 {
			t.Fatalf("cas update: %d", rec3.Code)
		}
	})

	t.Run("invalid definition surfaces diagnostics envelope", func(t *testing.T) {
		var a map[string]any
		json.Unmarshal([]byte(artifactJSON("v1")), &a)
		// Corrupt: remove exits so strict validation fails.
		a["definition"].(map[string]any)["graph"].(map[string]any)["exits"] = nil
		corrupted, _ := json.Marshal(a)
		rec := f.do("PUT", "/api/v1/workflows/wf2/draft", string(corrupted),
			map[string]string{"If-None-Match": "*"})
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("corrupt draft: %d %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Code        string            `json:"code"`
			Diagnostics []inofy.Diagnostic `json:"diagnostics"`
		}
		json.Unmarshal(rec.Body.Bytes(), &env)
		if env.Code == "" || len(env.Diagnostics) == 0 {
			t.Fatalf("no diagnostics: %s", rec.Body.String())
		}
		// A valid draft still validates cleanly on exact etag.
		d, err := f.svc.SaveDraft(context.Background(), "wf2", definitions.ETagAbsent, mustArtifact(t, "v1"))
		if err != nil {
			t.Fatal(err)
		}
		rec = f.do("POST", "/api/v1/workflows/wf2/validate", "",
			map[string]string{"If-Match": d.ETag})
		if rec.Code != 200 {
			t.Fatalf("validate: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("publish is idempotent under same draft", func(t *testing.T) {
		rec := f.do("PUT", "/api/v1/workflows/wf3/draft", artifactJSON("p"),
			map[string]string{"If-None-Match": "*"})
		var env struct {
			ETag string `json:"etag"`
		}
		json.Unmarshal(rec.Body.Bytes(), &env)
		r1 := f.do("POST", "/api/v1/workflows/wf3/publish", "",
			map[string]string{"If-Match": env.ETag})
		if r1.Code != 200 {
			t.Fatalf("publish: %d %s", r1.Code, r1.Body.String())
		}
		var rev1 struct {
			Revision uint64 `json:"revision"`
		}
		json.Unmarshal(r1.Body.Bytes(), &rev1)
		r2 := f.do("POST", "/api/v1/workflows/wf3/publish", "",
			map[string]string{"If-Match": env.ETag})
		var rev2 struct {
			Revision uint64 `json:"revision"`
		}
		json.Unmarshal(r2.Body.Bytes(), &rev2)
		if rev1.Revision != rev2.Revision {
			t.Fatalf("same draft allocated %d then %d", rev1.Revision, rev2.Revision)
		}
	})

	t.Run("capabilities and node-types", func(t *testing.T) {
		rec := f.do("GET", "/api/v1/capabilities", "", nil)
		if rec.Code != 200 {
			t.Fatalf("capabilities: %d", rec.Code)
		}
		rec = f.do("GET", "/api/v1/node-types", "", nil)
		if rec.Code != 200 {
			t.Fatalf("node-types: %d", rec.Code)
		}
		var body struct {
			Types []inofy.NodeDescriptor `json:"types"`
		}
		json.Unmarshal(rec.Body.Bytes(), &body)
		if len(body.Types) != 1 {
			t.Fatalf("types: %#v", body)
		}
	})

	t.Run("error envelope shape", func(t *testing.T) {
		rec := f.do("PUT", "/api/v1/workflows/wf4/draft", artifactJSON("x"),
			map[string]string{"If-Match": "nothing"})
		var env struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if rec.Code == 200 {
			t.Fatal("bad etag accepted")
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Code == "" {
			t.Fatalf("envelope: %s", rec.Body.String())
		}
	})
}

func mustArtifact(t *testing.T, title string) inofy.Artifact {
	a, _, err := inofy.DecodeArtifact([]byte(artifactJSON(title)))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func mustDecode(t *testing.T, data []byte) inofy.Artifact {
	a, _, err := inofy.DecodeArtifact(data)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
