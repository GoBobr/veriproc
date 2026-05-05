package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// SplitGroupState constants. Spec §3.11 / §5.5.7.
const (
	SplitGroupStateOpen        = "open"
	SplitGroupStateAggregating = "aggregating"
	SplitGroupStateComplete    = "complete"
	SplitGroupStateFailed      = "failed"
)

// SplitGroupRecord persists one split group (Spec §3.11).
type SplitGroupRecord struct {
	SplitGroupID    string
	Label           string
	Description     string
	State           string
	ExpectedMembers sql.NullInt64
	CanonicalCount  int
	FailedCount     int
	Summary         string
	CreatedAt       time.Time
	ClosedAt        sql.NullTime
	AggregatedAt    sql.NullTime
}

// SplitGroupMember is one (group, run) membership row.
type SplitGroupMember struct {
	SplitGroupID string
	RunID        string
	TaskID       string
	Role         string
	AddedAt      time.Time
}

// SplitGroupRepo persists split groups + memberships.
type SplitGroupRepo struct {
	q       querier
	dialect dialect
}

// SplitGroups returns the repo bound to the connection pool.
func (s *Store) SplitGroups() *SplitGroupRepo {
	return &SplitGroupRepo{q: s.db, dialect: s.dialect}
}

// SplitGroups returns the repo bound to the active transaction.
func (t *Tx) SplitGroups() *SplitGroupRepo {
	return &SplitGroupRepo{q: t.tx, dialect: t.dialect}
}

// EnsureGroup inserts the group if it does not yet exist. Returns the
// (possibly pre-existing) record. Idempotent.
func (r *SplitGroupRepo) EnsureGroup(ctx context.Context, id, label, description string, expected int, at time.Time) (*SplitGroupRecord, error) {
	if g, err := r.Get(ctx, id); err == nil {
		return g, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	expN := sql.NullInt64{}
	if expected > 0 {
		expN = sql.NullInt64{Int64: int64(expected), Valid: true}
	}
	if at.IsZero() {
		at = nowUTC()
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO split_groups
			(split_group_id, label, description, state, expected_members,
			 canonical_count, failed_count, summary, created_at, closed_at, aggregated_at)
		VALUES (?, ?, ?, 'open', ?, 0, 0, NULL, ?, NULL, NULL)`,
		id, nullStr(label), nullStr(description), expN, at.UTC())
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return r.Get(ctx, id)
		}
		return nil, err
	}
	return r.Get(ctx, id)
}

// AddMember records membership of a run in a split group. Idempotent on
// (split_group_id, run_id).
func (r *SplitGroupRepo) AddMember(ctx context.Context, m *SplitGroupMember) error {
	if m.AddedAt.IsZero() {
		m.AddedAt = nowUTC()
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO split_group_members
			(split_group_id, run_id, task_id, role, added_at)
		VALUES (?, ?, ?, ?, ?)`,
		m.SplitGroupID, m.RunID, m.TaskID, nullStr(m.Role), m.AddedAt.UTC())
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return nil
		}
		if r.dialect.IsForeignKeyViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// Get returns the split group with the supplied id.
func (r *SplitGroupRepo) Get(ctx context.Context, id string) (*SplitGroupRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT split_group_id, COALESCE(label,''), COALESCE(description,''),
		       state, expected_members, canonical_count, failed_count,
		       COALESCE(summary,''), created_at, closed_at, aggregated_at
		FROM split_groups WHERE split_group_id = ?`, id)
	var g SplitGroupRecord
	if err := row.Scan(&g.SplitGroupID, &g.Label, &g.Description,
		&g.State, &g.ExpectedMembers, &g.CanonicalCount, &g.FailedCount,
		&g.Summary, &g.CreatedAt, &g.ClosedAt, &g.AggregatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	g.CreatedAt = g.CreatedAt.UTC()
	return &g, nil
}

// SplitGroupListFilter narrows List results.
type SplitGroupListFilter struct {
	State         string
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Limit         int
}

// List returns split groups filtered by state and creation time, ordered by
// created_at DESC. Limit defaults to 50.
func (r *SplitGroupRepo) List(ctx context.Context, f SplitGroupListFilter) ([]*SplitGroupRecord, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	var (
		conds []string
		args  []any
	)
	if f.State != "" {
		conds = append(conds, "state = ?")
		args = append(args, f.State)
	}
	if !f.CreatedAfter.IsZero() {
		conds = append(conds, "created_at >= ?")
		args = append(args, f.CreatedAfter.UTC())
	}
	if !f.CreatedBefore.IsZero() {
		conds = append(conds, "created_at <= ?")
		args = append(args, f.CreatedBefore.UTC())
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, f.Limit)
	rows, err := r.q.QueryContext(ctx, `
		SELECT split_group_id, COALESCE(label,''), COALESCE(description,''),
		       state, expected_members, canonical_count, failed_count,
		       COALESCE(summary,''), created_at, closed_at, aggregated_at
		FROM split_groups`+where+` ORDER BY created_at DESC, split_group_id DESC LIMIT ?`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SplitGroupRecord
	for rows.Next() {
		var g SplitGroupRecord
		if err := rows.Scan(&g.SplitGroupID, &g.Label, &g.Description,
			&g.State, &g.ExpectedMembers, &g.CanonicalCount, &g.FailedCount,
			&g.Summary, &g.CreatedAt, &g.ClosedAt, &g.AggregatedAt); err != nil {
			return nil, err
		}
		g.CreatedAt = g.CreatedAt.UTC()
		out = append(out, &g)
	}
	return out, rows.Err()
}

// ListMembers returns memberships for a group, ordered by added_at.
func (r *SplitGroupRepo) ListMembers(ctx context.Context, groupID string) ([]*SplitGroupMember, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT split_group_id, run_id, task_id, COALESCE(role,''), added_at
		FROM split_group_members WHERE split_group_id = ?
		ORDER BY added_at ASC, run_id ASC`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*SplitGroupMember
	for rows.Next() {
		var m SplitGroupMember
		if err := rows.Scan(&m.SplitGroupID, &m.RunID, &m.TaskID, &m.Role, &m.AddedAt); err != nil {
			return nil, err
		}
		m.AddedAt = m.AddedAt.UTC()
		out = append(out, &m)
	}
	return out, rows.Err()
}

// SetState transitions a group's state and stamps closed_at / aggregated_at
// based on the target state.
func (r *SplitGroupRepo) SetState(ctx context.Context, id, state string, at time.Time) error {
	switch state {
	case SplitGroupStateAggregating:
		_, err := r.q.ExecContext(ctx, `
			UPDATE split_groups SET state = ?, closed_at = COALESCE(closed_at, ?)
			WHERE split_group_id = ?`, state, at.UTC(), id)
		return err
	case SplitGroupStateComplete, SplitGroupStateFailed:
		_, err := r.q.ExecContext(ctx, `
			UPDATE split_groups SET state = ?, closed_at = COALESCE(closed_at, ?), aggregated_at = ?
			WHERE split_group_id = ?`, state, at.UTC(), at.UTC(), id)
		return err
	default:
		_, err := r.q.ExecContext(ctx, `
			UPDATE split_groups SET state = ? WHERE split_group_id = ?`, state, id)
		return err
	}
}

// SetAggregation persists aggregator output (counts + summary text).
func (r *SplitGroupRepo) SetAggregation(ctx context.Context, id string, canonicalCount, failedCount int, summary string) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE split_groups
		SET canonical_count = ?, failed_count = ?, summary = ?
		WHERE split_group_id = ?`,
		canonicalCount, failedCount, nullStr(summary), id)
	return err
}
