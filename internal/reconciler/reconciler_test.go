package reconciler_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/reconciler"
	"github.com/eum/veriproc/internal/runs"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

// helper: build a fully wired runs.Service over a fresh sqlite store with a
// stub executor and one seeded station. Returns the service and the seeded
// station id and a clock-controlled ticker.
type harness struct {
	store *store.Store
	runs  *runs.Service
	now   func() time.Time
}

func newHarness(t *testing.T, clock func() time.Time) *harness {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "rec.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(context.Background(), st, stations.Spec{
		StationID: "STA", StationName: "P", ContentHash: "sha256:x",
		SchemaVersion: "veriproc.station/v1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	exec := executor.NewStubExecutor(clock)
	rsvc := runs.NewService(runs.Config{
		Store: st, Executor: exec, Resolver: reg,
		WorkingRootBase: t.TempDir(),
		Clock:           clock,
	})
	return &harness{store: st, runs: rsvc, now: clock}
}

// TestReconciler_StampsAndClearsMarker_M7 — a stale dispatched run gets the
// reconciliation_started_at marker stamped during reconcileOne and cleared
// by the time Tick returns. Spec §3.13 / §7.8.
func TestReconciler_StampsAndClearsMarker_M7(t *testing.T) {
	now := time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, func() time.Time { return now })

	ctx := context.Background()
	// Insert a task and prepare+dispatch a run; do NOT poll, so its job
	// has only a submitted_at and no last_observed_at.
	tk := &store.TaskRecord{
		TaskID: "task-rec", DestinationStationID: "STA",
		WindowStart: now, WindowEnd: now.Add(time.Minute),
		State: "accepted", CreatedAt: now,
	}
	if err := h.store.Tasks().Insert(ctx, tk); err != nil {
		t.Fatalf("task insert: %v", err)
	}
	r, err := h.runs.PrepareRun(ctx, tk.TaskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := h.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Advance clock past the stale threshold and tick.
	now = now.Add(2 * time.Minute)
	rec := reconciler.New(reconciler.Config{
		Store: h.store, Runs: h.runs,
		Clock: func() time.Time { return now },
		StaleThreshold: 30 * time.Second,
		Interval:       time.Hour,
		Logger:         zerolog.Nop(),
	})
	n, err := rec.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if n == 0 {
		t.Fatalf("expected at least one stale run, got 0")
	}

	// Marker must be cleared after the pass.
	got, _ := h.store.Runs().Get(ctx, r.RunID)
	if got.ReconciliationStartedAt.Valid {
		t.Errorf("reconciliation marker still set after Tick")
	}
}

// TestReconciler_NotStaleSkipped_M7 — runs whose last_observed_at is fresh
// must not be touched by the reconciler.
func TestReconciler_NotStaleSkipped_M7(t *testing.T) {
	now := time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, func() time.Time { return now })

	ctx := context.Background()
	tk := &store.TaskRecord{
		TaskID: "task-fresh", DestinationStationID: "STA",
		WindowStart: now, WindowEnd: now.Add(time.Minute),
		State: "accepted", CreatedAt: now,
	}
	if err := h.store.Tasks().Insert(ctx, tk); err != nil {
		t.Fatalf("task insert: %v", err)
	}
	r, _ := h.runs.PrepareRun(ctx, tk.TaskID)
	if _, err := h.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Poll once to update last_observed_at.
	if _, err := h.runs.Poll(ctx, r.RunID); err != nil {
		t.Fatalf("poll: %v", err)
	}

	rec := reconciler.New(reconciler.Config{
		Store: h.store, Runs: h.runs,
		Clock: func() time.Time { return now },
		StaleThreshold: 5 * time.Minute,
		Logger:         zerolog.Nop(),
	})
	n, _ := rec.Tick(ctx)
	if n != 0 {
		t.Errorf("expected 0 reconciled runs, got %d", n)
	}
}
