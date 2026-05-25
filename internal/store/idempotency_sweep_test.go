package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestIdempotency_TTLSweep_3_3 — Sweep deletes records strictly older than
// the cutoff and reports the count; non-expired and NULL-expiry records are
// retained. Spec §3.3 retention.
func TestIdempotency_TTLSweep_3_3(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "idem.db")
	s, err := Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := Migrate(context.Background(), s); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, key string, expiresAt time.Time) {
		rec := &IdempotencyRecord{
			ID: id, Scope: "tasks.submit", Key: key, RequestHash: "h-" + id, CreatedAt: now,
		}
		if !expiresAt.IsZero() {
			rec.ExpiresAt.Time = expiresAt.UTC()
			rec.ExpiresAt.Valid = true
		}
		if err := s.Idempotency().Insert(ctx, rec); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	mk("ir-old", "k1", now.Add(-time.Hour))    // expired
	mk("ir-future", "k2", now.Add(time.Hour))  // alive
	mk("ir-null", "k3", time.Time{})           // NULL expiry → retained
	mk("ir-edge", "k4", now.Add(-time.Minute)) // also expired

	n, err := s.Idempotency().Sweep(ctx, now)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Errorf("sweep deleted %d, want 2", n)
	}
	if _, err := s.Idempotency().GetByKey(ctx, "tasks.submit", "k1"); err == nil {
		t.Error("expired record still retrievable after sweep")
	}
	if _, err := s.Idempotency().GetByKey(ctx, "tasks.submit", "k2"); err != nil {
		t.Errorf("future record missing: %v", err)
	}
	if _, err := s.Idempotency().GetByKey(ctx, "tasks.submit", "k3"); err != nil {
		t.Errorf("null-expiry record missing: %v", err)
	}
}

// TestIdempotency_SweepDeterministic — re-running Sweep with no expired
// rows returns 0 and is a no-op.
func TestIdempotency_SweepDeterministic(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "idem.db")
	s, err := Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := Migrate(context.Background(), s); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	rec := &IdempotencyRecord{
		ID: "ir-z", Scope: "tasks.submit", Key: "kz", RequestHash: "h", CreatedAt: now,
	}
	rec.ExpiresAt.Time = now.Add(time.Hour)
	rec.ExpiresAt.Valid = true
	if err := s.Idempotency().Insert(ctx, rec); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n, err := s.Idempotency().Sweep(ctx, now); err != nil || n != 0 {
		t.Errorf("first sweep n=%d err=%v, want 0,nil", n, err)
	}
	if n, err := s.Idempotency().Sweep(ctx, now); err != nil || n != 0 {
		t.Errorf("second sweep n=%d err=%v, want 0,nil", n, err)
	}
}
