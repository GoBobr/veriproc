package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// runScanCols is the canonical column list / order used by RunRepo Scan helpers.
const runScanCols = `run_id, task_id, station_revision_id, retry_index, working_root,
		       state, canonicality,
		       COALESCE(processing_fingerprint, ''),
		       COALESCE(failure_reason, ''),
		       created_at, prepared_at, dispatched_at, started_at, terminal_at,
		       cancellation_requested_at`

func scanRun(s scanner) (*RunRecord, error) {
	var r RunRecord
	if err := s.Scan(
		&r.RunID, &r.TaskID, &r.StationRevisionID, &r.RetryIndex, &r.WorkingRoot,
		&r.State, &r.Canonicality,
		&r.ProcessingFingerprint, &r.FailureReason,
		&r.CreatedAt, &r.PreparedAt, &r.DispatchedAt, &r.StartedAt, &r.TerminalAt,
		&r.CancellationRequestedAt,
	); err != nil {
		return nil, err
	}
	r.CreatedAt = r.CreatedAt.UTC()
	return &r, nil
}

// scanner abstracts *sql.Row and *sql.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

// MarkPrepared sets the run's processing_fingerprint and prepared_at and
// transitions to "ready". The transition is conditional on current state to
// keep it idempotent under concurrent dispatcher ticks.
func (r *RunRepo) MarkPrepared(ctx context.Context, runID, fingerprint string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs
		SET processing_fingerprint = ?, prepared_at = ?, state = 'ready'
		WHERE run_id = ? AND state IN ('pending','preparing')`,
		fingerprint, at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkPrepared")
}

// MarkDispatched transitions a ready run to dispatched and stores the job id.
func (r *RunRepo) MarkDispatched(ctx context.Context, runID string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET dispatched_at = ?, state = 'dispatched'
		WHERE run_id = ? AND state = 'ready'`,
		at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkDispatched")
}

// MarkRunning transitions dispatched → running.
func (r *RunRepo) MarkRunning(ctx context.Context, runID string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET started_at = COALESCE(started_at, ?), state = 'running'
		WHERE run_id = ? AND state IN ('dispatched','running')`,
		at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkRunning")
}

// MarkReadyForFinalization transitions a running run to "finalizing".
func (r *RunRepo) MarkReadyForFinalization(ctx context.Context, runID string) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET state = 'finalizing'
		WHERE run_id = ? AND state IN ('running','dispatched','finalizing')`, runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkReadyForFinalization")
}

// MarkComplete transitions finalizing → complete and sets canonicality.
func (r *RunRepo) MarkComplete(ctx context.Context, runID, canonicality string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET state = 'complete', canonicality = ?, terminal_at = ?
		WHERE run_id = ? AND state = 'finalizing'`,
		canonicality, at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkComplete")
}

// SetCanonicality updates only the canonicality column on a complete run.
// Used by operator promotion (Spec §3.16 / §7.4.5).
func (r *RunRepo) SetCanonicality(ctx context.Context, runID, canonicality string) error {
	res, err := r.q.ExecContext(ctx,
		`UPDATE runs SET canonicality = ? WHERE run_id = ? AND state = 'complete'`,
		canonicality, runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "SetCanonicality")
}

// MarkFailed transitions any non-terminal state → failed.
func (r *RunRepo) MarkFailed(ctx context.Context, runID, reason string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET state = 'failed', failure_reason = ?, terminal_at = ?
		WHERE run_id = ? AND state NOT IN ('complete','failed','cancelled')`,
		reason, at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkFailed")
}

// MarkCancellationRequested stamps cancellation_requested_at on the run if it
// is currently in a cancellable state. Idempotent: a second call when the
// stamp is already set returns nil. Spec §5.8.
func (r *RunRepo) MarkCancellationRequested(ctx context.Context, runID string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET cancellation_requested_at = COALESCE(cancellation_requested_at, ?)
		WHERE run_id = ?
		  AND state IN ('pending','preparing','ready','dispatched','running','finalizing')`,
		at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkCancellationRequested")
}

// MarkCancelled transitions any non-terminal state → cancelled and stamps
// terminal_at. Spec §5.8: cancellation does not delete records.
func (r *RunRepo) MarkCancelled(ctx context.Context, runID string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET state = 'cancelled', terminal_at = ?
		WHERE run_id = ? AND state NOT IN ('complete','failed','cancelled')`,
		at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkCancelled")
}

// ListByStates returns runs whose state is in any of the supplied values,
// ordered by created_at ASC for deterministic dispatcher scheduling.
func (r *RunRepo) ListByStates(ctx context.Context, states ...string) ([]*RunRecord, error) {
	if len(states) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(states))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(states))
	for _, s := range states {
		args = append(args, s)
	}
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+runScanCols+` FROM runs WHERE state IN (`+placeholders+`) ORDER BY created_at ASC, run_id ASC`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RunRecord
	for rows.Next() {
		rec, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// RunListFilter is used by the public List endpoint.
type RunListFilter struct {
	TaskID            string
	StationID         string
	StationRevisionID string
	State             string
	Canonicality      string
	Fingerprint       string
	CreatedAfter      time.Time
	CreatedBefore     time.Time
	Limit             int
	CursorCreatedAt   time.Time
	CursorRunID       string
}

// RunListPage mirrors ListPage for runs.
type RunListPage struct {
	Items         []*RunRecord
	NextCreatedAt time.Time
	NextRunID     string
	HasMore       bool
}

// List returns one page of runs ordered by (created_at DESC, run_id DESC).
func (r *RunRepo) List(ctx context.Context, f RunListFilter) (*RunListPage, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var (
		conds []string
		args  []any
	)
	if f.TaskID != "" {
		conds = append(conds, "task_id = ?")
		args = append(args, f.TaskID)
	}
	if f.StationRevisionID != "" {
		conds = append(conds, "station_revision_id = ?")
		args = append(args, f.StationRevisionID)
	}
	if f.StationID != "" {
		conds = append(conds,
			"station_revision_id IN (SELECT revision_id FROM station_revisions WHERE station_id = ?)")
		args = append(args, f.StationID)
	}
	if f.State != "" {
		conds = append(conds, "state = ?")
		args = append(args, f.State)
	}
	if f.Canonicality != "" {
		conds = append(conds, "canonicality = ?")
		args = append(args, f.Canonicality)
	}
	if f.Fingerprint != "" {
		conds = append(conds, "processing_fingerprint = ?")
		args = append(args, f.Fingerprint)
	}
	if !f.CreatedAfter.IsZero() {
		conds = append(conds, "created_at >= ?")
		args = append(args, f.CreatedAfter.UTC())
	}
	if !f.CreatedBefore.IsZero() {
		conds = append(conds, "created_at <= ?")
		args = append(args, f.CreatedBefore.UTC())
	}
	if !f.CursorCreatedAt.IsZero() && f.CursorRunID != "" {
		conds = append(conds, "(created_at < ? OR (created_at = ? AND run_id < ?))")
		args = append(args, f.CursorCreatedAt.UTC(), f.CursorCreatedAt.UTC(), f.CursorRunID)
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit+1)
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+runScanCols+` FROM runs`+where+` ORDER BY created_at DESC, run_id DESC LIMIT ?`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	page := &RunListPage{}
	for rows.Next() {
		rec, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > f.Limit {
		page.HasMore = true
		last := page.Items[f.Limit-1]
		page.NextCreatedAt = last.CreatedAt
		page.NextRunID = last.RunID
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

// mustAffect returns ErrNotFound (interpreted as "no eligible row") when the
// UPDATE did not match, so callers can distinguish illegal transitions from
// missing records.
func mustAffect(res sql.Result, runID, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s on run %s (no eligible row)", ErrInvalidTransition, op, runID)
	}
	return nil
}

// ErrInvalidTransition is returned when a state-conditional UPDATE matches
// no rows, indicating either an unknown run or a state that disallows the
// requested transition.
var ErrInvalidTransition = errors.New("store: invalid run state transition")
