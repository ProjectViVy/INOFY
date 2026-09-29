// Package conns persists approved node connections and their
// credentials inside the App's owner-only state directory.
//
// connections.json holds {id: Connection} (kind/base_url/model/
// secret_env) — never raw keys. secrets.json holds
// {env_name: raw key} with 0600 mode; a key typed through the API
// is reachable only through its env-name indirection, same as a
// -connections file entry pointing at a real environment variable.
package conns

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ProjectViVy/inofy/apps/inofy/internal/nodes"
)

const connsFile = "connections.json"
const secretsFile = "secrets.json"

// View is the API-facing summary of one connection — the raw key
// is never returned, only whether one is stored.
type View struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	HasSecret bool   `json:"has_secret"`
	Source    string `json:"source"` // "file" (startup) or "api" (persisted)
}

// Store couples the shared nodes.Registry with JSON persistence.
type Store struct {
	reg     *nodes.Registry
	dir     string
	file    map[string]nodes.Connection // startup-file seed (read-only)
	persist map[string]nodes.Connection // api-written entries
	secrets map[string]string           // env name -> raw key

	mu sync.Mutex // serializes file writes
}

// Open loads persisted state and returns a store seeded with the
// startup file's connections (source "file"). A persisted entry
// with the same id overrides the seeded one.
func Open(dir string, seed map[string]nodes.Connection) (*Store, error) {
	s := &Store{
		dir:     dir,
		file:    map[string]nodes.Connection{},
		persist: map[string]nodes.Connection{},
		secrets: map[string]string{},
	}
	for id, c := range seed {
		s.file[id] = c
	}
	if b, err := os.ReadFile(filepath.Join(dir, connsFile)); err == nil {
		if err := json.Unmarshal(b, &s.persist); err != nil {
			return nil, fmt.Errorf("parse %s: %w", connsFile, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if b, err := os.ReadFile(filepath.Join(dir, secretsFile)); err == nil {
		if err := json.Unmarshal(b, &s.secrets); err != nil {
			return nil, fmt.Errorf("parse %s: %w", secretsFile, err)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	m := map[string]nodes.Connection{}
	for id, c := range s.file {
		m[id] = c
	}
	for id, c := range s.persist {
		m[id] = c
	}
	s.reg = nodes.NewRegistry(m)
	return s, nil
}

// Registry returns the shared live view the executor consults.
func (s *Store) Registry() *nodes.Registry { return s.reg }

// Resolve looks up a secret by env name — persisted keys first,
// then the process environment. Returns (value, found).
func (s *Store) Resolve(env string) (string, bool) {
	s.mu.Lock()
	v, ok := s.secrets[env]
	s.mu.Unlock()
	if ok {
		return v, true
	}
	return os.LookupEnv(env)
}

// Upsert registers or replaces a connection. A non-empty apiKey is
// stored under the connection's secret_env name and never written
// to connections.json. The connection is rejected if it would
// point at a provider shape the catalog cannot serve.
func (s *Store) Upsert(id string, c nodes.Connection, apiKey string) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/\\") {
		return fmt.Errorf("invalid connection id")
	}
	if c.Kind != "openai" {
		return fmt.Errorf("unsupported connection kind %q", c.Kind)
	}
	if c.BaseURL == "" || c.Model == "" {
		return fmt.Errorf("base_url and model are required")
	}
	if c.SecretEnv == "" {
		c.SecretEnv = "INOFY_CONN_" + strings.ToUpper(strings.Map(func(r rune) rune {
			if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
				return r
			}
			return '_'
		}, id))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if apiKey != "" {
		s.secrets[c.SecretEnv] = apiKey
	}
	s.persist[id] = c
	s.reg.Set(id, c)
	return s.saveLocked()
}

// Delete removes a persisted connection. A startup-file connection
// of the same id re-emerges on next boot (the file is read-only).
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.persist, id)
	s.reg.Delete(id)
	return s.saveLocked()
}

// List returns every live connection with its provenance and
// whether a credential resolves — never the key itself.
func (s *Store) List() []View {
	s.mu.Lock()
	defer s.mu.Unlock()
	all := s.reg.List()
	out := make([]View, 0, len(all))
	for id, c := range all {
		src := "api"
		if _, isFile := s.file[id]; isFile {
			if _, overridden := s.persist[id]; !overridden {
				src = "file"
			}
		}
		_, has := s.secrets[c.SecretEnv]
		if !has {
			_, has = os.LookupEnv(c.SecretEnv)
		}
		out = append(out, View{
			ID: id, Kind: c.Kind, BaseURL: c.BaseURL, Model: c.Model,
			HasSecret: has, Source: src,
		})
	}
	return out
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	write := func(name string, v any) error {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		tmp := filepath.Join(s.dir, name+".tmp")
		if err := os.WriteFile(tmp, b, 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, filepath.Join(s.dir, name))
	}
	if err := write(connsFile, s.persist); err != nil {
		return err
	}
	return write(secretsFile, s.secrets)
}
