package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// IdempotencyRecord persists one client submission attempt within a scope
// (Spec §3.3 / §4.3 idempotency_records).
type IdempotencyRecord struct {
	ID          string
	Scope       string
	Key         string
	RequestHash string
	TaskID      string
	CreatedAt   time.Time
	ExpiresAt   sql.NullTime
}

// IdempotencyRepo persists idempotency records.
type IdempotencyRepo struct {
	q       querier
	dialect dialect
}

// Insert creates a new idempotency record. Returns ErrConflict on (scope, key) reuse.
func (r *IdempotencyRepo) Insert(ctx context.Context, rec *IdempotencyRecord) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = nowUTC()
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO idempotency_records
			(idempotency_record_id, scope, key, request_hash, task_id, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.ID, rec.Scope, rec.Key, rec.RequestHash,
		nullStr(rec.TaskID), rec.CreatedAt.UTC(), nullTime(rec.ExpiresAt))
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return fmt.Errorf("%w: idempotency (scope=%s, key=%s)", ErrConflict, rec.Scope, rec.Key)
		}
		return err
	}
	return nil
}

// GetByKey returns the record for (scope, key) or ErrNotFound. Records past
// their expires_at remain visible until Sweep removes them; clients see a
// stable replay window even if the sweeper has not yet run.
func (r *IdempotencyRepo) GetByKey(ctx context.Context, scope, key string) (*IdempotencyRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT idempotency_record_id, scope, key, request_hash,
		       COALESCE(task_id, ''), created_at, expires_at
		FROM idempotency_records WHERE scope = ? AND key = ?`, scope, key)
	var rec IdempotencyRecord
	if err := row.Scan(&rec.ID, &rec.Scope, &rec.Key, &rec.RequestHash,
		&rec.TaskID, &rec.CreatedAt, &rec.ExpiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return &rec, nil
}

// Sweep deletes idempotency_records whose expires_at is non-NULL and strictly
// before `before`. Records with NULL expires_at are retained indefinitely.
// Returns the number of rows deleted. Spec §3.3 retention sweeper.
func (r *IdempotencyRepo) Sweep(ctx context.Context, before time.Time) (int, error) {
	res, err := r.q.ExecContext(ctx,
		`DELETE FROM idempotency_records WHERE expires_at IS NOT NULL AND expires_at < ?`,
		before.UTC())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}
