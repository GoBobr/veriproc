package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// TaskRecord is the persisted shape of a task (Spec §4.3.1).
type TaskRecord struct {
	TaskID                 string
	SchemaVersion          string
	DestinationStationID   string
	DestinationProcType    string
	WindowStart            time.Time
	WindowEnd              time.Time
	Force                  bool
	ParentTaskID           string
	ParentRunID            string
	SplitGroupID           string
	Priority               string
	ClientMetadata         json.RawMessage
	RoutingContent         json.RawMessage
	RoutingContentHash     string
	SubmissionOrigin       string // "client" | "backend"
	State                  string
	FailureSummary         string
	LatestRunID            string
	CanonicalRunID         string
	IdempotencyRecordID    string
	CreatedAt              time.Time
	CompletedAt            sql.NullTime
}

// TaskRepo persists and queries task records.
type TaskRepo struct {
	q       querier
	dialect dialect
}

// Insert creates a new task. The caller is responsible for assigning task_id
// and routing_content/routing_content_hash. Returns ErrConflict if task_id
// already exists.
func (r *TaskRepo) Insert(ctx context.Context, t *TaskRecord) error {
	if t.CreatedAt.IsZero() {
		t.CreatedAt = nowUTC()
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO tasks (
			task_id, schema_version,
			destination_station_id, destination_proc_type,
			window_start, window_end,
			force,
			parent_task_id, parent_run_id, split_group_id,
			priority, client_metadata,
			routing_content, routing_content_hash,
			submission_origin, state,
			failure_summary, latest_run_id, canonical_run_id,
			idempotency_record_id, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TaskID, t.SchemaVersion,
		nullStr(t.DestinationStationID), nullStr(t.DestinationProcType),
		t.WindowStart.UTC(), t.WindowEnd.UTC(),
		boolInt(t.Force),
		nullStr(t.ParentTaskID), nullStr(t.ParentRunID), nullStr(t.SplitGroupID),
		nullStr(t.Priority), nullJSON(t.ClientMetadata),
		string(t.RoutingContent), t.RoutingContentHash,
		t.SubmissionOrigin, t.State,
		nullStr(t.FailureSummary), nullStr(t.LatestRunID), nullStr(t.CanonicalRunID),
		nullStr(t.IdempotencyRecordID), t.CreatedAt.UTC(), nullTime(t.CompletedAt),
	)
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return fmt.Errorf("%w: task_id=%s", ErrConflict, t.TaskID)
		}
		return err
	}
	return nil
}

// Get returns the task with the supplied id, or ErrNotFound.
func (r *TaskRepo) Get(ctx context.Context, taskID string) (*TaskRecord, error) {
	row := r.q.QueryRowContext(ctx, taskSelectColumns+` FROM tasks WHERE task_id = ?`, taskID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return t, err
}

// SetState updates the task's state and (optionally) failure summary. No
// preconditions are enforced; callers compose state machines at the service
// layer.
func (r *TaskRepo) SetState(ctx context.Context, taskID, state, failureSummary string) error {
	res, err := r.q.ExecContext(ctx,
		`UPDATE tasks SET state = ?, failure_summary = ? WHERE task_id = ?`,
		state, nullStr(failureSummary), taskID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetLatestRun records the latest run id for a task (Spec §5.6: latest_run
// follows backend-defined ordering; the service is responsible for ordering).
func (r *TaskRepo) SetLatestRun(ctx context.Context, taskID, runID string) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE tasks SET latest_run_id = ? WHERE task_id = ?`, runID, taskID)
	return err
}

// SetCanonicalRun records the canonical run id for a task and stamps
// completed_at if not already set.
func (r *TaskRepo) SetCanonicalRun(ctx context.Context, taskID, runID string, completedAt time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE tasks SET canonical_run_id = ?,
		 completed_at = COALESCE(completed_at, ?) WHERE task_id = ?`,
		runID, completedAt.UTC(), taskID)
	return err
}

// ListFilter constrains List queries. Zero values mean "no filter".
type ListFilter struct {
	DestinationStationID string
	DestinationProcType  string
	State                string
	ParentTaskID         string
	ParentRunID          string
	SplitGroupID         string
	Force                *bool
	CreatedBefore        time.Time
	CreatedAfter         time.Time
	WindowOverlapStart   time.Time
	WindowOverlapEnd     time.Time

	// Pagination cursor. If non-zero, only tasks strictly older (created_at,
	// task_id) than the cursor are returned.
	CursorCreatedAt time.Time
	CursorTaskID    string

	// Limit must be 1..200; the caller is expected to clamp.
	Limit int
}

// ListPage holds one page of task records plus a cursor describing the next
// page (or empty values if there are no more results).
type ListPage struct {
	Items          []*TaskRecord
	NextCreatedAt  time.Time
	NextTaskID     string
	HasMore        bool
}

// List returns one page of tasks ordered by (created_at DESC, task_id DESC).
// Ordering matches Spec §5.4.2 (default ordering, stable tie-breaker).
func (r *TaskRepo) List(ctx context.Context, f ListFilter) (*ListPage, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var (
		conds []string
		args  []any
	)
	if f.DestinationStationID != "" {
		conds = append(conds, "destination_station_id = ?")
		args = append(args, f.DestinationStationID)
	}
	if f.DestinationProcType != "" {
		conds = append(conds, "destination_proc_type = ?")
		args = append(args, f.DestinationProcType)
	}
	if f.State != "" {
		conds = append(conds, "state = ?")
		args = append(args, f.State)
	}
	if f.ParentTaskID != "" {
		conds = append(conds, "parent_task_id = ?")
		args = append(args, f.ParentTaskID)
	}
	if f.ParentRunID != "" {
		conds = append(conds, "parent_run_id = ?")
		args = append(args, f.ParentRunID)
	}
	if f.SplitGroupID != "" {
		conds = append(conds, "split_group_id = ?")
		args = append(args, f.SplitGroupID)
	}
	if f.Force != nil {
		conds = append(conds, "force = ?")
		args = append(args, boolInt(*f.Force))
	}
	if !f.CreatedAfter.IsZero() {
		conds = append(conds, "created_at >= ?")
		args = append(args, f.CreatedAfter.UTC())
	}
	if !f.CreatedBefore.IsZero() {
		conds = append(conds, "created_at <= ?")
		args = append(args, f.CreatedBefore.UTC())
	}
	if !f.WindowOverlapStart.IsZero() && !f.WindowOverlapEnd.IsZero() {
		conds = append(conds, "window_start <= ? AND window_end >= ?")
		args = append(args, f.WindowOverlapEnd.UTC(), f.WindowOverlapStart.UTC())
	}
	if !f.CursorCreatedAt.IsZero() && f.CursorTaskID != "" {
		conds = append(conds,
			"(created_at < ? OR (created_at = ? AND task_id < ?))")
		args = append(args, f.CursorCreatedAt.UTC(), f.CursorCreatedAt.UTC(), f.CursorTaskID)
	}

	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit+1)

	rows, err := r.q.QueryContext(ctx,
		taskSelectColumns+` FROM tasks`+where+` ORDER BY created_at DESC, task_id DESC LIMIT ?`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &ListPage{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		page.Items = append(page.Items, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Items) > f.Limit {
		page.HasMore = true
		last := page.Items[f.Limit-1]
		page.NextCreatedAt = last.CreatedAt
		page.NextTaskID = last.TaskID
		page.Items = page.Items[:f.Limit]
	}
	return page, nil
}

const taskSelectColumns = `SELECT
	task_id, schema_version,
	COALESCE(destination_station_id, ''),
	COALESCE(destination_proc_type, ''),
	window_start, window_end,
	force,
	COALESCE(parent_task_id, ''),
	COALESCE(parent_run_id, ''),
	COALESCE(split_group_id, ''),
	COALESCE(priority, ''),
	COALESCE(client_metadata, ''),
	routing_content, routing_content_hash,
	submission_origin, state,
	COALESCE(failure_summary, ''),
	COALESCE(latest_run_id, ''),
	COALESCE(canonical_run_id, ''),
	COALESCE(idempotency_record_id, ''),
	created_at, completed_at`

// rowScanner abstracts *sql.Row and *sql.Rows for shared scan logic.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanTask(s rowScanner) (*TaskRecord, error) {
	var (
		t        TaskRecord
		force    int
		clientMd string
		routing  string
	)
	if err := s.Scan(
		&t.TaskID, &t.SchemaVersion,
		&t.DestinationStationID, &t.DestinationProcType,
		&t.WindowStart, &t.WindowEnd,
		&force,
		&t.ParentTaskID, &t.ParentRunID, &t.SplitGroupID,
		&t.Priority, &clientMd,
		&routing, &t.RoutingContentHash,
		&t.SubmissionOrigin, &t.State,
		&t.FailureSummary, &t.LatestRunID, &t.CanonicalRunID,
		&t.IdempotencyRecordID,
		&t.CreatedAt, &t.CompletedAt,
	); err != nil {
		return nil, err
	}
	t.WindowStart = t.WindowStart.UTC()
	t.WindowEnd = t.WindowEnd.UTC()
	t.CreatedAt = t.CreatedAt.UTC()
	t.Force = force != 0
	if clientMd != "" {
		t.ClientMetadata = json.RawMessage(clientMd)
	}
	if routing != "" {
		t.RoutingContent = json.RawMessage(routing)
	}
	return &t, nil
}

// helpers ---------------------------------------------------------------

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

func nullTime(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return t.Time.UTC()
}
