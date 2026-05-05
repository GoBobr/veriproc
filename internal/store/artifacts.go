package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// ArtifactRecord is the persisted shape of an artifact (Spec §4.3 / §5.5.6).
type ArtifactRecord struct {
	ArtifactID       string
	ProducingRunID   string
	LogicalType      string // output | joborder | log | manifest_export | task_out
	FileType         string
	Path             string
	Size             int64
	Checksum         string
	ChecksumAlgo     string
	ValidationStatus string
	Availability     string // available | missing | unknown
	CreatedAt        time.Time
}

// ArtifactRepo persists artifact metadata.
type ArtifactRepo struct {
	q       querier
	dialect dialect
}

// Insert creates an artifact metadata row.
func (r *ArtifactRepo) Insert(ctx context.Context, a *ArtifactRecord) error {
	if a.CreatedAt.IsZero() {
		a.CreatedAt = nowUTC()
	}
	if a.Availability == "" {
		a.Availability = "available"
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO artifacts
			(artifact_id, producing_run_id, logical_type, file_type, path,
			 size, checksum, checksum_algo, validation_status, availability, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ArtifactID, nullStr(a.ProducingRunID), a.LogicalType,
		nullStr(a.FileType), nullStr(a.Path),
		nullInt(a.Size), nullStr(a.Checksum), nullStr(a.ChecksumAlgo),
		nullStr(a.ValidationStatus), a.Availability, a.CreatedAt.UTC())
	if err != nil {
		if r.dialect.IsForeignKeyViolation(err) {
			return fmt.Errorf("%w: artifact references missing run", ErrConflict)
		}
		return err
	}
	return nil
}

// Get returns an artifact by id.
func (r *ArtifactRepo) Get(ctx context.Context, artifactID string) (*ArtifactRecord, error) {
	row := r.q.QueryRowContext(ctx, artifactSelect+` WHERE artifact_id = ?`, artifactID)
	return scanArtifact(row)
}

// ListByRun returns all artifacts produced by a run.
func (r *ArtifactRepo) ListByRun(ctx context.Context, runID, logicalType string) ([]*ArtifactRecord, error) {
	q := artifactSelect + ` WHERE producing_run_id = ?`
	args := []any{runID}
	if logicalType != "" {
		q += ` AND logical_type = ?`
		args = append(args, logicalType)
	}
	q += ` ORDER BY created_at ASC, artifact_id ASC`
	rows, err := r.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ArtifactRecord
	for rows.Next() {
		a, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

const artifactSelect = `SELECT artifact_id, COALESCE(producing_run_id,''), logical_type,
	COALESCE(file_type,''), COALESCE(path,''),
	COALESCE(size, 0), COALESCE(checksum,''), COALESCE(checksum_algo,''),
	COALESCE(validation_status,''), availability, created_at
FROM artifacts`

func scanArtifact(s scanner) (*ArtifactRecord, error) {
	var a ArtifactRecord
	if err := s.Scan(&a.ArtifactID, &a.ProducingRunID, &a.LogicalType,
		&a.FileType, &a.Path, &a.Size, &a.Checksum, &a.ChecksumAlgo,
		&a.ValidationStatus, &a.Availability, &a.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	a.CreatedAt = a.CreatedAt.UTC()
	return &a, nil
}

// sha12 returns the hex of a short SHA-256 prefix; used to derive deterministic
// surrogate ids from variable-length values.
func sha12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}
