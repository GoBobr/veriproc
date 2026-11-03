package stations_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
)

// newSummaryTestStore opens a migrated store seeded with one registered
// station and returns a Service with a fixed clock.
func newSummaryTestStore(t *testing.T, now time.Time) (*store.Store, *stations.Service) {
	t.Helper()
	dbPath := t.TempDir() + "/summary.db"
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(context.Background(), st,
		stations.Spec{StationID: "SCE-1", StationName: "Scene One",
			ContentHash: "sha256:sce1", SchemaVersion: "veriproc.station/v1"},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	svc := stations.NewService(st, func() time.Time { return now })
	svc.SetConfigSource(reg, reg, nil, nil)
	return st, svc
}

// insertCompletedRun seeds a task and a complete run whose terminal_at is
// the given time, returning the task ID.
func insertCompletedRun(t *testing.T, st *store.Store, taskID string, terminal time.Time) string {
	t.Helper()
	ctx := context.Background()
	if err := st.Tasks().Insert(ctx, &store.TaskRecord{
		TaskID: taskID, SchemaVersion: "veriproc.task/v1",
		DestinationStationID: "SCE-1",
		WindowStart:          terminal.Add(-time.Hour), WindowEnd: terminal,
		State:                "complete",
		LatestRetryIndex:     sql.NullInt64{Int64: 0, Valid: true},
	}); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if err := st.Runs().Insert(ctx, &store.RunRecord{
		RunID: "run-" + taskID, TaskID: taskID, RetryIndex: 0,
		StationRevisionID: revisionID("SCE-1", "sha256:sce1"),
		WorkingRoot:       "/tmp/wr-" + taskID, State: "complete",
		CreatedAt:         terminal.Add(-2 * time.Hour),
		TerminalAt:        sql.NullTime{Time: terminal, Valid: true},
	}); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return taskID
}

// revisionID returns the deterministic revision id the registry derives
// from a spec's content hash (rev-<station>-<last 12 chars of hash>).
func revisionID(stationID, contentHash string) string {
	shortHash := contentHash
	if len(shortHash) > 12 {
		shortHash = shortHash[len(shortHash)-12:]
	}
	return "rev-" + stationID + "-" + shortHash
}

// TestService_SummaryCompletedNeverAgedOut — the upstream summary must
// include completed runs in the slot list regardless of how old their
// terminal_at is; age-based visibility pruning is owned by the console
// gateway (Spec: console / Station Summary Contract — "Visibility timeout
// alignment").
func TestService_SummaryCompletedNeverAgedOut(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	oldTerminal := now.Add(-48 * time.Hour)
	since := now.Add(-7 * 24 * time.Hour)
	st, svc := newSummaryTestStore(t, now)
	insertCompletedRun(t, st, "oldtask", oldTerminal)

	items, err := svc.Summary(context.Background(), since, 12)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("summary items = %d, want 1", len(items))
	}
	station := items[0]
	if station.StationID != "SCE-1" {
		t.Fatalf("station_id = %s, want SCE-1", station.StationID)
	}
	if station.Counts.Success != 1 {
		t.Fatalf("counts.success = %d, want 1 (terminal within since)", station.Counts.Success)
	}
	found := false
	for _, slot := range station.Slots {
		if slot.Kind == "completed" && slot.TaskID == "oldtask" {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed slot for oldtask (terminal 48h ago) missing from summary slots: %+v", station.Slots)
	}
}

// TestService_SummaryCompletedOutsideWindowNotCounted — a completed run
// whose terminal_at precedes the since window is still listed as a slot
// but excluded from the interval success counter.
func TestService_SummaryCompletedOutsideWindowNotCounted(t *testing.T) {
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	oldTerminal := now.Add(-48 * time.Hour)
	since := now.Add(-24 * time.Hour)
	st, svc := newSummaryTestStore(t, now)
	insertCompletedRun(t, st, "oldtask", oldTerminal)

	items, err := svc.Summary(context.Background(), since, 12)
	if err != nil {
		t.Fatalf("Summary: %v", err)
	}
	station := items[0]
	if station.Counts.Success != 0 {
		t.Fatalf("counts.success = %d, want 0 (terminal before since)", station.Counts.Success)
	}
	found := false
	for _, slot := range station.Slots {
		if slot.Kind == "completed" && slot.TaskID == "oldtask" {
			found = true
		}
	}
	if !found {
		t.Fatalf("completed slot for oldtask missing from summary slots: %+v", station.Slots)
	}
}
