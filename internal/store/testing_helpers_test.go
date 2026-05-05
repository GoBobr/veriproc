// Package storetest contains shared test helpers for the store package.
package store

import (
	"context"
	"path/filepath"
	"testing"
)

// newTestStore opens an isolated SQLite database in a per-test temp dir,
// applies all migrations, and returns it. Tests should not share databases.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "veriproc.db")
	s, err := Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := Migrate(context.Background(), s); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}
