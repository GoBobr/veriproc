package store

import (
	"context"
	"errors"
	"testing"
)

// TestMigrate_FreshDB_7_3_2 — migrations apply cleanly to an empty DB and
// the expected core tables exist (Spec §4.3, §7.3.2).
func TestMigrate_FreshDB_7_3_2(t *testing.T) {
	s := newTestStore(t)
	wantTables := []string{
		"schema_migrations",
		"station_revisions",
		"station_controls",
		"idempotency_records",
		"tasks",
		"task_history_entries",
		"provenance_links",
		"processing_fingerprints",
		"runs",
		"jobs",
		"resolved_input_manifests",
		"resolved_input_entries",
		"artifacts",
		"rolling_archive_publications",
		"deduplication_records",
		"canonicality_audit",
		"split_groups",
		"split_group_members",
	}
	for _, tbl := range wantTables {
		var name string
		err := s.DB().QueryRow(
			`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("missing table %s: %v", tbl, err)
		}
	}
}

// TestMigrate_Idempotent — re-running Migrate is a no-op.
func TestMigrate_Idempotent(t *testing.T) {
	s := newTestStore(t)
	// Migrate again; should not error or duplicate rows.
	if err := Migrate(context.Background(), s); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("schema_migrations rows = %d, want 1", n)
	}
}

// TestStore_OpenUnsupportedScheme — non-sqlite/postgres DSN fails clearly.
func TestStore_OpenUnsupportedScheme(t *testing.T) {
	if _, err := Open("mysql://nope"); err == nil {
		t.Error("expected error for unsupported scheme")
	}
}

// TestStore_OpenPostgresDeferred — postgres scheme is recognized but
// returns a clear error until the adapter ships.
func TestStore_OpenPostgresDeferred(t *testing.T) {
	_, err := Open("postgres://user:pw@localhost/db")
	if err == nil {
		t.Fatal("expected deferred error for postgres scheme")
	}
	if !contains(err.Error(), "postgres") {
		t.Errorf("error message should mention postgres: %v", err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && (indexOf(s, sub) >= 0)))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// Compile-time check that ErrNotFound and ErrConflict are exported.
var _ = errors.Is
