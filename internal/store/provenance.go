package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ProvenanceLink persists a machine-queryable relationship between domain objects.
type ProvenanceLink struct {
	LinkID           string
	SourceType       string
	SourceID         string
	TargetType       string
	TargetID         string
	RelationshipType string
	Role             string
	Reason           string
	CreatedAt        time.Time
}

// ProvenanceRepo persists provenance_links records.
type ProvenanceRepo struct {
	q       querier
	dialect dialect
}

// Insert records a provenance link. Duplicate source/target/type links return ErrConflict.
func (r *ProvenanceRepo) Insert(ctx context.Context, link *ProvenanceLink) error {
	if link.CreatedAt.IsZero() {
		link.CreatedAt = nowUTC()
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO provenance_links
			(link_id, source_type, source_id, target_type, target_id, relationship_type, role, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		link.LinkID, link.SourceType, link.SourceID, link.TargetType, link.TargetID,
		link.RelationshipType, nullStr(link.Role), nullStr(link.Reason), link.CreatedAt.UTC())
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return ErrConflict
		}
		if r.dialect.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: provenance link references missing object", ErrConflict)
		}
		return err
	}
	return nil
}

// ListByTarget returns provenance links pointing at the supplied object.
func (r *ProvenanceRepo) ListByTarget(ctx context.Context, targetType, targetID string) ([]*ProvenanceLink, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT link_id, source_type, source_id, target_type, target_id, relationship_type,
		       COALESCE(role, ''), COALESCE(reason, ''), created_at
		FROM provenance_links
		WHERE target_type = ? AND target_id = ?
		ORDER BY created_at ASC, link_id ASC`, targetType, targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	defer rows.Close()
	out := []*ProvenanceLink{}
	for rows.Next() {
		var link ProvenanceLink
		if err := rows.Scan(&link.LinkID, &link.SourceType, &link.SourceID, &link.TargetType, &link.TargetID,
			&link.RelationshipType, &link.Role, &link.Reason, &link.CreatedAt); err != nil {
			return nil, err
		}
		link.CreatedAt = link.CreatedAt.UTC()
		out = append(out, &link)
	}
	return out, rows.Err()
}
