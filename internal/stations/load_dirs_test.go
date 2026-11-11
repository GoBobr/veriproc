package stations_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
)

// newMultiRootTestStore opens a migrated in-memory store.
func newMultiRootTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite://:memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// loadIDs returns the station IDs (in load order) from LoadDirs.
func loadIDs(t *testing.T, roots []stations.RootSpec) []string {
	t.Helper()
	st := newMultiRootTestStore(t)
	reg := stations.NewRegistry()
	specs, _, err := stations.LoadDirs(context.Background(), roots, reg, st, nil)
	if err != nil {
		t.Fatalf("LoadDirs: %v", err)
	}
	ids := make([]string, len(specs))
	for i, sp := range specs {
		ids[i] = sp.StationID
	}
	return ids
}

// writeTwoStationRoot creates a root containing stations `root-a-station`
// and `root-b-station` (in `alpha-a` and `beta-b` folders respectively, so
// lexical folder order differs from station ID order determined below).
func writeTwoStationRoot(t *testing.T, idA, idB string) string {
	t.Helper()
	root := t.TempDir()
	writeStation(t, root, "alpha-a", "station_id: "+idA+"\nstation_name: "+idA+" name\n")
	writeStation(t, root, "beta-b", "station_id: "+idB+"\nstation_name: "+idB+" name\n")
	return root
}

// Scenario: Stations from two roots are all loaded
func TestLoadDirs_TwoRootsAllLoaded(t *testing.T) {
	rootA := writeTwoStationRoot(t, "GAP-A", "OTHER-A")
	rootB := writeTwoStationRoot(t, "GAP-B", "OTHER-B")
	got := loadIDs(t, []stations.RootSpec{{Dir: rootA}, {Dir: rootB}})
	want := []string{"GAP-A", "OTHER-A", "GAP-B", "OTHER-B"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("loaded %v, want %v", got, want)
	}
}

// Scenario: Filtered station is invisible
func TestLoadDirs_IncludeFilterExcludesStation(t *testing.T) {
	root := writeTwoStationRoot(t, "CL2GAP", "CL2REF")
	st := newMultiRootTestStore(t)
	reg := stations.NewRegistry()
	specs, warnings, err := stations.LoadDirs(context.Background(),
		[]stations.RootSpec{{Dir: root, Include: map[string]bool{"CL2REF": true}}},
		reg, st, nil)
	if err != nil {
		t.Fatalf("LoadDirs: %v", err)
	}
	if len(specs) != 1 || specs[0].StationID != "CL2REF" {
		t.Fatalf("loaded %v, want [CL2REF]", specIDs(specs))
	}
	if _, err := reg.Resolve(context.Background(), "CL2GAP"); err == nil {
		t.Fatal("filtered station CL2GAP must not be resolvable in the registry")
	}
	for _, w := range warnings {
		if strings.Contains(w, "CL2GAP") {
			t.Fatalf("unexpected warning about filtered station: %s", w)
		}
	}
}

// Scenario: Same station in two roots, filtered in one
func TestLoadDirs_DuplicateFilteredInOneRoot(t *testing.T) {
	rootA := t.TempDir()
	writeStation(t, rootA, "only-dup", "station_id: DUP\nstation_name: A\n")
	rootB := t.TempDir()
	writeStation(t, rootB, "only-dup", "station_id: DUP\nstation_name: B\n")
	got := loadIDs(t, []stations.RootSpec{
		{Dir: rootA},
		{Dir: rootB, Include: map[string]bool{"SOMETHING-ELSE": true}},
	})
	if len(got) != 1 || got[0] != "DUP" {
		t.Fatalf("loaded %v, want [DUP] loaded exactly once from root A", got)
	}
}

// Scenario: Unfiltered duplicate fails startup
func TestLoadDirs_DuplicateAcrossRootsFails(t *testing.T) {
	rootA := t.TempDir()
	writeStation(t, rootA, "dup", "station_id: DUP\nstation_name: A\n")
	rootB := t.TempDir()
	writeStation(t, rootB, "dup", "station_id: DUP\nstation_name: B\n")
	st := newMultiRootTestStore(t)
	reg := stations.NewRegistry()
	_, _, err := stations.LoadDirs(context.Background(),
		[]stations.RootSpec{{Dir: rootA}, {Dir: rootB}}, reg, st, nil)
	if err == nil || !strings.Contains(err.Error(), "DUP") {
		t.Fatalf("want duplicate error naming DUP, got %v", err)
	}
	if !strings.Contains(err.Error(), "dup") || !strings.Contains(err.Error(), rootA) || !strings.Contains(err.Error(), rootB) {
		t.Fatalf("duplicate error must name station and both paths: %v", err)
	}
}

// Scenario: Include entry matching nothing is not an error
func TestLoadDirs_UnmatchedIncludeEntrySucceeds(t *testing.T) {
	root := writeTwoStationRoot(t, "ONE", "TWO")
	got := loadIDs(t, []stations.RootSpec{
		{Dir: root, Include: map[string]bool{"ONE": true, "NOSUCHSTATION": true}},
	})
	if len(got) != 1 || got[0] != "ONE" {
		t.Fatalf("loaded %v, want [ONE]", got)
	}
}

// Scenario: Missing ghost root does not break startup
func TestLoadDirs_MissingRootWarnsAndContinues(t *testing.T) {
	rootA := writeTwoStationRoot(t, "KEEP-1", "KEEP-2")
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	st := newMultiRootTestStore(t)
	reg := stations.NewRegistry()
	specs, warnings, err := stations.LoadDirs(context.Background(),
		[]stations.RootSpec{{Dir: rootA}, {Dir: missing}}, reg, st, nil)
	if err != nil {
		t.Fatalf("LoadDirs must not fail on a missing root: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("loaded %d stations from root A, want 2", len(specs))
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, missing) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning naming missing root %q in %v", missing, warnings)
	}
}

// Scenario: Legacy singular root keeps working (via LoadDir wrapper)
func TestLoadDirs_LoadDirWrapperUnfiltered(t *testing.T) {
	// covered by TestLoader_LoadsMultipleStationsFromDirectory for LoadDir;
	// here: LoadDirs with one unfiltered root equals the same behavior.
	root := writeTwoStationRoot(t, "LEG-1", "LEG-2")
	got := loadIDs(t, []stations.RootSpec{{Dir: root}})
	if len(got) != 2 {
		t.Fatalf("loaded %v, want both legacy stations", got)
	}
}

// LoadDirs returns specs in deterministic per-root lexical order (root
// order, then folder order within each root). Display ordering is a
// service concern and defaults to global station_id-alphabetical when no
// station_order is set — see load_dirs_retirement_test.go and the store's
// ORDER BY station_id. This test pins the loader's own deterministic
// ordering only.
func TestLoadDirs_FallbackOrderRootThenLexical(t *testing.T) {
	// root A listed first contains a station that would sort LAST lexically.
	rootA := t.TempDir()
	writeStation(t, rootA, "zeta-first", "station_id: ZETA\nstation_name: Z\n")
	rootB := t.TempDir()
	writeStation(t, rootB, "a-alpha", "station_id: ALPHA\nstation_name: A\n")
	got := loadIDs(t, []stations.RootSpec{{Dir: rootA}, {Dir: rootB}})
	if strings.Join(got, ",") != "ZETA,ALPHA" {
		t.Fatalf("loader spec order = %v, want root order then lexical: ZETA,ALPHA", got)
	}
}

func specIDs(specs []stations.Spec) []string {
	ids := make([]string, len(specs))
	for i, sp := range specs {
		ids[i] = sp.StationID
	}
	return ids
}
