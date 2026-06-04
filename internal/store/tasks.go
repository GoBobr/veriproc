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
	TaskID               string
	SchemaVersion        string
	DestinationStationID string
	WindowStart          time.Time
	WindowEnd            time.Time
	Force                bool
	ParentTaskID         string
	// ParentRunRetryIndex references the contributing run via task-scoped
	// composite identity (task_id, retry_index). Spec §3.7 / §3.6.1.
	ParentRunRetryIndex sql.NullInt64
	SplitGroupID        string
	Priority            string
	ClientMetadata      json.RawMessage
	RoutingContent      json.RawMessage
	RoutingContentHash  string
	SubmissionOrigin    string // "client" | "backend"
	State               string
	FailureSummary      string
	// LatestRetryIndex is the retry_index of the most recent run for this
	// task; nil until a run has been prepared.
	LatestRetryIndex sql.NullInt64
	// CanonicalRetryIndex is the retry_index of the run currently elected
	// canonical for this task (Spec §3.10).
	CanonicalRetryIndex sql.NullInt64
	IdempotencyRecordID string
	CreatedAt           time.Time
	CompletedAt         sql.NullTime

	// JoinArrivalCount is incremented atomically each time an upstream
	// producer finalises for a join task. When it reaches the expected count
	// the task is advanced from waiting_inputs to accepted.
	JoinArrivalCount int

	// LatestRunID and CanonicalRunID are non-persisted convenience fields
	// populated by Get/List via JOIN onto runs(task_id, retry_index). They
	// expose the internal surrogate run_id and exist solely for backend code
	// that still resolves runs by surrogate (e.g. job/artifact lookups). The
	// external contract is task-scoped retry_index / run_ref (Spec §3.6.1).
	LatestRunID    string
	CanonicalRunID string
	// ParentRunID is similarly a non-persisted convenience derived from
	// (parent_task_id, parent_run_retry_index) for backend lookups.
	ParentRunID string
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
			destination_station_id,
			window_start, window_end,
			force,
			parent_task_id, parent_run_retry_index, split_group_id,
			priority, client_metadata,
			routing_content, routing_content_hash,
			submission_origin, state,
			failure_summary, latest_retry_index, canonical_retry_index,
			idempotency_record_id, created_at, completed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TaskID, t.SchemaVersion,
		t.DestinationStationID,
		t.WindowStart.UTC(), t.WindowEnd.UTC(),
		boolInt(t.Force),
		nullStr(t.ParentTaskID), nullableInt(t.ParentRunRetryIndex), nullStr(t.SplitGroupID),
		nullStr(t.Priority), nullJSON(t.ClientMetadata),
		string(t.RoutingContent), t.RoutingContentHash,
		t.SubmissionOrigin, t.State,
		nullStr(t.FailureSummary), nullableInt(t.LatestRetryIndex), nullableInt(t.CanonicalRetryIndex),
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
	if err != nil {
		return t, err
	}
	r.populateRunRefs(ctx, t)
	return t, nil
}

// populateRunRefs fills the non-persisted LatestRunID / CanonicalRunID
// convenience fields by looking up the surrogate run_id for the persisted
// retry_index projections. Errors are ignored (best-effort backend helper).
func (r *TaskRepo) populateRunRefs(ctx context.Context, t *TaskRecord) {
	if t == nil {
		return
	}
	if t.LatestRetryIndex.Valid {
		var id string
		if err := r.q.QueryRowContext(ctx,
			`SELECT run_id FROM runs WHERE task_id = ? AND retry_index = ?`,
			t.TaskID, t.LatestRetryIndex.Int64).Scan(&id); err == nil {
			t.LatestRunID = id
		}
	}
	if t.CanonicalRetryIndex.Valid {
		var id string
		if err := r.q.QueryRowContext(ctx,
			`SELECT run_id FROM runs WHERE task_id = ? AND retry_index = ?`,
			t.TaskID, t.CanonicalRetryIndex.Int64).Scan(&id); err == nil {
			t.CanonicalRunID = id
		}
	}
	if t.ParentTaskID != "" && t.ParentRunRetryIndex.Valid {
		var id string
		if err := r.q.QueryRowContext(ctx,
			`SELECT run_id FROM runs WHERE task_id = ? AND retry_index = ?`,
			t.ParentTaskID, t.ParentRunRetryIndex.Int64).Scan(&id); err == nil {
			t.ParentRunID = id
		}
	}
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

// SetStateIfCurrent updates a task state only when it currently matches the
// supplied state. It returns true when a row was changed.
func (r *TaskRepo) SetStateIfCurrent(ctx context.Context, taskID, currentState, nextState, failureSummary string) (bool, error) {
	res, err := r.q.ExecContext(ctx,
		`UPDATE tasks SET state = ?, failure_summary = ? WHERE task_id = ? AND state = ?`,
		nextState, nullStr(failureSummary), taskID, currentState)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// IncrementJoinArrival atomically increments join_arrival_count for the task
// and returns the new value. It is used by the join wakeup logic to determine
// when all expected upstream producers have finalised.
func (r *TaskRepo) IncrementJoinArrival(ctx context.Context, taskID string) (int64, error) {
	var count int64
	err := r.q.QueryRowContext(ctx,
		`UPDATE tasks SET join_arrival_count = join_arrival_count + 1
		 WHERE task_id = ?
		 RETURNING join_arrival_count`,
		taskID).Scan(&count)
	if err != nil {
		return 0, err
	}
	return count, nil
}

// SetLatestRun records the latest run's retry_index for a task.
func (r *TaskRepo) SetLatestRun(ctx context.Context, taskID string, retryIndex int) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE tasks SET latest_retry_index = ? WHERE task_id = ?`, retryIndex, taskID)
	return err
}

// SetCanonicalRun records the canonical retry_index for a task and stamps
// completed_at if not already set.
func (r *TaskRepo) SetCanonicalRun(ctx context.Context, taskID string, retryIndex int, completedAt time.Time) error {
	_, err := r.q.ExecContext(ctx,
		`UPDATE tasks SET canonical_retry_index = ?,
		 completed_at = COALESCE(completed_at, ?) WHERE task_id = ?`,
		retryIndex, completedAt.UTC(), taskID)
	return err
}

// ListFilter constrains List queries. Zero values mean "no filter".
type ListFilter struct {
	DestinationStationID string
	State                string
	ParentTaskID         string
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
	Items         []*TaskRecord
	NextCreatedAt time.Time
	NextTaskID    string
	HasMore       bool
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
	if f.State != "" {
		conds = append(conds, "state = ?")
		args = append(args, f.State)
	}
	if f.ParentTaskID != "" {
		conds = append(conds, "parent_task_id = ?")
		args = append(args, f.ParentTaskID)
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
	for _, t := range page.Items {
		r.populateRunRefs(ctx, t)
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
	destination_station_id,
	window_start, window_end,
	force,
	COALESCE(parent_task_id, ''),
	parent_run_retry_index,
	COALESCE(split_group_id, ''),
	COALESCE(priority, ''),
	COALESCE(client_metadata, ''),
	routing_content, routing_content_hash,
	submission_origin, state,
	COALESCE(failure_summary, ''),
	latest_retry_index,
	canonical_retry_index,
	COALESCE(idempotency_record_id, ''),
	created_at, completed_at,
	join_arrival_count`

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
		&t.DestinationStationID,
		&t.WindowStart, &t.WindowEnd,
		&force,
		&t.ParentTaskID, &t.ParentRunRetryIndex, &t.SplitGroupID,
		&t.Priority, &clientMd,
		&routing, &t.RoutingContentHash,
		&t.SubmissionOrigin, &t.State,
		&t.FailureSummary, &t.LatestRetryIndex, &t.CanonicalRetryIndex,
		&t.IdempotencyRecordID,
		&t.CreatedAt, &t.CompletedAt,
		&t.JoinArrivalCount,
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

func nullableInt(n sql.NullInt64) any {
	if !n.Valid {
		return nil
	}
	return n.Int64
}
