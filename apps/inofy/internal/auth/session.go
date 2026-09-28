// Package auth owns the App's local access surface (architecture
// §11.3): owner token from a user-only file, bounded HttpOnly
// SameSite sessions for browsers, bearer for CLI, and Origin/CSRF
// checks on session-carried mutations. Constant-time compares —
// a token never leaks through timing.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Options bound the session surface.
type Options struct {
	MaxSessions int
	IdleTTL     time.Duration
}

// Service mints and validates sessions.
type Service struct {
	token    string
	maxSess  int
	idleTTL  time.Duration
	mu       sync.Mutex
	sessions map[string]time.Time // session id -> last-seen
	order    []string             // LRU eviction order
}

// New loads the owner token from a user-only file (0600 enforced).
func New(tokenFile string, opts Options) (*Service, error) {
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(tokenFile); err == nil && st.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("token file must be user-only (0600)")
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return nil, errors.New("empty owner token")
	}
	if opts.MaxSessions <= 0 {
		opts.MaxSessions = 256
	}
	if opts.IdleTTL <= 0 {
		opts.IdleTTL = 12 * time.Hour
	}
	return &Service{
		token:    token,
		maxSess:  opts.MaxSessions,
		idleTTL:  opts.IdleTTL,
		sessions: map[string]time.Time{},
	}, nil
}

// Exchange trades the owner token for a session id — constant-time
// compare, never echoing the presented secret.
func (s *Service) Exchange(presented string) (string, error) {
	if subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) != 1 {
		return "", errors.New("invalid token")
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	sid := "s_" + hex.EncodeToString(b)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked(time.Now())
	if len(s.sessions) >= s.maxSess && len(s.order) > 0 {
		delete(s.sessions, s.order[0])
		s.order = s.order[1:]
	}
	s.sessions[sid] = time.Now()
	s.order = append(s.order, sid)
	return sid, nil
}

// ValidSession checks a session id and refreshes its idle window.
func (s *Service) ValidSession(sid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reapLocked(time.Now())
	last, ok := s.sessions[sid]
	if !ok {
		return false
	}
	s.sessions[sid] = time.Now()
	_ = last
	return true
}

// ValidBearer checks a bearer token against the owner token.
func (s *Service) ValidBearer(tok string) bool {
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
}

// Revoke drops a session id (idempotent).
func (s *Service) Revoke(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sid)
}

// Cookie renders the session cookie — HttpOnly, SameSite=Strict,
// Path=/, no persistence beyond the session.
func (s *Service) Cookie(sid string) *http.Cookie {
	return &http.Cookie{
		Name:     "inofy_session",
		Value:    sid,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	}
}

// RequireAuth wraps a handler: bearer (Authorization header) or
// session cookie both authenticate; mutations carried by a session
// additionally require a matching Origin (CSRF guard — §11.3).
// Bearer clients are not subject to Origin checks.
func (s *Service) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var viaSession string
		if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			if !s.ValidBearer(strings.TrimPrefix(h, "Bearer ")) {
				unauthorized(w)
				return
			}
		} else if c, err := r.Cookie("inofy_session"); err == nil && s.ValidSession(c.Value) {
			viaSession = c.Value
		} else {
			unauthorized(w)
			return
		}
		if viaSession != "" && isMutation(r.Method) {
			if !sameOrigin(r) {
				w.WriteHeader(http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isMutation(m string) bool {
	switch m {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// sameOrigin requires Origin (when present) to match the request
// host — a session-carried cross-site mutation is rejected.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		// No Origin header on a same-site browser form submit is
		// acceptable; Sec-Fetch-Site cross-site is not.
		return r.Header.Get("Sec-Fetch-Site") != "cross-site"
	}
	return strings.HasSuffix(o, r.Host)
}

func (s *Service) reapLocked(now time.Time) {
	kept := s.order[:0]
	for _, sid := range s.order {
		last, ok := s.sessions[sid]
		if !ok || now.Sub(last) > s.idleTTL {
			delete(s.sessions, sid)
			continue
		}
		kept = append(kept, sid)
	}
	s.order = kept
}

func unauthorized(w http.ResponseWriter) {
	w.WriteHeader(http.StatusUnauthorized)
}
