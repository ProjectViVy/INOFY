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

	"github.com/ProjectViVy/inofy/apps/inofy/internal/auth"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/conns"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/httpapi"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
)

func connFixture(t *testing.T) (http.Handler, string, *conns.Store) {
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
	cs, err := conns.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	sid, err := a.Exchange("owner-secret-token")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.NewHandler(httpapi.Dependencies{Auth: a, Store: s, Conns: cs})
	return h, sid, cs
}

func doConn(h http.Handler, sid, method, path, body string) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != "" {
		rdr = bytes.NewReader([]byte(body))
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Cookie", "inofy_session="+sid)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestConnectionsAPI(t *testing.T) {
	h, sid, cs := connFixture(t)

	rec := doConn(h, sid, "PUT", "/api/v1/connections/sensetime",
		`{"kind":"openai","base_url":"https://token.sensenova.cn/v1","model":"sensenova-6.8-flash-lite","api_key":"sk-unit"}`)
	if rec.Code != 200 {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}

	rec = doConn(h, sid, "GET", "/api/v1/connections", "")
	var listed struct {
		Connections []struct {
			ID        string `json:"id"`
			Kind      string `json:"kind"`
			BaseURL   string `json:"base_url"`
			Model     string `json:"model"`
			Source    string `json:"source"`
			HasSecret bool   `json:"has_secret"`
		}
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &listed) != nil {
		t.Fatalf("list: %d %s", rec.Code, rec.Body)
	}
	if len(listed.Connections) != 1 {
		t.Fatalf("want 1 connection: %+v", listed.Connections)
	}
	c := listed.Connections[0]
	if c.ID != "sensetime" || c.Kind != "openai" || c.Model != "sensenova-6.8-flash-lite" ||
		!c.HasSecret || c.Source != "api" {
		t.Fatalf("view: %+v", c)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("sk-unit")) {
		t.Fatal("raw key leaked in list response")
	}

	// The live registry must reflect the write immediately.
	conn, ok := cs.Registry().Get("sensetime")
	if !ok || conn.BaseURL != "https://token.sensenova.cn/v1" {
		t.Fatalf("registry: %+v", conn)
	}
	if v, ok := cs.Resolve(conn.SecretEnv); !ok || v != "sk-unit" {
		t.Fatal("credential not resolvable")
	}

	// Update without api_key keeps the stored credential.
	rec = doConn(h, sid, "PUT", "/api/v1/connections/sensetime",
		`{"kind":"openai","base_url":"https://token.sensenova.cn/v1","model":"sensenova-6.8-flash-lite"}`)
	if rec.Code != 200 {
		t.Fatalf("re-put: %d %s", rec.Code, rec.Body)
	}
	conn, _ = cs.Registry().Get("sensetime")
	if v, ok := cs.Resolve(conn.SecretEnv); !ok || v != "sk-unit" {
		t.Fatal("credential lost on update without api_key")
	}

	// Validation: wrong kind rejected.
	rec = doConn(h, sid, "PUT", "/api/v1/connections/bad",
		`{"kind":"ftp","base_url":"x","model":"y"}`)
	if rec.Code != 400 {
		t.Fatalf("bad kind: %d", rec.Code)
	}

	rec = doConn(h, sid, "DELETE", "/api/v1/connections/sensetime", "")
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if _, ok := cs.Registry().Get("sensetime"); ok {
		t.Fatal("registry still holds deleted connection")
	}
}

func TestConnectionsUnauthenticated(t *testing.T) {
	h, _, _ := connFixture(t)
	rec := doConn(h, "bogus", "GET", "/api/v1/connections", "")
	if rec.Code == 200 {
		t.Fatal("connections list served without a session")
	}
}
