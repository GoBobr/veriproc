package stations_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
)

func TestLoader_ComputesContentHash(t *testing.T) {
	def := stations.Definition{
		StationID:   "STATION-A",
		StationName: "SCE_2",
		Description: "demo station",
		Execution:   stations.Execution{Executable: "./scripts/run.sh"},
		Outputs:     stations.OutputDefinitions{{Name: "result.json", FileType: "RESULT", Required: true}},
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

func TestLoader_DeterministicHashForEquivalentConfig(t *testing.T) {
	left := stations.Definition{
		StationID: "STATION-A", StationName: "SCE_2",
		Execution: stations.Execution{Executable: "./scripts/run.sh", Args: []string{"--validate"}},
	}
	right := stations.Definition{
		StationName: "SCE_2", StationID: "STATION-A",
		Execution: stations.Execution{Executable: "./scripts/run.sh", Args: []string{"--validate"}},
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

func TestLoader_RejectsMismatchedExplicitHash(t *testing.T) {
	_, err := stations.SpecFromDefinition(stations.Definition{
		StationID: "STATION-A", StationName: "SCE_2", ContentHash: "sha256:wrong",
	})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestLoader_LoadsMultipleStationsFromDirectory(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "station-a", `station_id: STATION-A
station_name: Scenario 2
description: first
scripts:
  run: ./scripts/run.sh
`)
	writeStation(t, root, "station-b", `station_id: STATION-B
station_name: Track L1
outputs:
  - file_type: TRACK
    name: track.csv
    mandatory: true
`)
	st := openStore(t)
	reg := stations.NewRegistry()
	specs, err := stations.LoadDir(context.Background(), root, reg, st, nil)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("loaded %d stations, want 2", len(specs))
	}
	if _, err := reg.Resolve(context.Background(), "STATION-A"); err != nil {
		t.Fatalf("resolve STATION-A: %v", err)
	}
	if _, err := reg.Resolve(context.Background(), "STATION-B"); err != nil {
		t.Fatalf("resolve STATION-B: %v", err)
	}
}

func TestLoader_NormalizesInputMandatoryToOptional(t *testing.T) {
	mandatoryTrue := true
	mandatoryFalse := false
	def := stations.Definition{
		StationID:   "STATION-A",
		StationName: "SCE_2",
		Execution:   stations.Execution{Executable: "./scripts/run.sh"},
		Inputs: []stations.InputDefinition{
			{FileType: "AX_OPT", Category: "aux", Mandatory: &mandatoryFalse}, // explicit non-mandatory
			{FileType: "AX_REQ", Category: "aux", Mandatory: &mandatoryTrue},  // explicit mandatory
			{FileType: "AX_DEFAULT", Category: "aux"},                         // omitted => mandatory
			{FileType: "AX_OPTFLAG", Category: "aux", Optional: true},         // optional flag honored
		},
	}
	spec, err := stations.SpecFromDefinition(def)
	if err != nil {
		t.Fatalf("SpecFromDefinition: %v", err)
	}
	want := []bool{true, false, false, true}
	for i, w := range want {
		if got := spec.Inputs[i].Optional; got != w {
			t.Errorf("input %q: Optional = %v, want %v", spec.Inputs[i].FileType, got, w)
		}
	}
}

func TestLoader_DuplicateStationDetection(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "one", `station_id: DUP
station_name: Alpha
`)
	writeStation(t, root, "two", `station_id: DUP
station_name: Beta
`)
	_, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if !errors.Is(err, stations.ErrDuplicateStation) {
		t.Fatalf("err = %v, want ErrDuplicateStation", err)
	}
}

func TestRegistry_RejectsConflictingSeedAfterLoad(t *testing.T) {
	reg := stations.NewRegistry()
	ctx := context.Background()
	first, err := stations.SpecFromSeed("STATION-A", "Scenario 2")
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

// TestLoader_JobOrder_Formats — valid joborder format values load without error.
func TestLoader_JobOrder_Formats(t *testing.T) {
	for _, format := range []string{"yaml", "json", "toml", "none", ""} {
		root := t.TempDir()
		content := "station_id: JO-FORMAT\nstation_name: JO Format Test\n"
		if format != "" {
			content += "joborder:\n  format: " + format + "\n"
		}
		writeStation(t, root, "s", content)
		_, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
		if err != nil {
			t.Errorf("format %q: unexpected error: %v", format, err)
		}
	}
}

// TestLoader_JobOrder_InvalidFormat — unknown format is rejected.
func TestLoader_JobOrder_InvalidFormat(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", "station_id: JO-BAD\nstation_name: JO Bad\njoborder:\n  format: xml\n")
	_, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if err == nil {
		t.Fatal("expected error for invalid joborder format")
	}
}

// TestLoader_JobOrder_NoneWithInclude — format: none must not have include.
func TestLoader_JobOrder_NoneWithInclude(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", `station_id: JO-NONE
station_name: JO None
joborder:
  format: none
  include:
    extra: forbidden
`)
	_, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if err == nil {
		t.Fatal("expected error: format none must not have include")
	}
}

// TestLoader_JobOrder_NoneWithName — format: none must not have a name.
func TestLoader_JobOrder_NoneWithName(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", "station_id: JO-NONE2\nstation_name: JO None2\njoborder:\n  format: none\n  name: should-not-exist.yaml\n")
	_, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if err == nil {
		t.Fatal("expected error: format none must not have name")
	}
}

func TestLoader_JobOrderTemplateFileLoaded(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", `station_id: JO-TPL
station_name: JO Template
joborder:
  renderer: template
  format: toml
  name: joborder.toml
  template_file: joborder.toml.tpl
`)
	templatePath := filepath.Join(root, "s", "joborder.toml.tpl")
	if err := os.WriteFile(templatePath, []byte("input = {{ tomlq (input \"PRIMARY\") }}\n"), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	specs, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("loaded %d stations, want 1", len(specs))
	}
	if specs[0].JobOrder.Renderer != "template" || specs[0].JobOrder.Template == "" {
		t.Fatalf("template joborder not loaded: %#v", specs[0].JobOrder)
	}
}

// TestLoader_Execution_RelativePathResolvedToAbsolute — relative executable
// paths are resolved to absolute when LoadDir is called.
func TestLoader_Execution_RelativePathResolvedToAbsolute(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", "station_id: EXE-REL\nstation_name: Exe Rel\nexecution:\n  executable: ./scripts/run.sh\n")
	specs, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if !filepath.IsAbs(specs[0].Execution.Executable) {
		t.Errorf("executable should be resolved to absolute path, got %q", specs[0].Execution.Executable)
	}
}

// TestLoader_Execution_RelativePath — relative executable path is accepted.
func TestLoader_Execution_RelativePath(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", "station_id: EXE-REL\nstation_name: Exe Rel\nexecution:\n  executable: ./scripts/run.sh\n")
	_, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoader_ExecutionSlurmDockerSettings(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", `station_id: SLURM-DOCKER
station_name: Slurm Docker
execution:
  mode: slurm-docker
  executable: /opt/proc/run.sh
  args:
    - --task
    - "{{ task_id }}"
  resources:
    cpus_per_task: 4
    mem_gb: 16
    walltime: PT1H
  slurm:
    partition: gpu
    account: co2m
    qos: normal
    extra_args:
      - --constraint=a100
  container:
    image: registry.example.test/proc:latest
    mounts:
      - /shared:/shared
    user: host
`)
	specs, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	exec := specs[0].Execution
	if exec.Mode != "slurm-docker" || exec.Resources.CPUsPerTask != 4 || exec.Resources.MemGB != 16 {
		t.Fatalf("execution = %+v", exec)
	}
	if exec.Slurm.Partition != "gpu" || len(exec.Slurm.ExtraArgs) != 1 {
		t.Fatalf("slurm = %+v", exec.Slurm)
	}
	if exec.Container.Image == "" || exec.Container.User != "host" || len(exec.Container.Mounts) != 1 {
		t.Fatalf("container = %+v", exec.Container)
	}
}

func TestLoader_SlurmDockerRequiresImage(t *testing.T) {
	root := t.TempDir()
	writeStation(t, root, "s", `station_id: SLURM-NO-IMAGE
station_name: Slurm No Image
execution:
	mode: slurm-docker
	executable: /opt/proc/run.sh
`)
	if _, err := stations.LoadDir(context.Background(), root, stations.NewRegistry(), nil, nil); err == nil {
		t.Fatal("expected validation error")
	}
}
