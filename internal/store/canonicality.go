package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

// CanonicalityChange records one transition of canonical ownership for a
// processing fingerprint. Spec §3.16 / §4.3.15.
type CanonicalityChange struct {
	AuditID        string
	FingerprintID  string
	PreviousRunID  sql.NullString
	NewRunID       string
	Action         string // "elect" | "promote" | "supersede"
	Reason         string
	Actor          string
	OccurredAt     time.Time
}

// CanonicalityRepo persists CanonicalityChange rows.
type CanonicalityRepo struct {
	q       querier
	dialect dialect
}

// RecordElection inserts an "elect" audit row for the first canonical
// election. Errors are surfaced unless they are unique-constraint violations
// on the audit_id (which indicate a benign duplicate retry).
func (r *CanonicalityRepo) RecordElection(ctx context.Context,
	fpID, newRunID, reason, actor string, at time.Time,
) error {
	id := "ce-" + sha8Hex(fpID+newRunID+at.UTC().Format(time.RFC3339Nano))
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO canonicality_audit
			(audit_id, fingerprint_id, previous_run_id, new_run_id, action, reason, actor, occurred_at)
		VALUES (?, ?, NULL, ?, 'elect', ?, ?, ?)`,
		id, fpID, newRunID, reason, actor, at.UTC())
	if err != nil && r.dialect.IsUniqueViolation(err) {
		return nil
	}
	return err
}

// RecordPromotion inserts a "promote" or "supersede" audit row reflecting an
// operator-driven canonical change.
func (r *CanonicalityRepo) RecordPromotion(ctx context.Context,
	fpID, previousRunID, newRunID, action, reason, actor string, at time.Time,
) error {
	if action == "" {
		action = "promote"
	}
	id := "cp-" + sha8Hex(fpID+previousRunID+newRunID+at.UTC().Format(time.RFC3339Nano))
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO canonicality_audit
			(audit_id, fingerprint_id, previous_run_id, new_run_id, action, reason, actor, occurred_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, fpID, nullStr(previousRunID), newRunID, action, reason, actor, at.UTC())
	return err
}

// ListByFingerprint returns the audit history for a fingerprint, ordered
// chronologically.
func (r *CanonicalityRepo) ListByFingerprint(ctx context.Context, fpID string) ([]*CanonicalityChange, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT audit_id, fingerprint_id, previous_run_id, new_run_id,
		       action, COALESCE(reason, ''), COALESCE(actor, ''), occurred_at
		FROM canonicality_audit
		WHERE fingerprint_id = ?
		ORDER BY occurred_at ASC, audit_id ASC`, fpID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CanonicalityChange
	for rows.Next() {
		var c CanonicalityChange
		if err := rows.Scan(&c.AuditID, &c.FingerprintID, &c.PreviousRunID,
			&c.NewRunID, &c.Action, &c.Reason, &c.Actor, &c.OccurredAt); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return out, nil
			}
			return nil, err
		}
		c.OccurredAt = c.OccurredAt.UTC()
		out = append(out, &c)
	}
	return out, rows.Err()
}

func sha8Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
}
