// Package store provides the persistence layer for VeriProc control-plane
// state.
//
// The package exposes a database-agnostic Store facade plus repository types
// for each aggregate. The current implementation targets SQLite (via the
// pure-Go modernc.org/sqlite driver). A Postgres adapter is planned for a
// later milestone; see docs/milestones for status.
//
// Spec references:
//   - Section 4.2 (persistence design principles)
//   - Section 4.3 (core tables)
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrNotFound is returned when a requested record does not exist.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned when a uniqueness or referential constraint is
// violated in a way the caller should surface as a domain conflict
// (e.g. duplicate task id, idempotency-key reuse with conflicting payload).
var ErrConflict = errors.New("store: conflict")

// DSN scheme prefixes recognized by Open.
const (
	schemeSQLite = "sqlite://"
	// schemePostgres is reserved for a future Postgres adapter.
	schemePostgres = "postgres://"
)

// Store is the top-level persistence handle. It owns the *sql.DB and exposes
// repositories scoped to either the underlying connection pool or a
// transaction.
type Store struct {
	db      *sql.DB
	dialect dialect
}

// Open opens a Store from a DSN. Supported schemes:
//
//   - sqlite://path/to/file.db  (use sqlite://:memory: for an in-memory DB)
//
// The Postgres scheme is recognized but currently returns an error; the
// adapter is tracked as a deferred feature (see M0–M2 report).
func Open(dsn string) (*Store, error) {
	switch {
	case strings.HasPrefix(dsn, schemeSQLite):
		path := strings.TrimPrefix(dsn, schemeSQLite)
		// modernc.org/sqlite uses driver name "sqlite" and accepts a plain path.
		// Enable foreign keys and WAL for sane defaults.
		conn := path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
		db, err := sql.Open("sqlite", conn)
		if err != nil {
			return nil, fmt.Errorf("store: open sqlite: %w", err)
		}
		// SQLite tolerates exactly one writer; serialize to avoid SQLITE_BUSY
		// in concurrent tests.
		db.SetMaxOpenConns(1)
		if err := db.PingContext(context.Background()); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: ping sqlite: %w", err)
		}
		return &Store{db: db, dialect: dialectSQLite{}}, nil
	case strings.HasPrefix(dsn, schemePostgres):
		return nil, fmt.Errorf("store: postgres adapter not yet implemented (deferred)")
	default:
		return nil, fmt.Errorf("store: unrecognized DSN scheme in %q", dsn)
	}
}

// Close releases the underlying database resources.
func (s *Store) Close() error { return s.db.Close() }

// Ping verifies that the underlying connection is alive. Used by the health
// aggregator's database checker.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// DB returns the raw *sql.DB. Reserved for the migrator and tests; production
// code should use the repository methods.
func (s *Store) DB() *sql.DB { return s.db }

// Tasks returns the task repository bound to the connection pool.
func (s *Store) Tasks() *TaskRepo { return &TaskRepo{q: s.db, dialect: s.dialect} }

// Runs returns the run repository bound to the connection pool.
func (s *Store) Runs() *RunRepo { return &RunRepo{q: s.db, dialect: s.dialect} }

// Jobs returns the job repository bound to the connection pool.
func (s *Store) Jobs() *JobRepo { return &JobRepo{q: s.db, dialect: s.dialect} }

// Manifests returns the manifest repository bound to the connection pool.
func (s *Store) Manifests() *ManifestRepo {
	return &ManifestRepo{q: s.db, dialect: s.dialect, store: s}
}

// Fingerprints returns the processing-fingerprint repository.
func (s *Store) Fingerprints() *FingerprintRepo {
	return &FingerprintRepo{q: s.db, dialect: s.dialect}
}

// Artifacts returns the artifact-metadata repository.
func (s *Store) Artifacts() *ArtifactRepo {
	return &ArtifactRepo{q: s.db, dialect: s.dialect}
}

// Stations returns the station-revision repository.
func (s *Store) Stations() *StationRevisionRepo {
	return &StationRevisionRepo{q: s.db, dialect: s.dialect}
}

// Idempotency returns the idempotency-record repository.
func (s *Store) Idempotency() *IdempotencyRepo {
	return &IdempotencyRepo{q: s.db, dialect: s.dialect}
}

// Canonicality returns the canonicality-audit repository (M6).
func (s *Store) Canonicality() *CanonicalityRepo {
	return &CanonicalityRepo{q: s.db, dialect: s.dialect}
}

// Publications returns the rolling-archive-publication repository (M6).
func (s *Store) Publications() *PublicationRepo {
	return &PublicationRepo{q: s.db, dialect: s.dialect}
}

// InTx runs fn inside a transaction. The transaction is committed if fn
// returns nil and rolled back otherwise. Tx-scoped repositories are passed via
// a dedicated Tx handle.
func (s *Store) InTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	t := &Tx{tx: tx, dialect: s.dialect}
	if err := fn(t); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Tx is a transaction handle that exposes the same repository methods as
// Store, scoped to the active transaction.
type Tx struct {
	tx      *sql.Tx
	dialect dialect
}

// Tasks returns the task repository bound to the transaction.
func (t *Tx) Tasks() *TaskRepo { return &TaskRepo{q: t.tx, dialect: t.dialect} }

// Runs returns the run repository bound to the transaction.
func (t *Tx) Runs() *RunRepo { return &RunRepo{q: t.tx, dialect: t.dialect} }

// Stations returns the station-revision repository.
func (t *Tx) Stations() *StationRevisionRepo {
	return &StationRevisionRepo{q: t.tx, dialect: t.dialect}
}

// Idempotency returns the idempotency-record repository.
func (t *Tx) Idempotency() *IdempotencyRepo {
	return &IdempotencyRepo{q: t.tx, dialect: t.dialect}
}

// Jobs returns the job repository bound to the transaction.
func (t *Tx) Jobs() *JobRepo { return &JobRepo{q: t.tx, dialect: t.dialect} }

// Manifests returns the manifest repository bound to the transaction.
func (t *Tx) Manifests() *ManifestRepo { return &ManifestRepo{q: t.tx, dialect: t.dialect} }

// Fingerprints returns the fingerprint repository bound to the transaction.
func (t *Tx) Fingerprints() *FingerprintRepo { return &FingerprintRepo{q: t.tx, dialect: t.dialect} }

// Artifacts returns the artifact repository bound to the transaction.
func (t *Tx) Artifacts() *ArtifactRepo { return &ArtifactRepo{q: t.tx, dialect: t.dialect} }

// Canonicality returns the canonicality-audit repository bound to the transaction.
func (t *Tx) Canonicality() *CanonicalityRepo {
	return &CanonicalityRepo{q: t.tx, dialect: t.dialect}
}

// Publications returns the publication repository bound to the transaction.
func (t *Tx) Publications() *PublicationRepo {
	return &PublicationRepo{q: t.tx, dialect: t.dialect}
}

// querier is the small subset of sql.DB / sql.Tx used by repositories so that
// repository methods can run in either pooled or transactional context.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// dialect abstracts the small differences between SQL dialects we need to
// paper over (currently only error classification). When a Postgres adapter
// is added it will live here.
type dialect interface {
	IsUniqueViolation(err error) bool
	IsForeignKeyViolation(err error) bool
}

type dialectSQLite struct{}

func (dialectSQLite) IsUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// modernc.org/sqlite returns errors whose Error() contains "UNIQUE constraint failed".
	return strings.Contains(err.Error(), "UNIQUE constraint failed") ||
		strings.Contains(err.Error(), "constraint failed: UNIQUE")
}

func (dialectSQLite) IsForeignKeyViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

// nowUTC returns the current time normalized to UTC. Centralized so test
// fixtures can swap if needed in later milestones.
func nowUTC() time.Time { return time.Now().UTC() }

// boolInt returns 1 if b is true, 0 otherwise. SQLite stores booleans as INTs.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
