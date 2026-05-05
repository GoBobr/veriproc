package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ManifestRecord is the resolved input manifest header (Spec §4.3.9).
type ManifestRecord struct {
	ManifestID string
	RunID      string
	FrozenAt   time.Time
	Entries    []ManifestEntry
}

// ManifestEntry is one resolved input row.
type ManifestEntry struct {
	EntryID      string
	FileType     string
	Category     string
	Path         string
	Optional     bool
	Present      bool
	Size         int64
	Checksum     string
	ChecksumAlgo string
}

// ManifestRepo persists manifest headers + entries.
type ManifestRepo struct {
	q       querier
	dialect dialect
	store   *Store // for InTx atomic writes when called via Store.Manifests()
}

// Insert writes the header + entries inside one transaction. Manifest is
// frozen-by-construction: we have no Update path. Returns ErrConflict on
// (run_id) reuse (one manifest per run) or missing FK.
func (r *ManifestRepo) Insert(ctx context.Context, m *ManifestRecord) error {
	if m.FrozenAt.IsZero() {
		m.FrozenAt = nowUTC()
	}
	exec := func(q querier) error {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO resolved_input_manifests (manifest_id, run_id, frozen_at)
			VALUES (?, ?, ?)`, m.ManifestID, m.RunID, m.FrozenAt.UTC()); err != nil {
			if r.dialect.IsUniqueViolation(err) {
				return fmt.Errorf("%w: manifest already exists for run %s", ErrConflict, m.RunID)
			}
			if r.dialect.IsForeignKeyViolation(err) {
				return fmt.Errorf("%w: manifest references missing run", ErrConflict)
			}
			return err
		}
		for _, e := range m.Entries {
			if _, err := q.ExecContext(ctx, `
				INSERT INTO resolved_input_entries
					(entry_id, manifest_id, file_type, category, path,
					 optional, present, size, checksum, checksum_algo)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				e.EntryID, m.ManifestID, e.FileType, nullStr(e.Category), nullStr(e.Path),
				boolInt(e.Optional), boolInt(e.Present),
				nullInt(e.Size), nullStr(e.Checksum), nullStr(e.ChecksumAlgo)); err != nil {
				return err
			}
		}
		return nil
	}
	// If we're already inside a tx (querier is *sql.Tx), just execute.
	if _, ok := r.q.(*sql.Tx); ok {
		return exec(r.q)
	}
	if r.store == nil {
		return exec(r.q)
	}
	return r.store.InTx(ctx, func(tx *Tx) error { return exec(tx.tx) })
}

// GetByRun returns the manifest for a run, or ErrNotFound.
func (r *ManifestRepo) GetByRun(ctx context.Context, runID string) (*ManifestRecord, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT manifest_id, run_id, frozen_at FROM resolved_input_manifests WHERE run_id = ?`,
		runID)
	var m ManifestRecord
	if err := row.Scan(&m.ManifestID, &m.RunID, &m.FrozenAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	m.FrozenAt = m.FrozenAt.UTC()
	rows, err := r.q.QueryContext(ctx, `
		SELECT entry_id, file_type, COALESCE(category,''), COALESCE(path,''),
		       optional, present, COALESCE(size,0),
		       COALESCE(checksum,''), COALESCE(checksum_algo,'')
		FROM resolved_input_entries WHERE manifest_id = ? ORDER BY entry_id`, m.ManifestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e ManifestEntry
		var opt, pres int
		if err := rows.Scan(&e.EntryID, &e.FileType, &e.Category, &e.Path,
			&opt, &pres, &e.Size, &e.Checksum, &e.ChecksumAlgo); err != nil {
			return nil, err
		}
		e.Optional = opt != 0
		e.Present = pres != 0
		m.Entries = append(m.Entries, e)
	}
	return &m, rows.Err()
}

// FingerprintRecord is one processing fingerprint (Spec §4.3.10).
type FingerprintRecord struct {
	FingerprintID  string
	Value          string
	CanonicalRunID string
	CreatedAt      time.Time
}

// FingerprintRepo persists processing fingerprints.
type FingerprintRepo struct {
	q       querier
	dialect dialect
}

// Upsert stores a fingerprint value if not already present and returns the
// existing or newly created record.
func (r *FingerprintRepo) Upsert(ctx context.Context, value string) (*FingerprintRecord, error) {
	if rec, err := r.GetByValue(ctx, value); err == nil {
		return rec, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	rec := &FingerprintRecord{
		FingerprintID: "fp-" + sha12(value),
		Value:         value,
		CreatedAt:     nowUTC(),
	}
	if _, err := r.q.ExecContext(ctx,
		`INSERT INTO processing_fingerprints (fingerprint_id, value, canonical_run_id, created_at)
		 VALUES (?, ?, NULL, ?)`,
		rec.FingerprintID, rec.Value, rec.CreatedAt); err != nil {
		if r.dialect.IsUniqueViolation(err) {
			// Race: another tx inserted; re-read.
			return r.GetByValue(ctx, value)
		}
		return nil, err
	}
	return rec, nil
}

// GetByValue returns the fingerprint with the given value, or ErrNotFound.
func (r *FingerprintRepo) GetByValue(ctx context.Context, value string) (*FingerprintRecord, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT fingerprint_id, value, COALESCE(canonical_run_id,''), created_at
		 FROM processing_fingerprints WHERE value = ?`, value)
	var rec FingerprintRecord
	if err := row.Scan(&rec.FingerprintID, &rec.Value, &rec.CanonicalRunID, &rec.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rec.CreatedAt = rec.CreatedAt.UTC()
	return &rec, nil
}

// ClaimCanonical sets canonical_run_id atomically iff still NULL. Returns
// (true, nil) if this caller won the race; (false, nil) if another run
// already claimed canonicality.
func (r *FingerprintRepo) ClaimCanonical(ctx context.Context, fingerprintID, runID string) (bool, error) {
	res, err := r.q.ExecContext(ctx,
		`UPDATE processing_fingerprints SET canonical_run_id = ?
		 WHERE fingerprint_id = ? AND canonical_run_id IS NULL`,
		runID, fingerprintID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// SetCanonical unconditionally overwrites canonical_run_id (operator
// promotion, Spec §3.16 / §7.4.5). Returns the previous canonical run id
// (empty string if none) and an error. The previous value is returned so
// callers can build an audit record.
func (r *FingerprintRepo) SetCanonical(ctx context.Context, fingerprintID, runID string) (string, error) {
	row := r.q.QueryRowContext(ctx,
		`SELECT COALESCE(canonical_run_id, '') FROM processing_fingerprints
		 WHERE fingerprint_id = ?`, fingerprintID)
	var prev string
	if err := row.Scan(&prev); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if _, err := r.q.ExecContext(ctx,
		`UPDATE processing_fingerprints SET canonical_run_id = ?
		 WHERE fingerprint_id = ?`, runID, fingerprintID); err != nil {
		return "", err
	}
	return prev, nil
}

// nullInt returns sql.NullInt64 for nonzero values; 0 → NULL.
func nullInt(v int64) sql.NullInt64 {
	if v == 0 {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: v, Valid: true}
}
