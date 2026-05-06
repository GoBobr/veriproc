package store

import (
	"context"
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
