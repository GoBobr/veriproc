package store

import (
	"context"
	"testing"
)

// TestStationRetirement_HiddenFromListings — a retired station disappears
// from ListCurrent and ExistsStationID while its revision rows (and run FK
// targets) remain intact (Spec: Station Retirement Semantics — "Retired
// station hidden from listings").
func TestStationRetirement_HiddenFromListings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mkStation(t, s, "oldname")
	mkStation(t, s, "newname")

	// Historical run pointing at the retired station's revision.
	revID := mkStation(t, s, "legacy")
	task := mkTask("task-retire-1")
	if err := s.Tasks().Insert(ctx, task); err != nil {
		t.Fatalf("task insert: %v", err)
	}
	run := &RunRecord{
		RunID:             "run-1",
		TaskID:            task.TaskID,
		StationRevisionID: revID,
		RetryIndex:        0,
		WorkingRoot:       "/tmp/wr-run-1",
		State:             "succeeded",
		Canonicality:      "canonical",
	}
	if err := s.Runs().Insert(ctx, run); err != nil {
		t.Fatalf("run insert: %v", err)
	}

	changed, err := s.Stations().RetireStation(ctx, "oldname")
	if err != nil {
		t.Fatalf("retire: %v", err)
	}
	if !changed {
		t.Fatal("retire should report change on first call")
	}

	current, err := s.Stations().ListCurrent(ctx)
	if err != nil {
		t.Fatalf("list current: %v", err)
	}
	found := map[string]bool{}
	for _, rec := range current {
		found[rec.StationID] = true
	}
	if found["oldname"] {
		t.Error("retired station still listed in ListCurrent")
	}
	if !found["newname"] || !found["legacy"] {
		t.Error("live stations missing from ListCurrent")
	}

	exists, err := s.Stations().ExistsStationID(ctx, "oldname")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}
	if exists {
		t.Error("retired station still reported by ExistsStationID")
	}

	// Revision row survives so the run FK stays satisfied.
	if _, err := s.Stations().Get(ctx, "rev-oldname"); err != nil {
		t.Errorf("revision row of retired station must remain: %v", err)
	}

	// Retirement is idempotent.
	changed, err = s.Stations().RetireStation(ctx, "oldname")
	if err != nil {
		t.Fatalf("re-retire: %v", err)
	}
	if changed {
		t.Error("re-retire should be a no-op")
	}

	// Unknown station retire is a no-op, not an error.
	changed, err = s.Stations().RetireStation(ctx, "nosuch")
	if err != nil {
		t.Fatalf("retire unknown: %v", err)
	}
	if changed {
		t.Error("retire unknown station should report no change")
	}
}

// TestStationRetirement_ListStationIDs — includeRetired toggles retired
// stations in the listing (Spec: Station Retirement Semantics /
// Station Prune CLI discovery support).
func TestStationRetirement_ListStationIDs(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mkStation(t, s, "live")
	mkStation(t, s, "ghost")

	if _, err := s.Stations().RetireStation(ctx, "ghost"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	active, err := s.Stations().ListStationIDs(ctx, false)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 || active[0] != "live" {
		t.Errorf("active ids = %v, want [live]", active)
	}

	all, err := s.Stations().ListStationIDs(ctx, true)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("all ids = %v, want 2 entries", all)
	}
}

// TestStationRetirement_UnretireStation — clearing the marker restores the
// station to listings (folder reappears / Seed path).
func TestStationRetirement_UnretireStation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mkStation(t, s, "back")
	if _, err := s.Stations().RetireStation(ctx, "back"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := s.Stations().UnretireStation(ctx, "back"); err != nil {
		t.Fatalf("unretire: %v", err)
	}

	current, err := s.Stations().ListCurrent(ctx)
	if err != nil {
		t.Fatalf("list current: %v", err)
	}
	if len(current) != 1 || current[0].StationID != "back" {
		t.Errorf("unretired station not listed: %+v", current)
	}
	exists, err := s.Stations().ExistsStationID(ctx, "back")
	if err != nil || !exists {
		t.Errorf("exists after unretire = %v, %v", exists, err)
	}
}
