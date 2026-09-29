package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	inofy "github.com/ProjectViVy/inofy"
)

// RunRow is the committed inspection view of a run (§11.4 GET /runs).
type RunRow struct {
	RunID      string          `json:"run_id"`
	WorkflowID string          `json:"workflow_id"`
	Revision   uint64          `json:"revision"`
	Status     string          `json:"status"`
	Epoch      uint64          `json:"writer_epoch"`
	Generation uint64          `json:"generation"`
	CreatedAt  string          `json:"created_at"`
	UpdatedAt  string          `json:"updated_at"`
}

// ListRuns returns a stable cursor page over run rows.
func (s *Store) ListRuns(ctx context.Context, cursor string, limit int) ([]RunRow, string, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, workflow_id, revision, status, writer_epoch, generation, created_at, updated_at
		 FROM runs WHERE run_id > ? ORDER BY run_id LIMIT ?`, cursor, limit+1)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []RunRow
	for rows.Next() {
		var r RunRow
		var rev sql.NullInt64
		if err := rows.Scan(&r.RunID, &r.WorkflowID, &rev, &r.Status, &r.Epoch, &r.Generation, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, "", err
		}
		if rev.Valid {
			r.Revision = uint64(rev.Int64)
		}
		out = append(out, r)
	}
	next := ""
	if len(out) > limit {
		next = out[limit-1].RunID
		out = out[:limit]
	}
	return out, next, rows.Err()
}

// EventRow is a committed, redacted run event with its durable seq.
type EventRow struct {
	Seq     uint64          `json:"seq"`
	Kind    string          `json:"kind"`
	Path    string          `json:"path,omitempty"`
	Attempt int             `json:"attempt,omitempty"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// ListEvents returns committed events after `after` (SSE
// Last-Event-ID cursor = committed seq), bounded by limit.
func (s *Store) ListEvents(ctx context.Context, runID string, after uint64, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq, kind, COALESCE(path,''), COALESCE(attempt,0), data
		 FROM run_events WHERE run_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		runID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventRow
	for rows.Next() {
		var e EventRow
		var data []byte
		if err := rows.Scan(&e.Seq, &e.Kind, &e.Path, &e.Attempt, &data); err != nil {
			return nil, err
		}
		e.Data = data
		out = append(out, e)
	}
	return out, rows.Err()
}

// ProtectedOutput returns the committed protected output for one
// node execution — inspection is authorized by the caller's session,
// never by knowledge of the operation key.
func (s *Store) ProtectedOutput(ctx context.Context, runID, nodePath string) (json.RawMessage, error) {
	var out []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT protected_output FROM node_executions
		 WHERE run_id = ? AND node_path = ? AND protected_output IS NOT NULL
		 ORDER BY attempt DESC LIMIT 1`, runID, nodePath).Scan(&out)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: nodePath,
			Message: "no committed output"}
	}
	return json.RawMessage(out), err
}

// CommittedOutput returns the committed output for one exact
// (run, node path, attempt) — the App executor's effect ledger
// (§11.2 node_executions doubles as it). The operation key is the
// caller's own claim: a repeat call under the same key replays the
// committed row instead of repeating the effect.
func (s *Store) CommittedOutput(ctx context.Context, runID, path string, attempt int) (json.RawMessage, bool, error) {
	var out []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT protected_output FROM node_executions
		 WHERE run_id = ? AND node_path = ? AND attempt = ? AND unresolved = 0`,
		runID, path, attempt).Scan(&out)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return json.RawMessage(out), true, nil
}

// CancelRun durably cancels a run that holds no live executor
// (queued/admitted placeholders and quiescent waiting rows). A
// running row is cancelled through its in-flight context instead —
// returns false so the dispatcher falls back to that path.
func (s *Store) CancelRun(ctx context.Context, runID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'cancelled', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE run_id = ? AND status IN ('queued','admitted','claimed','waiting','recovery_required')`,
		runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
