package conns_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ProjectViVy/inofy/apps/inofy/internal/conns"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/nodes"
)

func TestStorePersistReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := conns.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Upsert("st", nodes.Connection{
		Kind: "openai", BaseURL: "https://token.sensenova.cn/v1",
		Model: "sensenova-6.8-flash-lite",
	}, "sk-live")
	if err != nil {
		t.Fatal(err)
	}

	// Files on disk: connections.json has no raw key; secrets.json is 0600.
	cb, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(cb) == "" || strings.Contains(string(cb), "sk-live") {
		t.Fatalf("connections.json leaked key or empty: %s", cb)
	}
	fi, err := os.Stat(filepath.Join(dir, "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Fatalf("secrets.json mode %o", fi.Mode())
	}

	// Reopen: same view, credential resolves.
	s2, err := conns.Open(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn, ok := s2.Registry().Get("st")
	if !ok || conn.Model != "sensenova-6.8-flash-lite" {
		t.Fatalf("reopened: %+v", conn)
	}
	if v, ok := s2.Resolve(conn.SecretEnv); !ok || v != "sk-live" {
		t.Fatal("secret not persisted")
	}
	if v, ok := s2.Resolve("PATH"); !ok || v == "" {
		t.Fatal("env fallback broken")
	}
}

func TestStoreFileSeed(t *testing.T) {
	dir := t.TempDir()
	s, err := conns.Open(dir, map[string]nodes.Connection{
		"seeded": {Kind: "openai", BaseURL: "https://x", Model: "m", SecretEnv: "NO_SUCH_ENV_XYZ"},
	})
	if err != nil {
		t.Fatal(err)
	}
	views := s.List()
	if len(views) != 1 || views[0].Source != "file" || views[0].HasSecret {
		t.Fatalf("views: %+v", views)
	}
	// Persisted override of the same id wins and flips source.
	if err := s.Upsert("seeded", nodes.Connection{Kind: "openai", BaseURL: "https://y", Model: "m2"}, "k"); err != nil {
		t.Fatal(err)
	}
	views = s.List()
	if len(views) != 1 || views[0].Source != "api" || views[0].BaseURL != "https://y" || !views[0].HasSecret {
		t.Fatalf("overridden views: %+v", views)
	}
}
