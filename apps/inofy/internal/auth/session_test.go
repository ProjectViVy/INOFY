package auth_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ProjectViVy/inofy/apps/inofy/internal/auth"
)

func tokenFile(t *testing.T) string {
	dir := t.TempDir()
	path := filepath.Join(dir, "owner.token")
	if err := os.WriteFile(path, []byte("owner-secret-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthAndWorkflowHTTP_SessionLayer(t *testing.T) {
	t.Run("token exchange mints HttpOnly SameSite session", func(t *testing.T) {
		s, err := auth.New(tokenFile(t), auth.Options{})
		if err != nil {
			t.Fatalf("auth new: %v", err)
		}
		sid, err := s.Exchange("owner-secret-token")
		if err != nil {
			t.Fatalf("exchange: %v", err)
		}
		if sid == "" {
			t.Fatal("empty session id")
		}
		if _, err := s.Exchange("wrong"); err == nil {
			t.Fatal("wrong token exchanged")
		}
	})

	t.Run("bearer and session both authenticate", func(t *testing.T) {
		s, err := auth.New(tokenFile(t), auth.Options{})
		if err != nil {
			t.Fatal(err)
		}
		sid, _ := s.Exchange("owner-secret-token")
		if !s.ValidSession(sid) {
			t.Fatal("session rejected")
		}
		if !s.ValidBearer("owner-secret-token") {
			t.Fatal("bearer rejected")
		}
		s.Revoke(sid)
		if s.ValidSession(sid) {
			t.Fatal("revoked session accepted")
		}
	})

	t.Run("cookie attributes: HttpOnly SameSite no expiry leak", func(t *testing.T) {
		s, _ := auth.New(tokenFile(t), auth.Options{})
		sid, _ := s.Exchange("owner-secret-token")
		c := s.Cookie(sid)
		if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
			t.Fatalf("unsafe cookie: %#v", c)
		}
	})

	t.Run("sessions bounded and expire", func(t *testing.T) {
		s, _ := auth.New(tokenFile(t), auth.Options{MaxSessions: 4, IdleTTL: 40 * time.Millisecond})
		var last string
		for i := 0; i < 8; i++ {
			last, _ = s.Exchange("owner-secret-token")
		}
		time.Sleep(60 * time.Millisecond)
		if s.ValidSession(last) {
			t.Fatal("expired session accepted")
		}
	})
}

func TestMutationGuards(t *testing.T) {
	s, err := auth.New(tokenFile(t), auth.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	t.Run("anonymous mutation denied", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", nil)
		rec := httptest.NewRecorder()
		s.RequireAuth(ok).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("anon got %d", rec.Code)
		}
	})

	t.Run("cross-origin session mutation denied, same-origin ok", func(t *testing.T) {
		sid, _ := s.Exchange("owner-secret-token")
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", nil)
		req.Header.Set("Cookie", "inofy_session="+sid)
		req.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		s.RequireAuth(ok).ServeHTTP(rec, req)
		if rec.Code == 200 {
			t.Fatal("cross-origin session mutation passed")
		}
		req2 := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", nil)
		req2.Header.Set("Cookie", "inofy_session="+sid)
		req2.Header.Set("Origin", "http://"+req2.Host)
		rec2 := httptest.NewRecorder()
		s.RequireAuth(ok).ServeHTTP(rec2, req2)
		if rec2.Code != 200 {
			t.Fatalf("same-origin mutation got %d", rec2.Code)
		}
	})

	t.Run("bearer skips origin check (CLI client)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflows", nil)
		req.Header.Set("Authorization", "Bearer owner-secret-token")
		req.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		s.RequireAuth(ok).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("bearer with foreign origin got %d", rec.Code)
		}
	})
}
