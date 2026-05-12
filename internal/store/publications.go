package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// PublicationState constants for rolling_archive_publications.publication_state.
const (
	PublicationStatePending   = "pending"
	PublicationStatePublished = "published"
	PublicationStateFailed    = "failed"
)

// PublicationRecord persists one rolling-archive publication request.
// Spec §4.3.12 / §4.6.6.
type PublicationRecord struct {
	PublicationID    string
	ArtifactID       string
	ProducingRunID   string
	ArchiveID        string
	TargetPath       string
	ObjectKind       string
	PublicationMode  string
	PublicationState string
	Size             int64
	Checksum         string
	ChecksumAlgo     string
	ChecksumSource   string
	FailureReason    string
	CreatedAt        time.Time
	PublishedAt      sql.NullTime
}

// PublicationRepo persists publications.
type PublicationRepo struct {
	q       querier
	dialect dialect
}

// Insert creates a new publication row in pending state. Returns ErrConflict
// on duplicate (artifact, archive, target_path).
func (r *PublicationRepo) Insert(ctx context.Context, p *PublicationRecord) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = nowUTC()
	}
	if p.PublicationState == "" {
		p.PublicationState = PublicationStatePending
	}
	if p.PublicationMode == "" {
		p.PublicationMode = "copy"
	}
	if p.ObjectKind == "" {
		p.ObjectKind = ObjectKindRegularFile
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO rolling_archive_publications
			(publication_id, artifact_id, producing_run_id, archive_id, target_path,
			 object_kind, publication_mode, publication_state, size, checksum, checksum_algo, checksum_source,
			 failure_reason, created_at, published_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PublicationID, p.ArtifactID, nullStr(p.ProducingRunID), p.ArchiveID, p.TargetPath,
		p.ObjectKind, p.PublicationMode, p.PublicationState, nullInt(p.Size), nullStr(p.Checksum),
		nullStr(p.ChecksumAlgo), nullStr(p.ChecksumSource), nullStr(p.FailureReason),
		p.CreatedAt.UTC(), nullTime(p.PublishedAt))
	if err != nil {
		if r.dialect.IsUniqueViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// MarkPublished records that a publication has succeeded.
func (r *PublicationRepo) MarkPublished(ctx context.Context, id string, at time.Time) error {
	res, err := r.q.ExecContext(ctx, `
		UPDATE rolling_archive_publications
		SET publication_state = 'published', published_at = ?, failure_reason = NULL
		WHERE publication_id = ? AND publication_state = 'pending'`,
		at.UTC(), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkFailed records that a publication attempt has failed (terminal).
func (r *PublicationRepo) MarkFailed(ctx context.Context, id, reason string) error {
	_, err := r.q.ExecContext(ctx, `
		UPDATE rolling_archive_publications
		SET publication_state = 'failed', failure_reason = ?
		WHERE publication_id = ?`, reason, id)
	return err
}

// ListPending returns up to limit publications still pending. Used by the
// publisher background worker.
func (r *PublicationRepo) ListPending(ctx context.Context, limit int) ([]*PublicationRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.q.QueryContext(ctx, `
		SELECT publication_id, artifact_id, COALESCE(producing_run_id, ''), archive_id,
		       target_path, COALESCE(object_kind,'regular_file'), publication_mode, publication_state,
		       COALESCE(size, 0), COALESCE(checksum, ''), COALESCE(checksum_algo, ''), COALESCE(checksum_source, ''),
		       COALESCE(failure_reason, ''), created_at, published_at
		FROM rolling_archive_publications
		WHERE publication_state = 'pending'
		ORDER BY created_at ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPublications(rows)
}

// ListByRun returns all publications produced by a run.
func (r *PublicationRepo) ListByRun(ctx context.Context, runID string) ([]*PublicationRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT publication_id, artifact_id, COALESCE(producing_run_id, ''), archive_id,
		       target_path, COALESCE(object_kind,'regular_file'), publication_mode, publication_state,
		       COALESCE(size, 0), COALESCE(checksum, ''), COALESCE(checksum_algo, ''), COALESCE(checksum_source, ''),
		       COALESCE(failure_reason, ''), created_at, published_at
		FROM rolling_archive_publications
		WHERE producing_run_id = ?
		ORDER BY created_at ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPublications(rows)
}

// ListByArtifact returns all publications referencing artifactID.
func (r *PublicationRepo) ListByArtifact(ctx context.Context, artifactID string) ([]*PublicationRecord, error) {
	rows, err := r.q.QueryContext(ctx, `
		SELECT publication_id, artifact_id, COALESCE(producing_run_id, ''), archive_id,
		       target_path, COALESCE(object_kind,'regular_file'), publication_mode, publication_state,
		       COALESCE(size, 0), COALESCE(checksum, ''), COALESCE(checksum_algo, ''), COALESCE(checksum_source, ''),
		       COALESCE(failure_reason, ''), created_at, published_at
		FROM rolling_archive_publications
		WHERE artifact_id = ?
		ORDER BY created_at ASC`, artifactID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPublications(rows)
}

// Get returns a single publication by id.
func (r *PublicationRepo) Get(ctx context.Context, id string) (*PublicationRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT publication_id, artifact_id, COALESCE(producing_run_id, ''), archive_id,
		       target_path, COALESCE(object_kind,'regular_file'), publication_mode, publication_state,
		       COALESCE(size, 0), COALESCE(checksum, ''), COALESCE(checksum_algo, ''), COALESCE(checksum_source, ''),
		       COALESCE(failure_reason, ''), created_at, published_at
		FROM rolling_archive_publications WHERE publication_id = ?`, id)
	var p PublicationRecord
	if err := row.Scan(&p.PublicationID, &p.ArtifactID, &p.ProducingRunID, &p.ArchiveID,
		&p.TargetPath, &p.ObjectKind, &p.PublicationMode, &p.PublicationState,
		&p.Size, &p.Checksum, &p.ChecksumAlgo, &p.ChecksumSource,
		&p.FailureReason, &p.CreatedAt, &p.PublishedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.CreatedAt = p.CreatedAt.UTC()
	return &p, nil
}

func scanPublications(rows *sql.Rows) ([]*PublicationRecord, error) {
	var out []*PublicationRecord
	for rows.Next() {
		var p PublicationRecord
		if err := rows.Scan(&p.PublicationID, &p.ArtifactID, &p.ProducingRunID, &p.ArchiveID,
			&p.TargetPath, &p.ObjectKind, &p.PublicationMode, &p.PublicationState,
			&p.Size, &p.Checksum, &p.ChecksumAlgo, &p.ChecksumSource,
			&p.FailureReason, &p.CreatedAt, &p.PublishedAt); err != nil {
			return nil, err
		}
		p.CreatedAt = p.CreatedAt.UTC()
		out = append(out, &p)
	}
	return out, rows.Err()
}
