package stations_test

import (
	"context"
	"testing"

	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
)

// newRetireTestStore opens a migrated store seeded with one live station
// (registered in the registry, folder present) and ghost stations that only
// exist in the store — the state a daemon sees after a station folder was
// renamed or deleted.
func newRetireTestStore(t *testing.T) (*store.Store, *stations.Registry, *stations.Service) {
	t.Helper()
	dbPath := t.TempDir() + "/retire.db"
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
		stations.Spec{StationID: "live", StationName: "Live",
			ContentHash: "sha256:live", SchemaVersion: "veriproc.station/v1"},
	); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	for _, ghost := range []string{"ghost1", "ghost2", "retiredgone"} {
		if err := st.Stations().Insert(context.Background(), &store.StationRevisionRecord{
			RevisionID: "rev-" + ghost, StationID: ghost, StationName: "Ghost " + ghost,
			ContentHash: "sha256:" + ghost, SchemaVersion: "veriproc.station/v1",
		}); err != nil {
			t.Fatalf("insert %s: %v", ghost, err)
		}
	}
	svc := stations.NewService(st, nil)
	svc.SetConfigSource(reg, reg, nil, nil)
	return st, reg, svc
}

// TestService_ListMissing — service.ListMissing reports store-known,
// non-retired stations whose folders are not registered (Spec: Startup
// Drift Warning For Missing Stations — "Startup warns about ghost station").
func TestService_ListMissing(t *testing.T) {
	st, reg, svc := newRetireTestStore(t)
	ctx := context.Background()

	missing, err := svc.ListMissing(ctx)
	if err != nil {
		t.Fatalf("ListMissing: %v", err)
	}
	if len(missing) != 3 {
		t.Fatalf("missing = %v, want 3 ghost stations", missing)
	}

	// Retire one ghost — it must drop out of the drift report while all
	// registration data stays intact (no DB writes of the check itself).
	if _, err := st.Stations().RetireStation(ctx, "retiredgone"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	missing, err = svc.ListMissing(ctx)
	if err != nil {
		t.Fatalf("ListMissing after retire: %v", err)
	}
	if len(missing) != 2 {
		t.Fatalf("missing after retire = %v, want 2", missing)
	}
	for _, id := range missing {
		if id == "retiredgone" {
			t.Error("retired station still reported as missing")
		}
	}

	// The check makes no writes: unretiring restores the station to the
	// listing, proving its rows were untouched by ListMissing.
	if err := st.Stations().UnretireStation(ctx, "retiredgone"); err != nil {
		t.Fatalf("unretire: %v", err)
	}
	missing, err = svc.ListMissing(ctx)
	if err != nil {
		t.Fatalf("ListMissing after unretire: %v", err)
	}
	if len(missing) != 3 {
		t.Fatalf("missing after unretire = %v, want 3", missing)
	}
	if _, err := reg.Resolve(ctx, "live"); err != nil {
		t.Errorf("live station must still resolve: %v", err)
	}
}

// TestService_GuardRetirable_RetireLifecycle — active station refused,
// unknown station unknown, ghost retired, re-retire idempotent
// (Spec: Station Retirement Semantics).
func TestService_GuardRetirable_RetireLifecycle(t *testing.T) {
	st, _, svc := newRetireTestStore(t)
	ctx := context.Background()

	if err := svc.GuardRetirable(ctx, "live"); err == nil {
		t.Error("guard should refuse a station that is still loaded")
	}
	if err := svc.GuardRetirable(ctx, "nosuch"); err == nil {
		t.Error("guard should refuse an unknown station")
	}
	if err := svc.GuardRetirable(ctx, "ghost1"); err != nil {
		t.Fatalf("guard should accept ghost1: %v", err)
	}

	changed, err := svc.Retire(ctx, "ghost1")
	if err != nil || !changed {
		t.Fatalf("retire ghost1 = %v, %v", changed, err)
	}
	changed, err = svc.Retire(ctx, "ghost1")
	if err != nil || changed {
		t.Fatalf("re-retire ghost1 = %v, %v (want false, nil)", changed, err)
	}

	// Retired station: hidden from listing, unresolvable as a registered
	// station, but still known to the store (guard passes for idempotent
	// re-retire; unknown stations must not).
	if _, err := st.Stations().Get(ctx, "rev-ghost1"); err != nil {
		t.Errorf("revision row of retired station must remain: %v", err)
	}
}
