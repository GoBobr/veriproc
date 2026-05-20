package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// JobRecord persists one scheduler submission for a run (Spec §3.9 / §4.3.8).
type JobRecord struct {
	JobID                   string
	RunID                   string
	Executor                string
	SchedulerID             string
	SchedulerState          string
	Node                    string // scheduler-allocated node(s); empty until first observed
	SubmissionAttempt       int
	SubmittedAt             sql.NullTime
	LastObservedAt          sql.NullTime
	TerminalAt              sql.NullTime
	CancellationRequestedAt sql.NullTime
	ReconciliationStatus    string
}

// JobRepo persists jobs.
type JobRepo struct {
	q       querier
	dialect dialect
}

// Insert creates a job.
func (r *JobRepo) Insert(ctx context.Context, j *JobRecord) error {
	if j.SubmissionAttempt == 0 {
		j.SubmissionAttempt = 1
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO jobs (
			job_id, run_id, executor, scheduler_id, scheduler_state,
			submission_attempt, submitted_at, last_observed_at, terminal_at,
			cancellation_requested_at, reconciliation_status
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.JobID, j.RunID, j.Executor, nullStr(j.SchedulerID), nullStr(j.SchedulerState),
		j.SubmissionAttempt, nullTime(j.SubmittedAt), nullTime(j.LastObservedAt),
		nullTime(j.TerminalAt), nullTime(j.CancellationRequestedAt),
		nullStr(j.ReconciliationStatus))
	if err != nil {
		if r.dialect.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: job references missing run", ErrConflict)
		}
		return err
	}
	return nil
}

// UpdateState records a new scheduler state and bumps last_observed_at. If the
// state is terminal (succeeded/failed/cancelled) it also stamps terminal_at.
func (r *JobRepo) UpdateState(ctx context.Context, jobID, schedulerState string, observedAt time.Time, terminal bool) error {
	if terminal {
		_, err := r.q.ExecContext(ctx, `
			UPDATE jobs SET scheduler_state = ?, last_observed_at = ?, terminal_at = COALESCE(terminal_at, ?)
			WHERE job_id = ?`, schedulerState, observedAt.UTC(), observedAt.UTC(), jobID)
		return err
	}
	_, err := r.q.ExecContext(ctx, `
		UPDATE jobs SET scheduler_state = ?, last_observed_at = ?
		WHERE job_id = ?`, schedulerState, observedAt.UTC(), jobID)
	return err
}

// StampCancellationRequested records cancellation_requested_at on the job
// (idempotent: COALESCE preserves first stamp). Spec §5.8.
func (r *JobRepo) StampCancellationRequested(ctx context.Context, jobID string, at time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE jobs SET cancellation_requested_at = COALESCE(cancellation_requested_at, ?) WHERE job_id = ?`,
		at.UTC(), jobID)
	return err
}

// SetNode records the scheduler-allocated node(s) for a job. It is a no-op if
// node is empty, and never overwrites a known node with an empty string so
// that existing node info is preserved after the scheduler releases the
// allocation.
func (r *JobRepo) SetNode(ctx context.Context, jobID, node string) error {
	if node == "" {
		return nil
	}
	_, err := r.q.ExecContext(ctx,
		`UPDATE jobs SET execution_node = ? WHERE job_id = ?`, node, jobID)
	return err
}

// Get returns a job by id.
func (r *JobRepo) Get(ctx context.Context, jobID string) (*JobRecord, error) {
	row := r.q.QueryRowContext(ctx, jobSelect+` WHERE job_id = ?`, jobID)
	return scanJob(row)
}

// ListByRun returns all jobs for a run, ordered by submission_attempt.
func (r *JobRepo) ListByRun(ctx context.Context, runID string) ([]*JobRecord, error) {
	rows, err := r.q.QueryContext(ctx, jobSelect+` WHERE run_id = ? ORDER BY submission_attempt ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JobRecord
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

const jobSelect = `SELECT job_id, run_id, executor,
	COALESCE(scheduler_id, ''), COALESCE(scheduler_state, ''), COALESCE(execution_node, ''),
	submission_attempt, submitted_at, last_observed_at, terminal_at,
	cancellation_requested_at, COALESCE(reconciliation_status, '')
FROM jobs`

func scanJob(s scanner) (*JobRecord, error) {
	var j JobRecord
	if err := s.Scan(&j.JobID, &j.RunID, &j.Executor,
		&j.SchedulerID, &j.SchedulerState, &j.Node,
		&j.SubmissionAttempt, &j.SubmittedAt, &j.LastObservedAt, &j.TerminalAt,
		&j.CancellationRequestedAt, &j.ReconciliationStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &j, nil
}
