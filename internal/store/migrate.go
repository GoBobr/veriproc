package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies any pending embedded migrations against the supplied store.
//
// The migrator records applied migrations in a `schema_migrations` table.
// Migrations are identified by the file name (e.g. `0001_init.sql`) and
// applied in lexicographic order. This is intentionally minimal; goose or
// atlas can be substituted in a later milestone if needs grow.
func Migrate(ctx context.Context, s *Store) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMP NOT NULL
		)`); err != nil {
		return fmt.Errorf("migrate: create tracking table: %w", err)
	}

	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("migrate: list embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		applied, err := isApplied(ctx, s.db, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(migrationsFS, "migrations/"+name)
		if err != nil {
			return fmt.Errorf("migrate: read %s: %w", name, err)
		}
		if err := applyOne(ctx, s.db, name, string(body)); err != nil {
			return fmt.Errorf("migrate: apply %s: %w", name, err)
		}
	}
	return nil
}

func isApplied(ctx context.Context, db *sql.DB, version string) (bool, error) {
	var v string
	err := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = ?`, version).Scan(&v)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, err
	default:
		return true, nil
	}
}

func applyOne(ctx context.Context, db *sql.DB, version, body string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck

	// SQLite does not support multiple statements in a single Exec via the
	// modernc driver in all configurations; split on `;` boundaries naively
	// (good enough for our hand-authored DDL).
	for _, stmt := range splitSQL(body) {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %q: %w", firstLine(stmt), err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		version, nowUTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// splitSQL splits a string on top-level `;` while ignoring `--` line comments.
// Sufficient for our migration files; not a general SQL parser.
func splitSQL(s string) []string {
	var out []string
	var buf strings.Builder
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "--") || trim == "" {
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
		if strings.HasSuffix(trim, ";") {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	if strings.TrimSpace(buf.String()) != "" {
		out = append(out, buf.String())
	}
	return out
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
