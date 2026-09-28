package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"

	inofy "github.com/ProjectViVy/inofy"
)

// Admit persists an immutable run admission — snapshot binding, input
// digest and idempotency key — before any dispatch wakeup (§11.2,
// S08 plan). Same (principal, workflow, key) replays the same run ID.
// admissionFailure counts as a pending-admission slot so a queue
// bound can reject without creating a hidden row.
func (s *Store) Admit(ctx context.Context, principal, workflowID string, revision uint64, inputDigest, key string) (string, error) {
	var existing string
	err := s.db.QueryRowContext(ctx,
		`SELECT run_id FROM runs WHERE principal = ? AND workflow_id = ? AND admission_key = ?`,
		principal, workflowID, key).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	runID := newRunID()
	admitData, _ := json.Marshal(map[string]any{
		"input_digest": inputDigest,
		"limits":       inofy.Limits{},
	})
	// Status 'queued' is a pre-admitted placeholder row; the run's
	// first durable commit (EventRunAdmitted Expected="") transitions
	// it to admitted in the same transaction as the dispatcher's
	// claim, so a crash between queue insert and admit commit leaves
	// an inspectable row, never a hidden effect.
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO runs (run_id, workflow_id, principal, revision, program_digest, status, input_digest, limits_json, admission_key)
		 VALUES (?, ?, ?, ?, '', 'queued', ?, ?, ?)`,
		runID, workflowID, principal, revision, inputDigest, string(admitData), key); err != nil {
		return "", err
	}
	return runID, nil
}

// QueuedRuns returns run IDs in admission order for the bounded
// dispatcher backlog.
func (s *Store) QueuedRuns(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id FROM runs WHERE status = 'queued' ORDER BY created_at, run_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PendingCount counts rows awaiting dispatch (queued + admitted).
func (s *Store) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE status IN ('queued','admitted','claimed')`).Scan(&n)
	return n, err
}

// ActiveCount counts rows holding a dispatch slot: 'running' plus
// 'claimed' rows mid-claim (a claimed row is already holding a slot
// even before its first commit lands).
func (s *Store) ActiveCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs WHERE status IN ('running','claimed')`).Scan(&n)
	return n, err
}

// Dispatchable returns rows that may be woken: 'queued' placeholders
// and durable 'admitted' runs that never started.
func (s *Store) Dispatchable(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id FROM runs WHERE status IN ('queued','admitted','claimed')
		 ORDER BY created_at, run_id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Claim atomically takes a queued/admitted row into 'claimed': the
// winner executes; concurrent dispatchers lose with zero rows. A
// 'claimed' row left by a dead process is reset to 'queued' at Start
// — it provably made no commit (any commit would have moved status).
func (s *Store) Claim(ctx context.Context, runID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'claimed', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE run_id = ? AND status IN ('queued','admitted')`, runID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// RequeueClaims resets dead claims to queued at Start.
func (s *Store) RequeueClaims(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'queued' WHERE status = 'claimed'`)
	return err
}

// StatusOf exposes the durable run status for reconciliation.
func (s *Store) StatusOf(ctx context.Context, runID string) (inofy.RunStatus, error) {
	var st string
	err := s.db.QueryRowContext(ctx, `SELECT status FROM runs WHERE run_id = ?`, runID).Scan(&st)
	if errors.Is(err, sql.ErrNoRows) {
		return "", &inofy.Error{Code: inofy.ErrRevisionConflict, Path: runID, Message: "unknown run"}
	}
	return inofy.RunStatus(st), err
}

// MarkRecoveryRequired transitions a recovered running row to
// recovery_required — never a replay (§8.5, S08 plan).
func (s *Store) MarkRecoveryRequired(ctx context.Context, runID string, epoch uint64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET status = 'recovery_required', updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')
		 WHERE run_id = ? AND status = 'running'`, runID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return &inofy.Error{Code: inofy.ErrRevisionConflict, Path: runID,
			Message: "run is not running"}
	}
	return nil
}

// RunningRuns lists rows the previous process left running.
func (s *Store) RunningRuns(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id FROM runs WHERE status = 'running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func newRunID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return "run-" + hex.EncodeToString(b)
}
