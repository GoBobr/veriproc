package stations_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

func TestLoader_ComputesContentHash_M7Refined(t *testing.T) {
	def := stations.Definition{
		StationID:   "STATION-A",
		ProcType:    "SCE_2",
		Description: "demo station",
		Scripts:     map[string]string{"run": "./scripts/run.sh"},
		Outputs:     []string{"result.json"},
	}
	spec, err := stations.SpecFromDefinition(def)
	if err != nil {
		t.Fatalf("SpecFromDefinition: %v", err)
	}
	if spec.ContentHash == "" || spec.ContentHash[:7] != "sha256:" {
		t.Fatalf("content_hash = %q, want sha256:...", spec.ContentHash)
	}
	if spec.SchemaVersion != stations.DefaultSchemaVersion {
		t.Fatalf("schema_version = %q", spec.SchemaVersion)
	}
}

func TestLoader_DeterministicHashForEquivalentConfig_M7Refined(t *testing.T) {
	left := stations.Definition{
		StationID: "STATION-A", ProcType: "SCE_2",
		Scripts:  map[string]string{"run": "./scripts/run.sh", "validate": "./scripts/validate.sh"},
		Metadata: map[string]string{"owner": "science", "tier": "sandbox"},
	}
	right := stations.Definition{
		ProcType: "SCE_2", StationID: "STATION-A",
		Metadata: map[string]string{"tier": "sandbox", "owner": "science"},
		Scripts:  map[string]string{"validate": "./scripts/validate.sh", "run": "./scripts/run.sh"},
	}
	lh, err := stations.ComputeContentHash(left)
	if err != nil {
		t.Fatalf("left hash: %v", err)
	}
	rh, err := stations.ComputeContentHash(right)
	if err != nil {
		t.Fatalf("right hash: %v", err)
	}
	if lh != rh {
		t.Fatalf("hashes differ: %s != %s", lh, rh)
	}
}

func TestLoader_RejectsMismatchedExplicitHash_M7Refined(t *testing.T) {
	_, err := stations.SpecFromDefinition(stations.Definition{
		StationID: "STATION-A", ProcType: "SCE_2", ContentHash: "sha256:wrong",
	})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestLoader_LoadsMultipleStationsFromDirectory_M7Refined(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "station-a", `station_id: STATION-A
proc_type: SCE_2
description: first
scripts:
  run: ./scripts/run.sh
`)
	writeStation(t, root, "station-b", `station_id: STATION-B
proc_type: TRACK_L1
outputs:
  - track.csv
`)
	st := openStore(t)
	reg := stations.NewRegistry()
	specs, err := stations.LoadDir(context.Background(), root, reg, st)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("loaded %d stations, want 2", len(specs))
	}
	if _, err := reg.Resolve(context.Background(), "STATION-A", ""); err != nil {
		t.Fatalf("resolve STATION-A: %v", err)
	}
	if _, err := reg.Resolve(context.Background(), "", "TRACK_L1"); err != nil {
		t.Fatalf("resolve TRACK_L1: %v", err)
	}
}

func TestLoader_DuplicateStationDetection_M7Refined(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "one", `station_id: DUP
proc_type: A
`)
	writeStation(t, root, "two", `station_id: DUP
proc_type: B
`)
	_, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil)
	if !errors.Is(err, stations.ErrDuplicateStation) {
		t.Fatalf("err = %v, want ErrDuplicateStation", err)
	}
}

func TestRegistry_RejectsConflictingSeedAfterLoad_M7Refined(t *testing.T) {
	reg := stations.NewRegistry()
	ctx := context.Background()
	first, err := stations.SpecFromSeed("STATION-A", "SCE_2")
	if err != nil {
		t.Fatalf("seed spec: %v", err)
	}
	if err := reg.Seed(ctx, nil, first); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	conflict := first
	conflict.ContentHash = "sha256:other"
	if err := reg.Seed(ctx, nil, conflict); !errors.Is(err, stations.ErrDuplicateStation) {
		t.Fatalf("err = %v, want ErrDuplicateStation", err)
	}
}

func writeStation(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "station.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write station: %v", err)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "stations.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}
