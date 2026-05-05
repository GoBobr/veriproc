package groups_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/eum/veriproc/internal/groups"
	"github.com/eum/veriproc/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "g.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Seed a single station revision so RunRecord FK constraints are satisfied.
	if err := st.Stations().Insert(context.Background(), &store.StationRevisionRecord{
		RevisionID: "rev", StationID: "S",
		ContentHash: "sha256:x", SchemaVersion: "veriproc.station/v1",
	}); err != nil {
		t.Fatalf("seed station: %v", err)
	}
	return st
}

func mkRun(t *testing.T, st *store.Store, runID, state, canon string) {
	t.Helper()
	ctx := context.Background()
	taskID := "task-" + runID
	if err := st.Tasks().Insert(ctx, &store.TaskRecord{
		TaskID: taskID, DestinationStationID: "S",
		WindowStart: time.Now().UTC(), WindowEnd: time.Now().UTC(),
		State: "accepted", CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("task: %v", err)
	}
	if err := st.Runs().Insert(ctx, &store.RunRecord{
		RunID: runID, TaskID: taskID, StationRevisionID: "rev",
		WorkingRoot: "/tmp/" + runID, State: state, Canonicality: canon,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// TestGroups_RegisterAndAggregate_M7 — RegisterRun creates the group on first
// reference, AddMember is idempotent, and Aggregate produces the expected
// derived state from member runs.
func TestGroups_RegisterAndAggregate_M7(t *testing.T) {
	st := newStore(t)
	svc := groups.NewService(st, nil)
	ctx := context.Background()

	// Two complete runs, one canonical and one duplicate → "complete".
	mkRun(t, st, "run-a", "complete", "canonical")
	mkRun(t, st, "run-b", "complete", "duplicate")
	if err := svc.RegisterRun(ctx, "g-1", "run-a", "task-run-a", ""); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if err := svc.RegisterRun(ctx, "g-1", "run-b", "task-run-b", ""); err != nil {
		t.Fatalf("register b: %v", err)
	}
	// Idempotent re-registration must not fail.
	if err := svc.RegisterRun(ctx, "g-1", "run-a", "task-run-a", ""); err != nil {
		t.Fatalf("re-register a: %v", err)
	}

	g, err := svc.Close(ctx, "g-1")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if g.State != store.SplitGroupStateComplete {
		t.Errorf("state = %s, want complete", g.State)
	}
	if g.CanonicalCount != 1 {
		t.Errorf("canonical_count = %d, want 1", g.CanonicalCount)
	}
	if len(g.Members) != 2 {
		t.Errorf("member_count = %d, want 2", len(g.Members))
	}
}

// TestGroups_FailedDerivation_M7 — all-failed members → group state "failed".
func TestGroups_FailedDerivation_M7(t *testing.T) {
	st := newStore(t)
	svc := groups.NewService(st, nil)
	ctx := context.Background()

	mkRun(t, st, "run-x", "failed", "pending")
	mkRun(t, st, "run-y", "failed", "pending")
	_ = svc.RegisterRun(ctx, "g-2", "run-x", "task-run-x", "")
	_ = svc.RegisterRun(ctx, "g-2", "run-y", "task-run-y", "")
	g, err := svc.Close(ctx, "g-2")
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if g.State != store.SplitGroupStateFailed {
		t.Errorf("state = %s, want failed", g.State)
	}
}

// TestGroups_OpenWithActiveMembers_M7 — Aggregate on an open group with at
// least one non-terminal member keeps the state "open".
func TestGroups_OpenWithActiveMembers_M7(t *testing.T) {
	st := newStore(t)
	svc := groups.NewService(st, nil)
	ctx := context.Background()
	mkRun(t, st, "run-r", "running", "pending")
	_ = svc.RegisterRun(ctx, "g-3", "run-r", "task-run-r", "")
	derived, err := svc.Aggregate(ctx, "g-3")
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if derived != store.SplitGroupStateOpen {
		t.Errorf("derived = %s, want open", derived)
	}
}
