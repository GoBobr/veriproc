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
	RevisionID    string
	StationID     string
	StationName   string
	ContentHash   string
	SchemaVersion string
	EffectiveAt   time.Time
	CreatedAt     time.Time
	// DeclaredOutputs is a JSON-encoded []string of output filenames declared
	// in station.yaml (outputs: [...]). Empty string means none declared.
	DeclaredInputs     string
	DeclaredOutputs    string
	DeclaredDownstream string
	PublicationPolicy  string
	RollingFolders     string
	// DeclaredExecution is a JSON-encoded stations.Execution (executable + args).
	// Empty string means no execution config declared.
	DeclaredExecution string
	// DeclaredJobOrder is a JSON-encoded stations.JobOrderConfig.
	// Empty string means no joborder config declared (defaults apply at runtime).
	DeclaredJobOrder string
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
			(revision_id, station_id, content_hash, schema_version, station_name, effective_at, created_at,
			 declared_inputs, declared_outputs, declared_downstream, publication_policy, rolling_folders,
			 declared_execution, declared_joborder)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		rec.RevisionID, rec.StationID, rec.ContentHash, rec.SchemaVersion,
		rec.StationName, rec.EffectiveAt.UTC(), rec.CreatedAt.UTC(),
		rec.DeclaredInputs, rec.DeclaredOutputs, rec.DeclaredDownstream,
		rec.PublicationPolicy, rec.RollingFolders, rec.DeclaredExecution, rec.DeclaredJobOrder)
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return fmt.Errorf("%w: station_revision station_id=%s hash=%s",
				ErrConflict, rec.StationID, rec.ContentHash)
		}
		return err
	}
	return nil
}

// Upsert inserts or updates a station revision. On conflict it refreshes all
// serialized JSON fields (declared_inputs, declared_outputs, etc.) while
// preserving the original created_at / effective_at timestamps. This ensures
// that normalization changes in code are always reflected in the DB at
// startup without requiring a manual DB wipe.
func (r *StationRevisionRepo) Upsert(ctx context.Context, rec *StationRevisionRecord) error {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = nowUTC()
	}
	if rec.EffectiveAt.IsZero() {
		rec.EffectiveAt = rec.CreatedAt
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO station_revisions
			(revision_id, station_id, content_hash, schema_version, station_name, effective_at, created_at,
			 declared_inputs, declared_outputs, declared_downstream, publication_policy, rolling_folders,
			 declared_execution, declared_joborder)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(revision_id) DO UPDATE SET
			station_name        = excluded.station_name,
			declared_inputs     = excluded.declared_inputs,
			declared_outputs    = excluded.declared_outputs,
			declared_downstream = excluded.declared_downstream,
			publication_policy  = excluded.publication_policy,
			rolling_folders     = excluded.rolling_folders,
			declared_execution  = excluded.declared_execution,
			declared_joborder   = excluded.declared_joborder`,
		rec.RevisionID, rec.StationID, rec.ContentHash, rec.SchemaVersion,
		rec.StationName, rec.EffectiveAt.UTC(), rec.CreatedAt.UTC(),
		rec.DeclaredInputs, rec.DeclaredOutputs, rec.DeclaredDownstream,
		rec.PublicationPolicy, rec.RollingFolders, rec.DeclaredExecution, rec.DeclaredJobOrder)
	return err
}

// Get returns the revision with the supplied id, or ErrNotFound.
func (r *StationRevisionRepo) Get(ctx context.Context, revisionID string) (*StationRevisionRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT revision_id, station_id, content_hash, schema_version,
		       station_name, effective_at, created_at,
		       COALESCE(declared_inputs, ''), COALESCE(declared_outputs, ''),
		       COALESCE(declared_downstream, ''), COALESCE(publication_policy, ''),
		       COALESCE(rolling_folders, ''), COALESCE(declared_execution, ''),
		       COALESCE(declared_joborder, '')
		FROM station_revisions WHERE revision_id = ?`, revisionID)
	var rec StationRevisionRecord
	if err := row.Scan(&rec.RevisionID, &rec.StationID, &rec.ContentHash,
		&rec.SchemaVersion, &rec.StationName, &rec.EffectiveAt, &rec.CreatedAt,
		&rec.DeclaredInputs, &rec.DeclaredOutputs, &rec.DeclaredDownstream,
		&rec.PublicationPolicy, &rec.RollingFolders, &rec.DeclaredExecution,
		&rec.DeclaredJobOrder); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rec.EffectiveAt = rec.EffectiveAt.UTC()
	rec.CreatedAt = rec.CreatedAt.UTC()
	return &rec, nil
}
