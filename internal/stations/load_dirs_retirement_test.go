package stations_test

import (
	"context"
	"testing"

	"github.com/gobobr/veriproc/internal/stations"
)

// Task 2.5 / Design D3: a station that is filtered out of every root's
// include list never registers, so:
//   - it surfaces in the startup drift warning (ListMissing) exactly like a
//     renamed station — correct operator signal, not filter noise;
//   - it remains retirable: GuardRetirable does not raise
//     ErrStationStillLoaded because the guard is registry-based.
func TestLoadDirs_FilteredStationDriftAndRetirement(t *testing.T) {
	root := writeTwoStationRoot(t, "PREV", "KEEP")

	// Seed the store with both stations loaded (no filter), as in a previous
	// instance run.
	seedStore := newMultiRootTestStore(t)
	seedReg := stations.NewRegistry()
	if _, _, err := stations.LoadDirs(context.Background(),
		[]stations.RootSpec{{Dir: root}}, seedReg, seedStore, nil); err != nil {
		t.Fatalf("seed LoadDirs: %v", err)
	}

	// Second run: PREV is now filtered out.
	reg := stations.NewRegistry()
	if _, _, err := stations.LoadDirs(context.Background(),
		[]stations.RootSpec{{Dir: root, Include: map[string]bool{"KEEP": true}}},
		reg, seedStore, nil); err != nil {
		t.Fatalf("filtered LoadDirs: %v", err)
	}

	svc := stations.NewService(seedStore, nil)
	svc.SetConfigSource(reg, reg, nil, nil)

	missing, err := svc.ListMissing(context.Background())
	if err != nil {
		t.Fatalf("ListMissing: %v", err)
	}
	if len(missing) != 1 || missing[0] != "PREV" {
		t.Fatalf("missing = %v, want [PREV] (filtered station surfaces like a renamed one)", missing)
	}

	// The filtered station is retirable (guard is registry-based).
	if err := svc.GuardRetirable(context.Background(), "PREV"); err != nil {
		t.Fatalf("filtered station must be retirable, got %v", err)
	}
	// The loaded station is not.
	if err := svc.GuardRetirable(context.Background(), "KEEP"); err == nil {
		t.Fatal("loaded station must not be retirable")
	}
}
