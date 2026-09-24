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
		       cancellation_requested_at, reconciliation_started_at`

func scanRun(s scanner) (*RunRecord, error) {
	var r RunRecord
	if err := s.Scan(
		&r.RunID, &r.TaskID, &r.StationRevisionID, &r.RetryIndex, &r.WorkingRoot,
		&r.State, &r.Canonicality,
		&r.ProcessingFingerprint, &r.FailureReason,
		&r.CreatedAt, &r.PreparedAt, &r.DispatchedAt, &r.StartedAt, &r.TerminalAt,
		&r.CancellationRequestedAt, &r.ReconciliationStartedAt,
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

// MaxRetryIndex returns the highest retry_index among runs for the supplied
// task, or -1 when the task has no runs. Used by PrepareRun to assign the
// next retry index without loading every run in the table.
func (r *RunRepo) MaxRetryIndex(ctx context.Context, taskID string) (int, error) {
	var max sql.NullInt64
	if err := r.q.QueryRowContext(ctx,
		`SELECT MAX(retry_index) FROM runs WHERE task_id = ?`, taskID).Scan(&max); err != nil {
		return 0, err
	}
	if !max.Valid {
		return -1, nil
	}
	return int(max.Int64), nil
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

// MarkCancelled transitions any non-terminal state → cancelled, stamps
// terminal_at, and records the run-level failure reason. Spec §5.8:
// cancellation does not delete records.
func (r *RunRepo) MarkCancelled(ctx context.Context, runID string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET state = 'cancelled', failure_reason = 'cancelled', terminal_at = ?
		WHERE run_id = ? AND state NOT IN ('complete','failed','cancelled')`,
		at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkCancelled")
}

// MarkReconciliationStarted stamps reconciliation_started_at on the run if
// it is not already set, asserting the reconciler has acquired soft ownership
// of the run. Idempotent. Spec §3.13 / §7.8.
func (r *RunRepo) MarkReconciliationStarted(ctx context.Context, runID string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE runs SET reconciliation_started_at = COALESCE(reconciliation_started_at, ?)
		WHERE run_id = ?`, at.UTC(), runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "MarkReconciliationStarted")
}

// ClearReconciliation removes the reconciliation_started_at marker once a
// reconciler pass has converged successfully or the run has reached a
// terminal state.
func (r *RunRepo) ClearReconciliation(ctx context.Context, runID string) error {
	res, err := r.q.ExecContext(ctx,
		`UPDATE runs SET reconciliation_started_at = NULL WHERE run_id = ?`, runID)
	if err != nil {
		return err
	}
	return mustAffect(res, runID, "ClearReconciliation")
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
	States            []string
	Canonicality      string
	Fingerprint       string
	CreatedAfter      time.Time
	CreatedBefore     time.Time
	Limit             int
	CursorCreatedAt   time.Time
	CursorRunID       string
	// CursorTaskID carries the task_id of the last row on the previous
	// page. Used when sorting by task_id.
	CursorTaskID string

	// SortBy controls the primary sort column. Allowed values are
	// "run_id", "created_at", and "task_id". An empty value defaults
	// to "run_id".
	SortBy string
	// SortDir controls sort direction: "ASC" or "DESC". An empty or
	// invalid value defaults to "DESC".
	SortDir string
}

// validRunSortColumns is the whitelist of columns that may appear in the
// ORDER BY clause. This prevents SQL injection through the sort parameter.
var validRunSortColumns = map[string]string{
	"run_id":     "run_id",
	"created_at": "created_at",
	"task_id":    "task_id",
}

// NormaliseRunSortBy returns a safe column name for ORDER BY, defaulting
// to "run_id" when the input is empty or not whitelisted.
func NormaliseRunSortBy(s string) string {
	if col, ok := validRunSortColumns[s]; ok {
		return col
	}
	return "run_id"
}

// NormaliseRunSortDir returns "ASC" or "DESC", defaulting to "DESC".
func NormaliseRunSortDir(s string) string {
	if strings.EqualFold(s, "ASC") {
		return "ASC"
	}
	return "DESC"
}

// RunListPage mirrors ListPage for runs.
type RunListPage struct {
	Items         []*RunRecord
	NextCreatedAt time.Time
	NextRunID     string
	NextTaskID    string
	HasMore       bool
}

// List returns one page of runs ordered by the configured sort column
// (default: run_id DESC). The secondary sort is always run_id in the same
// direction for stable ordering.
func (r *RunRepo) List(ctx context.Context, f RunListFilter) (*RunListPage, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	sortBy := NormaliseRunSortBy(f.SortBy)
	sortDir := NormaliseRunSortDir(f.SortDir)
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
	if len(f.States) > 0 {
		placeholders := strings.Repeat("?,", len(f.States))
		placeholders = placeholders[:len(placeholders)-1]
		conds = append(conds, "state IN ("+placeholders+")")
		for _, state := range f.States {
			args = append(args, state)
		}
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
	if f.CursorRunID != "" {
		switch sortBy {
		case "created_at":
			if !f.CursorCreatedAt.IsZero() {
				if sortDir == "ASC" {
					conds = append(conds, "(created_at > ? OR (created_at = ? AND run_id > ?))")
				} else {
					conds = append(conds, "(created_at < ? OR (created_at = ? AND run_id < ?))")
				}
				args = append(args, f.CursorCreatedAt.UTC(), f.CursorCreatedAt.UTC(), f.CursorRunID)
			}
		case "task_id":
			if sortDir == "ASC" {
				conds = append(conds, "(task_id > ? OR (task_id = ? AND run_id > ?))")
				} else {
				conds = append(conds, "(task_id < ? OR (task_id = ? AND run_id < ?))")
				}
			args = append(args, f.CursorTaskID, f.CursorTaskID, f.CursorRunID)
		default: // run_id
			if sortDir == "ASC" {
				conds = append(conds, "run_id > ?")
			} else {
				conds = append(conds, "run_id < ?")
			}
			args = append(args, f.CursorRunID)
		}
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit+1)
	orderBy := sortBy + " " + sortDir + ", run_id " + sortDir
	rows, err := r.q.QueryContext(ctx,
		`SELECT `+runScanCols+` FROM runs`+where+` ORDER BY `+orderBy+` LIMIT ?`,
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
		page.NextTaskID = last.TaskID
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
