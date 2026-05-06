package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// StationRevisionRecord matches Spec §3.2 / §4.3 (station_revisions).
type StationRevisionRecord struct {
	RevisionID      string
	StationID       string
	ContentHash     string
	SchemaVersion   string
	Label           string
	EffectiveAt     time.Time
	CreatedAt       time.Time
	// DeclaredOutputs is a JSON-encoded []string of output filenames declared
	// in station.yaml (outputs: [...]). Empty string means none declared.
	DeclaredOutputs string
	// DeclaredScripts is a JSON-encoded map[string]string of script name →
	// resolved absolute path (e.g. {"run": "/abs/scripts/run.sh"}).
	DeclaredScripts string
}

// StationRevisionRepo persists station revisions.
type StationRevisionRepo struct {
	q       querier
	dialect dialect
}

// Insert creates a new revision. Returns ErrConflict on (station_id, content_hash) reuse.
func (r *StationRevisionRepo) Insert(ctx context.Context, rec *StationRevisionRecord) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = nowUTC()
	}
	if rec.EffectiveAt.IsZero() {
		rec.EffectiveAt = rec.CreatedAt
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO station_revisions
			(revision_id, station_id, content_hash, schema_version, label, effective_at, created_at, declared_outputs, declared_scripts)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RevisionID, rec.StationID, rec.ContentHash, rec.SchemaVersion,
		nullStr(rec.Label), rec.EffectiveAt.UTC(), rec.CreatedAt.UTC(), rec.DeclaredOutputs, rec.DeclaredScripts)
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return fmt.Errorf("%w: station_revision station_id=%s hash=%s",
				ErrConflict, rec.StationID, rec.ContentHash)
		}
		return err
	}
	return nil
}

// Get returns the revision with the supplied id, or ErrNotFound.
func (r *StationRevisionRepo) Get(ctx context.Context, revisionID string) (*StationRevisionRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT revision_id, station_id, content_hash, schema_version,
		       COALESCE(label, ''), effective_at, created_at,
		       COALESCE(declared_outputs, ''), COALESCE(declared_scripts, '')
		FROM station_revisions WHERE revision_id = ?`, revisionID)
	var rec StationRevisionRecord
	if err := row.Scan(&rec.RevisionID, &rec.StationID, &rec.ContentHash,
		&rec.SchemaVersion, &rec.Label, &rec.EffectiveAt, &rec.CreatedAt,
		&rec.DeclaredOutputs, &rec.DeclaredScripts); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rec.EffectiveAt = rec.EffectiveAt.UTC()
	rec.CreatedAt = rec.CreatedAt.UTC()
	return &rec, nil
}
