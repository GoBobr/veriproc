package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// StationListRecord is the operator-facing current station view derived from
// the latest known revision for each station_id.
type StationListRecord struct {
	StationID          string
	StationName        string
	DeclaredDownstream string // raw JSON, empty if none
	DeclaredInputs     string // raw JSON, empty if none
	DeclaredOutputs    string // raw JSON, empty if none
}

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

// ExistsStationID reports whether at least one revision exists for stationID.
func (r *StationRevisionRepo) ExistsStationID(ctx context.Context, stationID string) (bool, error) {
	var n int
	if err := r.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM station_revisions WHERE station_id = ?`, stationID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// StationIDsForRevisions resolves a batch of revision IDs to their station
// IDs. Unknown revision IDs are silently omitted. Callers that need station
// identity for many revisions (e.g. the dispatcher's dispatch phase) should
// use this once per pass instead of one Get query per revision.
func (r *StationRevisionRepo) StationIDsForRevisions(ctx context.Context, revisionIDs []string) (map[string]string, error) {
	out := make(map[string]string, len(revisionIDs))
	if len(revisionIDs) == 0 {
		return out, nil
	}
	placeholders := strings.Repeat("?,", len(revisionIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(revisionIDs))
	for _, id := range revisionIDs {
		args = append(args, id)
	}
	rows, err := r.q.QueryContext(ctx,
		`SELECT revision_id, station_id FROM station_revisions WHERE revision_id IN (`+placeholders+`)`,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var revID, stationID string
		if err := rows.Scan(&revID, &stationID); err != nil {
			return nil, err
		}
		out[revID] = stationID
	}
	return out, rows.Err()
}

// ListCurrent returns one current operator-facing record per station_id.
func (r *StationRevisionRepo) ListCurrent(ctx context.Context) ([]*StationListRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT sr.station_id, sr.station_name,
		       COALESCE(sr.declared_downstream, ''),
		       COALESCE(sr.declared_inputs, ''),
		       COALESCE(sr.declared_outputs, '')
		FROM station_revisions sr
		WHERE NOT EXISTS (
			SELECT 1
			FROM station_revisions newer
			WHERE newer.station_id = sr.station_id
			  AND (
				newer.created_at > sr.created_at
				OR (newer.created_at = sr.created_at AND newer.revision_id > sr.revision_id)
			  )
		)
		ORDER BY sr.station_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*StationListRecord{}
	for rows.Next() {
		var rec StationListRecord
		if err := rows.Scan(&rec.StationID, &rec.StationName, &rec.DeclaredDownstream,
			&rec.DeclaredInputs, &rec.DeclaredOutputs); err != nil {
			return nil, err
		}
		out = append(out, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
