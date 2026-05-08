package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RunRecord is the persisted shape of a run (Spec §3.7 / §4.3.7).
type RunRecord struct {
	RunID                   string
	TaskID                  string
	StationRevisionID       string
	RetryIndex              int
	WorkingRoot             string
	State                   string
	Canonicality            string
	ProcessingFingerprint   string
	FailureReason           string
	CreatedAt               time.Time
	PreparedAt              sql.NullTime
	DispatchedAt            sql.NullTime
	StartedAt               sql.NullTime
	TerminalAt              sql.NullTime
	CancellationRequestedAt sql.NullTime
	ReconciliationStartedAt sql.NullTime
}

// RunRepo persists runs.
type RunRepo struct {
	q       querier
	dialect dialect
}

// Insert creates a new run. Returns ErrConflict on (task_id, retry_index) or
// working_root reuse, or on missing FK targets.
func (r *RunRepo) Insert(ctx context.Context, rec *RunRecord) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = nowUTC()
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO runs (
			run_id, task_id, station_revision_id, retry_index, working_root,
			state, canonicality, processing_fingerprint, failure_reason,
			created_at, prepared_at, dispatched_at, started_at, terminal_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RunID, rec.TaskID, rec.StationRevisionID, rec.RetryIndex, rec.WorkingRoot,
		rec.State, rec.Canonicality,
		nullStr(rec.ProcessingFingerprint), nullStr(rec.FailureReason),
		rec.CreatedAt.UTC(),
		nullTime(rec.PreparedAt), nullTime(rec.DispatchedAt),
		nullTime(rec.StartedAt), nullTime(rec.TerminalAt),
	)
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return fmt.Errorf("%w: run uniqueness violation", ErrConflict)
		}
		if r.dialect.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: run references missing task or station_revision", ErrConflict)
		}
		return err
	}
	return nil
}

// Get returns the run by id, or ErrNotFound.
func (r *RunRepo) Get(ctx context.Context, runID string) (*RunRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT run_id, task_id, station_revision_id, retry_index, working_root,
		       state, canonicality,
		       COALESCE(processing_fingerprint, ''),
		       COALESCE(failure_reason, ''),
		       created_at, prepared_at, dispatched_at, started_at, terminal_at,
		       cancellation_requested_at, reconciliation_started_at
		FROM runs WHERE run_id = ?`, runID)

	var rec RunRecord
	if err := row.Scan(
		&rec.RunID, &rec.TaskID, &rec.StationRevisionID, &rec.RetryIndex, &rec.WorkingRoot,
		&rec.State, &rec.Canonicality,
		&rec.ProcessingFingerprint, &rec.FailureReason,
		&rec.CreatedAt, &rec.PreparedAt, &rec.DispatchedAt, &rec.StartedAt, &rec.TerminalAt,
		&rec.CancellationRequestedAt, &rec.ReconciliationStartedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return &rec, nil
}

// GetByTaskRetry returns the run identified by the task-scoped composite
// (task_id, retry_index). This is the canonical run identity per Spec
// §3.7. Returns ErrNotFound when no matching row exists.
func (r *RunRepo) GetByTaskRetry(ctx context.Context, taskID string, retryIndex int) (*RunRecord, error) {
	var runID string
	if err := r.q.QueryRowContext(ctx,
		`SELECT run_id FROM runs WHERE task_id = ? AND retry_index = ?`,
		taskID, retryIndex).Scan(&runID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r.Get(ctx, runID)
}

