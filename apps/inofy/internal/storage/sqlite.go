// Package storage is the App's sole state authority (architecture
// §11.2): one SQLite file, embedded migrations, and a single
// transaction per RunStore.Commit making identity, events,
// projections and checkpoint visible together. Pure-Go driver —
// CGO_ENABLED=0.
package storage

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"sync/atomic"

	appmigrations "github.com/ProjectViVy/inofy/apps/inofy/migrations"
	_ "modernc.org/sqlite"
)

var etagCounter atomic.Uint64

func nextETag() uint64 { return etagCounter.Add(1) }

// Store owns the SQLite handle and explicit lifecycle.
type Store struct {
	db *sql.DB
}

// Open applies pending migrations then returns the store; it is the
// single state-directory owner.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // single-writer serialization at the driver level
	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	files, err := appmigrations.FS.ReadDir(".")
	if err != nil {
		files, err = appmigrations.FS.ReadDir(".")
		if err != nil {
			return err
		}
	}
	var versions []string
	for _, f := range files {
		versions = append(versions, f.Name())
	}
	sort.Strings(versions)
	var applied int
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version),0) FROM schema_migrations").Scan(&applied); err != nil {
		// Bootstrap on a fresh file.
	}
	for _, name := range versions {
		var v int
		if _, err := fmt.Sscanf(name, "%d_", &v); err != nil {
			continue
		}
		if v <= applied {
			continue
		}
		body, err := appmigrations.FS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version) VALUES(?)", v); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
