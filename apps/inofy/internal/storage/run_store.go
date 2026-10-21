package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	inofy "github.com/ProjectViVy/inofy"
)

// Commit is the single atomic visibility boundary: identity, events,
// node/run projections and checkpoint become visible in one SQL
// transaction (§11.2). Exact-redelivery returns the original receipt;
// same CommitID with a different body is idempotency_conflict; a lower
// writer epoch is stale_writer; a wrong Expected is revision_conflict.
func (s *Store) Commit(ctx context.Context, ref inofy.ExecutionRef, change inofy.RunCommit) (inofy.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return inofy.Receipt{}, err
	}
	body, err := commitBody(change)
	if err != nil {
		return inofy.Receipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return inofy.Receipt{}, err
	}
	defer tx.Rollback()

	var (
		status        string
		epoch         int64
		programID     sql.NullString
		hostBindingID sql.NullString
	)
	err = tx.QueryRowContext(ctx,
		`SELECT status, writer_epoch, program_id, host_binding_id FROM runs WHERE run_id = ?`,
		ref.RunID).Scan(&status, &epoch, &programID, &hostBindingID)
	notAdmitted := errors.Is(err, sql.ErrNoRows)
	if err != nil && !notAdmitted {
		return inofy.Receipt{}, err
	}
	// A queued/claimed admission placeholder row is not admitted: the
	// first Expected="" commit adopts it in this transaction (§11.2).
	if status == "queued" || status == "claimed" {
		notAdmitted = true
	}
	if !notAdmitted {
		if programID.String != ref.ProgramID {
			return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrCheckpointIncompatible, Path: ref.RunID,
				Message: "program identity mismatch"}
		}
		if hostBindingID.String != ref.HostBindingID {
			return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrCheckpointIncompatible, Path: ref.RunID,
				Message: "host binding identity mismatch"}
		}
	}
	if notAdmitted && change.Transition.Expected != "" {
		return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: ref.RunID,
			Message: "run is not admitted"}
	}
	if notAdmitted {
		if status == "queued" || status == "claimed" {
			if programID.String != ref.ProgramID {
				return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrCheckpointIncompatible, Path: ref.RunID,
					Message: "program identity mismatch"}
			}
			// Adopt the dispatcher's placeholder row.
			if _, err := tx.ExecContext(ctx,
				`UPDATE runs SET program_id = ?, host_binding_id = ?, status = ?, writer_epoch = ? WHERE run_id = ?`,
				ref.ProgramID, nullStr(ref.HostBindingID), string(change.Transition.Target), ref.Epoch, ref.RunID); err != nil {
				return inofy.Receipt{}, err
			}
		} else {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO runs (run_id, program_id, host_binding_id, status, writer_epoch)
				 VALUES (?, ?, ?, ?, ?)`,
				ref.RunID, ref.ProgramID, nullStr(ref.HostBindingID), string(change.Transition.Target), ref.Epoch); err != nil {
				return inofy.Receipt{}, err
			}
		}
		status = string(change.Transition.Expected)
		epoch = int64(ref.Epoch)
		programID = sql.NullString{String: ref.ProgramID, Valid: true}
		hostBindingID = sql.NullString{String: ref.HostBindingID, Valid: true}
	}
	// Idempotent redelivery: exact body → original receipt.
	var prevBody []byte
	var prevFirst, prevLast int64
	err = tx.QueryRowContext(ctx,
		`SELECT body, first_sequence, last_sequence FROM run_commits
		 WHERE run_id = ? AND commit_id = ?`, ref.RunID, change.CommitID).
		Scan(&prevBody, &prevFirst, &prevLast)
	switch {
	case err == nil && len(prevBody) == 0:
		return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrOutcomeUnknown, Path: ref.RunID,
			Message: "legacy commit receipt has no replay body"}
	case err == nil && bytes.Equal(prevBody, body):
		if err := tx.Commit(); err != nil {
			return inofy.Receipt{}, err
		}
		return inofy.Receipt{FirstSequence: uint64(prevFirst), LastSequence: uint64(prevLast)}, nil
	case err == nil:
		return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrIdempotencyConflict, Path: ref.RunID,
			Message: "commit id reused with a different body"}
	case !errors.Is(err, sql.ErrNoRows):
		return inofy.Receipt{}, err
	}
	// Epoch CAS: newer claims, older is stale.
	if int64(ref.Epoch) < epoch {
		return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrStaleWriter, Path: ref.RunID,
			Message: "writer epoch mismatch"}
	}
	if !notAdmitted && status != string(change.Transition.Expected) {
		return inofy.Receipt{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: ref.RunID,
			Message: fmt.Sprintf("run is %q, commit expects %q", status, change.Transition.Expected)}
	}

	// Assign ordered sequences from the durable event count.
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq),0) FROM run_events WHERE run_id = ?`, ref.RunID).
		Scan(&seq); err != nil {
		return inofy.Receipt{}, err
	}
	first := seq + 1
	usage := loadUsage(ctx, tx, ref.RunID)

	for _, ev := range change.Events {
		seq++
		var data []byte
		if len(ev.Data) > 0 {
			data = append([]byte(nil), ev.Data...)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO run_events (run_id, seq, kind, path, attempt, data)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			ref.RunID, seq, string(ev.Kind), nullStr(ev.Path), nullInt(ev.Attempt), data); err != nil {
			return inofy.Receipt{}, err
		}
		if err := applyEvent(ctx, tx, ref, ev, change.AdmittedInput, usage); err != nil {
			return inofy.Receipt{}, err
		}
	}
	for _, r := range change.Results {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_executions (run_id, node_path, attempt, operation_key, unresolved, protected_output)
			 VALUES (?, ?, ?, ?, 0, ?)
			 ON CONFLICT (run_id, node_path, attempt) DO UPDATE SET
			 unresolved = 0, protected_output = excluded.protected_output`,
			ref.RunID, r.Path, r.Attempt, ref.RunID+"/"+r.Path,
			r.Output); err != nil {
			return inofy.Receipt{}, err
		}
		usage.CompletedOutputBytes += int64(len(r.Output))
	}
	// Persist run projection: status, epoch, checkpoint, usage.
	set := `status = ?, writer_epoch = ?, usage_json = ?`
	args := []any{string(change.Transition.Target), max(epoch, int64(ref.Epoch)), mustJSON(usage)}
	if change.Checkpoint != nil {
		envJSON, err := marshalEnvelopeMeta(change.Checkpoint)
		if err != nil {
			return inofy.Receipt{}, err
		}
		set += `, checkpoint_json = ?, checkpoint_payload = ?`
		args = append(args, envJSON, change.Checkpoint.Payload)
	}
	if change.ResumeKey != "" {
		set += `, resume_key = ?, resume_answers_json = ?`
		args = append(args, change.ResumeKey, mustJSON(change.ResumeAnswers))
	}
	args = append(args, ref.RunID)
	if _, err := tx.ExecContext(ctx, `UPDATE runs SET `+set+`, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE run_id = ?`, args...); err != nil {
		return inofy.Receipt{}, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO run_commits (run_id, commit_id, body, first_sequence, last_sequence)
		 VALUES (?, ?, ?, ?, ?)`,
		ref.RunID, change.CommitID, body, first, seq); err != nil {
		return inofy.Receipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return inofy.Receipt{}, err
	}
	return inofy.Receipt{FirstSequence: uint64(first), LastSequence: uint64(seq)}, nil
}

// Load returns the committed recovery state — never synthesizes it.
func (s *Store) Load(ctx context.Context, runID string) (inofy.RecoveryState, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT program_id, host_binding_id, status, writer_epoch, input_json, limits_json,
		        waits_json, interrupts_json, gates_json, resume_key, resume_answers_json,
		        checkpoint_json, checkpoint_payload
		 FROM runs WHERE run_id = ?`, runID)
	var (
		progID, status         sql.NullString
		hostBindingID          sql.NullString
		epoch                  int64
		waits, ints, gates     sql.NullString
		limits, cpJSON         sql.NullString
		resumeKey, answersJSON sql.NullString
		inputRaw               []byte
		cpPayload              []byte
	)
	if err := row.Scan(&progID, &hostBindingID, &status, &epoch, &inputRaw, &limits,
		&waits, &ints, &gates, &resumeKey, &answersJSON, &cpJSON, &cpPayload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return inofy.RecoveryState{}, &inofy.Error{Code: inofy.ErrRevisionConflict, Path: runID,
				Message: "unknown run"}
		}
		return inofy.RecoveryState{}, err
	}
	st := inofy.RecoveryState{
		Ref:           inofy.ExecutionRef{RunID: runID, ProgramID: progID.String, HostBindingID: hostBindingID.String, Epoch: uint64(epoch)},
		Status:        inofy.RunStatus(status.String),
		AdmittedInput: append(json.RawMessage(nil), inputRaw...),
		ResumeKey:     resumeKey.String,
	}
	_ = json.Unmarshal([]byte(answersJSON.String), &st.ResumeAnswers)
	unmarshalInto(limits.String, &st.Limits)
	unmarshalInto(waits.String, &st.Waits)
	unmarshalInto(ints.String, &st.Interrupts)
	unmarshalInto(gates.String, &st.Gates)
	var usageJSON string
	if err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(usage_json, '') FROM runs WHERE run_id = ?`, runID).Scan(&usageJSON); err == nil {
		unmarshalInto(usageJSON, &st.Usage)
	}
	// An envelope restores whenever checkpoint_json committed — the
	// payload itself may legitimately serialize empty.
	if cpJSON.Valid && cpJSON.String != "" {
		var env inofy.CheckpointEnvelope
		if err := json.Unmarshal([]byte(cpJSON.String), &env); err != nil {
			return inofy.RecoveryState{}, err
		}
		env.Payload = cpPayload
		st.LatestCheckpoint = &env
	}
	ops, err := s.unresolved(ctx, runID)
	if err != nil {
		return inofy.RecoveryState{}, err
	}
	st.UnresolvedOperations = ops
	return st, nil
}

func (s *Store) unresolved(ctx context.Context, runID string) ([]inofy.OperationRef, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT operation_key, node_path, attempt FROM node_executions
		 WHERE run_id = ? AND unresolved = 1 ORDER BY operation_key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []inofy.OperationRef
	for rows.Next() {
		var op inofy.OperationRef
		if err := rows.Scan(&op.OperationKey, &op.Path, &op.Attempt); err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// applyEvent maintains the node/run projections inside the commit
// transaction: admission metadata, waiting waits/interrupts/gates,
// resume claim identity, and unresolved operation keys.
func applyEvent(ctx context.Context, tx *sql.Tx, ref inofy.ExecutionRef, ev inofy.Event, admittedInput json.RawMessage, usage *inofy.Usage) error {
	switch ev.Kind {
	case inofy.EventRunAdmitted:
		var meta struct {
			Limits inofy.Limits `json:"limits"`
		}
		if json.Unmarshal(ev.Data, &meta) == nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE runs SET input_json = ?, limits_json = ? WHERE run_id = ?`,
				[]byte(admittedInput), mustJSON(meta.Limits), ref.RunID); err != nil {
				return err
			}
		}
	case inofy.EventRunWaiting:
		var meta struct {
			Waits      []inofy.WaitRequest `json:"waits"`
			Interrupts map[string]string   `json:"interrupts"`
			Gates      []string            `json:"gates"`
		}
		if json.Unmarshal(ev.Data, &meta) == nil {
			if _, err := tx.ExecContext(ctx,
				`UPDATE runs SET waits_json = ?, interrupts_json = ?, gates_json = ? WHERE run_id = ?`,
				mustJSON(meta.Waits), mustJSON(meta.Interrupts), mustJSON(meta.Gates), ref.RunID); err != nil {
				return err
			}
		}
	case inofy.EventRunResumed:
		if _, err := tx.ExecContext(ctx,
			`UPDATE runs SET waits_json = NULL, interrupts_json = NULL, gates_json = NULL WHERE run_id = ?`,
			ref.RunID); err != nil {
			return err
		}
	}
	switch ev.Kind {
	case inofy.EventNodeAttempt:
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO node_executions (run_id, node_path, attempt, operation_key, unresolved)
			 VALUES (?, ?, ?, ?, 1)
			 ON CONFLICT (run_id, node_path, attempt) DO UPDATE SET unresolved = 1`,
			ref.RunID, ev.Path, ev.Attempt, ref.RunID+"/"+ev.Path); err != nil {
			return err
		}
	case inofy.EventNodeCompleted, inofy.EventNodeFailed, inofy.EventNodeDegraded, inofy.EventNodeWait:
		if _, err := tx.ExecContext(ctx,
			`UPDATE node_executions SET unresolved = 0
			 WHERE run_id = ? AND node_path = ? AND attempt = ?`,
			ref.RunID, ev.Path, ev.Attempt); err != nil {
			return err
		}
	}
	usage.Attempts++
	return nil
}

// --- helpers ---------------------------------------------------------

func commitBody(c inofy.RunCommit) ([]byte, error) {
	// Keep the body fields and key separate exactly as MemoryRunStore does;
	// canonicalize RawMessage values so object member order cannot change replay.
	body := map[string]any{
		"events":         c.Events,
		"results":        c.Results,
		"checkpoint":     c.Checkpoint,
		"transition":     c.Transition,
		"admitted_input": c.AdmittedInput,
		"resume_key":     c.ResumeKey,
		"resume_answers": c.ResumeAnswers,
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	canonical, err := inofy.NormalizeJSONValue(b)
	if err != nil {
		return nil, &inofy.Error{Code: inofy.ErrInvalidDefinition, Err: err,
			Message: "commit body is not canonicalizable"}
	}
	return canonical, nil
}

func loadUsage(ctx context.Context, tx *sql.Tx, runID string) *inofy.Usage {
	var raw sql.NullString
	_ = tx.QueryRowContext(ctx,
		`SELECT usage_json FROM runs WHERE run_id = ?`, runID).Scan(&raw)
	u := &inofy.Usage{}
	unmarshalInto(raw.String, u)
	return u
}

func marshalEnvelopeMeta(env *inofy.CheckpointEnvelope) (string, error) {
	meta := *env
	meta.Payload = nil
	b, err := json.Marshal(meta)
	return string(b), err
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func unmarshalInto(raw string, v any) {
	if raw == "" {
		return
	}
	_ = json.Unmarshal([]byte(raw), v)
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
