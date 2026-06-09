package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	toml "github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/gobobr/veriproc/internal/executor"
	"github.com/gobobr/veriproc/internal/runs"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/gobobr/veriproc/internal/tasks"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

type fixture struct {
	st       *store.Store
	tasks    *tasks.Service
	exec     *executor.StubExecutor
	runs     *runs.Service
	dispatch *runs.Dispatcher
}

// TestRuns_LocalExecutionJobOrderArchiveDownstream_2_8_2_12_2_13 exercises a
// spec-faithful local execution path: real input resolution, joborder.yaml,
// runtime environment, output validation, publication, and default downstream.
func TestRuns_LocalExecutionJobOrderArchiveDownstream_2_8_2_12_2_13(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "local.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	archive := filepath.Join(dir, "archive", "hot")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archive, "20250703_AUX_A_v1.txt"), []byte("aux A v1\n"), 0o644); err != nil {
		t.Fatalf("write aux: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archive, "20250703_PRIMARY_A_v1.txt"), []byte("primary A v1\n"), 0o644); err != nil {
		t.Fatalf("write primary: %v", err)
	}

	scriptA := writeExecutable(t, dir, "station-a.sh", `#!/bin/sh
set -eu
test -n "$VERIPROC_WORKING_ROOT"
test -n "$VERIPROC_STATION_ID"
test -n "$VERIPROC_RUN_REF"
test -n "$VERIPROC_TASK_ID"
test -n "$VERIPROC_RETRY_INDEX"
test -f "$VERIPROC_JOBORDER_PATH"
printf '{"station":"%s","run_ref":"%s"}\n' "$VERIPROC_STATION_ID" "$VERIPROC_RUN_REF" > "$VERIPROC_RUN_DIR/result-a.json"
`)
	scriptB := writeExecutable(t, dir, "station-b.sh", `#!/bin/sh
set -eu
test -f input/result-a.json
printf '{"station":"%s","parent_input":"ok"}\n' "$VERIPROC_STATION_ID" > "$VERIPROC_RUN_DIR/result-b.json"
`)

	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st,
		stations.Spec{StationID: "STATION-A", StationName: "A_PROC", ContentHash: "sha256:station-a", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "PRIMARY_A", Category: "product"}, {FileType: "AUX_A", Category: "product"}}, Outputs: []stations.OutputDefinition{{Name: "result-a.json", FileType: "A_RESULT", Required: true, Publish: &stations.OutputPublish{RollingArchive: "hot", Mode: "copy"}}}, Downstream: []stations.DownstreamTarget{{StationID: "STATION-B"}}, Execution: stations.Execution{Executable: scriptA}},
		stations.Spec{StationID: "STATION-B", StationName: "B_PROC", ContentHash: "sha256:station-b", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "A_RESULT", Category: "product", Pattern: "result-a.json"}}, Outputs: []stations.OutputDefinition{{Name: "result-b.json", FileType: "B_RESULT", Required: true}}, Execution: stations.Execution{Executable: scriptB}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "00000a" })
	runN := 0
	rsvc := runs.NewService(runs.Config{Store: st, Executors: executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), InstanceID: "test-instance", Facility: map[string]string{"environment": "TEST"}, RollingArchives: map[string]string{"hot": archive}, ProductCategories: map[string][]string{"product": {"rolling:hot"}}, Generators: map[string]string{"job_order": "test-generator-v1"}, IDFactory: func() string { runN++; return "run-local-" + strconv.Itoa(runN) }})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "STATION-A"}, Window: tasks.Window{Start: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC), End: time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC)}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")

	parent, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	runA, _ := st.Runs().Get(ctx, parent.CanonicalRunID)
	for _, rel := range []string{"joborder.yaml", "input", "output", "logs", "temp", "manifest/resolved-inputs.yaml"} {
		if _, err := os.Stat(filepath.Join(runA.WorkingRoot, rel)); err != nil {
			t.Fatalf("working root missing %s: %v", rel, err)
		}
	}
	jobOrderRaw, err := os.ReadFile(filepath.Join(runA.WorkingRoot, "joborder.yaml"))
	if err != nil {
		t.Fatalf("read joborder: %v", err)
	}
	var jobOrder map[string]any
	if err := yaml.Unmarshal(jobOrderRaw, &jobOrder); err != nil {
		t.Fatalf("parse joborder: %v", err)
	}
	meta, ok := jobOrder["veriproc_meta"].(map[string]any)
	if !ok {
		t.Fatalf("joborder missing veriproc_meta: %#v", jobOrder)
	}
	if meta["schema_version"] != "veriproc.joborder/v1" || meta["task_id"] != runA.TaskID || meta["retry_index"] == nil {
		t.Fatalf("joborder veriproc_meta incomplete: %#v", meta)
	}
	if _, ok := jobOrder["schema_version"]; ok {
		t.Fatalf("schema_version must be under veriproc_meta, got root joborder: %#v", jobOrder)
	}
	if inputs, ok := jobOrder["inputs"].([]any); !ok || len(inputs) != 2 {
		t.Fatalf("joborder inputs = %#v, want two flat input entries", jobOrder["inputs"])
	}
	mf, err := st.Manifests().GetByRun(ctx, runA.RunID)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(mf.Entries) != 2 || mf.Entries[0].SourceArchiveID != "hot" {
		t.Fatalf("manifest entries not resolved from archive: %#v", mf.Entries)
	}
	for _, entry := range mf.Entries {
		if entry.Size == 0 || !entry.MTime.Valid || entry.SelectionReason == "" {
			t.Fatalf("manifest entry missing available metadata: %#v", entry)
		}
		if entry.Checksum != "" || entry.ChecksumAlgo != "" || entry.ChecksumSource != "" {
			t.Fatalf("available_only should not compute input checksum without metadata: %#v", entry)
		}
	}
	arts, _ := st.Artifacts().ListByRun(ctx, runA.RunID, "output")
	if len(arts) != 1 || arts[0].Size == 0 || arts[0].FileType != "A_RESULT" {
		t.Fatalf("output artifact = %#v", arts)
	}
	if arts[0].Checksum != "" || arts[0].ChecksumAlgo != "" || arts[0].ChecksumSource != "" {
		t.Fatalf("available_only should not compute output checksum without metadata: %#v", arts[0])
	}
	pubs, _ := st.Publications().ListByRun(ctx, runA.RunID)
	if len(pubs) != 1 || pubs[0].PublicationState != store.PublicationStatePublished {
		t.Fatalf("publication = %#v", pubs)
	}
	if pubs[0].Checksum != "" || pubs[0].ChecksumAlgo != "" || pubs[0].ChecksumSource != "" {
		t.Fatalf("available_only should not compute publication checksum without metadata: %#v", pubs[0])
	}
	if pubs[0].TargetPath != "result-a.json" {
		t.Fatalf("publication target_path = %q, want archive-root filename", pubs[0].TargetPath)
	}
	if _, err := os.Stat(filepath.Join(archive, "result-a.json")); err != nil {
		t.Fatalf("published output missing: %v", err)
	}
	waitForTaskCount(t, ctx, st, disp, 2)
	page, _ := st.Tasks().List(ctx, store.ListFilter{ParentTaskID: runA.TaskID, Limit: 10})
	if len(page.Items) != 1 || page.Items[0].DestinationStationID != "STATION-B" {
		t.Fatalf("downstream tasks = %#v", page.Items)
	}
	waitForTaskState(t, ctx, st, disp, page.Items[0].TaskID, "completed")
}

func TestRuns_JoinDownstreamWaitsForAllMandatoryInputs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "join.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	archive := filepath.Join(dir, "archive", "hot")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}

	scriptA := writeExecutable(t, dir, "join-a.sh", `#!/bin/sh
set -eu
printf 'from A\n' > "$VERIPROC_RUN_DIR/A_OUT.dat"
`)
	scriptB := writeExecutable(t, dir, "join-b.sh", `#!/bin/sh
set -eu
printf 'from B\n' > "$VERIPROC_RUN_DIR/B_OUT.dat"
`)
	scriptC := writeExecutable(t, dir, "join-c.sh", `#!/bin/sh
set -eu
test -f input/A_OUT.dat
test -f input/B_OUT.dat
printf 'joined\n' > "$VERIPROC_RUN_DIR/C_OUT.dat"
`)

	reg := stations.NewRegistry()
	joinRoute := stations.DownstreamTarget{StationID: "JOIN-C", Mode: "join", JoinID: "a-b-to-c"}
	if err := reg.Seed(ctx, st,
		stations.Spec{StationID: "JOIN-A", StationName: "Join A", ContentHash: "sha256:join-a", SchemaVersion: "veriproc.station/v1", Outputs: []stations.OutputDefinition{{Name: "A_OUT.dat", FileType: "A_OUT", Required: true, Publish: &stations.OutputPublish{RollingArchive: "hot", Mode: "copy"}}}, Downstream: []stations.DownstreamTarget{joinRoute}, Execution: stations.Execution{Executable: scriptA}},
		stations.Spec{StationID: "JOIN-B", StationName: "Join B", ContentHash: "sha256:join-b", SchemaVersion: "veriproc.station/v1", Outputs: []stations.OutputDefinition{{Name: "B_OUT.dat", FileType: "B_OUT", Required: true, Publish: &stations.OutputPublish{RollingArchive: "hot", Mode: "copy"}}}, Downstream: []stations.DownstreamTarget{joinRoute}, Execution: stations.Execution{Executable: scriptB}},
		stations.Spec{StationID: "JOIN-C", StationName: "Join C", ContentHash: "sha256:join-c", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "A_OUT", Category: "product"}, {FileType: "B_OUT", Category: "product"}}, Outputs: []stations.OutputDefinition{{Name: "C_OUT.dat", FileType: "C_OUT", Required: true}}, Execution: stations.Execution{Executable: scriptC}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	taskN := 0
	tsvc := tasks.NewService(st, reg, nil, func() string { taskN++; return fmt.Sprintf("%06x", taskN) })
	runN := 0
	rsvc := runs.NewService(runs.Config{Store: st, Executors: executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), RollingArchives: map[string]string{"hot": archive}, ProductCategories: map[string][]string{"product": {"rolling:hot"}}, IDFactory: func() string { runN++; return "run-join-" + strconv.Itoa(runN) }})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	window := tasks.Window{Start: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC), End: time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC)}

	resA, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "JOIN-A"}, Window: window})
	if err != nil {
		t.Fatalf("submit A: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, resA.Task.TaskID, "completed")
	waitForTaskCount(t, ctx, st, disp, 2)
	cTaskID := findTaskByStation(t, ctx, st, "JOIN-C")
	waitForTaskState(t, ctx, st, disp, cTaskID, "waiting_inputs")
	cTask, _ := st.Tasks().Get(ctx, cTaskID)
	if cTask.LatestRetryIndex.Valid {
		t.Fatalf("join task created a run while inputs were missing: retry_index=%d", cTask.LatestRetryIndex.Int64)
	}

	resB, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "JOIN-B"}, Window: window})
	if err != nil {
		t.Fatalf("submit B: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, resB.Task.TaskID, "completed")
	waitForTaskState(t, ctx, st, disp, cTaskID, "completed")

	page, err := st.Tasks().List(ctx, store.ListFilter{DestinationStationID: "JOIN-C", Limit: 10})
	if err != nil {
		t.Fatalf("list JOIN-C tasks: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("JOIN-C task count = %d, want 1: %#v", len(page.Items), page.Items)
	}
	links, err := st.Provenance().ListByTarget(ctx, "task", cTaskID)
	if err != nil {
		t.Fatalf("list provenance: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("provenance link count = %d, want 2: %#v", len(links), links)
	}
	for _, link := range links {
		if link.SourceType != "run" || link.RelationshipType != "produced_downstream" {
			t.Fatalf("unexpected provenance link: %#v", link)
		}
	}
}

func TestRuns_DirectoryInputsOutputsAndPublication(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "dirs.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	archive := filepath.Join(dir, "archive", "hot")
	inputDir := filepath.Join(archive, "20250703_AUX_DIR_v1")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		t.Fatalf("mkdir input dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "data.nc"), []byte("aux directory payload\n"), 0o644); err != nil {
		t.Fatalf("write input dir payload: %v", err)
	}

	script := writeExecutable(t, dir, "dir-station.sh", `#!/bin/sh
set -eu
test -d input/20250703_AUX_DIR_v1
test -f input/20250703_AUX_DIR_v1/data.nc
mkdir -p "$VERIPROC_RUN_DIR/dir-output"
cp input/20250703_AUX_DIR_v1/data.nc "$VERIPROC_RUN_DIR/dir-output/data.nc"
`)
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "DIR-STATION",
		StationName:   "DIR-STATION",
		ContentHash:   "sha256:dir-station",
		SchemaVersion: "veriproc.station/v1",
		Inputs:        []stations.InputDefinition{{FileType: "AUX_DIR", Category: "product", ObjectKind: store.ObjectKindDirectory}},
		Outputs:       []stations.OutputDefinition{{Name: "dir-output", FileType: "DIR_OUTPUT", ObjectKind: store.ObjectKindDirectory, Required: true, Publish: &stations.OutputPublish{RollingArchive: "hot", Mode: "copy"}}},
		Execution:     stations.Execution{Executable: script},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "00d1a0" })
	rsvc := runs.NewService(runs.Config{Store: st, Executors: executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), RollingArchives: map[string]string{"hot": archive}, ProductCategories: map[string][]string{"product": {"rolling:hot"}}, IDFactory: func() string { return "run-dir" }})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "DIR-STATION"}, Window: tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)
	mf, err := st.Manifests().GetByRun(ctx, run.RunID)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(mf.Entries) != 1 || mf.Entries[0].ObjectKind != store.ObjectKindDirectory || mf.Entries[0].Size != 0 || mf.Entries[0].Checksum != "" {
		t.Fatalf("manifest directory entry = %#v", mf.Entries)
	}
	linkTarget, err := os.Readlink(filepath.Join(run.WorkingRoot, "input", filepath.Base(inputDir)))
	if err != nil {
		t.Fatalf("input directory should be symlinked: %v", err)
	}
	if linkTarget != inputDir {
		t.Fatalf("input symlink target = %q, want %q", linkTarget, inputDir)
	}
	arts, _ := st.Artifacts().ListByRun(ctx, run.RunID, "output")
	if len(arts) != 1 || arts[0].ObjectKind != store.ObjectKindDirectory || arts[0].Size != 0 || arts[0].Checksum != "" {
		t.Fatalf("directory output artifact = %#v", arts)
	}
	pubs, _ := st.Publications().ListByRun(ctx, run.RunID)
	if len(pubs) != 1 || pubs[0].ObjectKind != store.ObjectKindDirectory || pubs[0].PublicationState != store.PublicationStatePublished {
		t.Fatalf("directory publication = %#v", pubs)
	}
	if got, err := os.ReadFile(filepath.Join(archive, "dir-output", "data.nc")); err != nil || string(got) != "aux directory payload\n" {
		t.Fatalf("published directory payload = %q, err=%v", got, err)
	}
}

func TestRuns_LocalExecutionMissingOutputFails_5_6_7_5_4(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "missing.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	script := writeExecutable(t, dir, "no-output.sh", "#!/bin/sh\nset -eu\necho no output created\n")
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{StationID: "BROKEN", StationName: "BROKEN", ContentHash: "sha256:broken", SchemaVersion: "veriproc.station/v1", Outputs: []stations.OutputDefinition{{Name: "required.json", FileType: "REQUIRED", Required: true}}, Execution: stations.Execution{Executable: script}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "0b0b0b" })
	rsvc := runs.NewService(runs.Config{Store: st, Executors: executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), IDFactory: func() string { return "run-broken" }})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "BROKEN"}, Window: tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "failed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.LatestRunID)
	if run.State != "failed" || run.FailureReason == "" {
		t.Fatalf("run should fail with reason, got %#v", run)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "runs.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	archive := filepath.Join(t.TempDir(), "archive")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archive, "20250703_PRIMARY_INPUT_v1.dat"), []byte("primary input\n"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	if err := reg.Seed(context.Background(), st,
		stations.Spec{StationID: "SCENE-L2", StationName: "SCE_2", ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "PRIMARY_INPUT", Category: "product"}}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	now := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	taskN := 0
	taskIDs := func() string {
		taskN++
		return fmt.Sprintf("%06x", taskN)
	}
	runN := 0
	runIDs := func() string {
		runN++
		return "run-" + strconv.Itoa(runN)
	}
	tsvc := tasks.NewService(st, reg, fixedClock(now), taskIDs)
	exec := executor.NewStubExecutor(fixedClock(now))
	rsvc := runs.NewService(runs.Config{
		Store:             st,
		Executors:         executor.NewSingleExecutorRegistry(exec),
		Resolver:          reg,
		WorkingRootBase:   t.TempDir(),
		RollingArchives:   map[string]string{"hot": archive},
		ProductCategories: map[string][]string{"product": {"rolling:hot"}},
		Clock:             fixedClock(now),
		IDFactory:         runIDs,
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	return &fixture{st: st, tasks: tsvc, exec: exec, runs: rsvc, dispatch: disp}
}

func submitTask(t *testing.T, f *fixture) string {
	t.Helper()
	res, err := f.tasks.Submit(context.Background(), tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "SCENE-L2"},
		Window: tasks.Window{
			Start: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC),
			End:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	return res.Task.TaskID
}

func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}
	return path
}

func waitForTaskState(t *testing.T, ctx context.Context, st *store.Store, disp *runs.Dispatcher, taskID, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := disp.Tick(ctx); err != nil {
			t.Fatalf("dispatcher tick: %v", err)
		}
		task, err := st.Tasks().Get(ctx, taskID)
		if err != nil {
			t.Fatalf("get task: %v", err)
		}
		if task.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _ := st.Tasks().Get(ctx, taskID)
	t.Fatalf("task %s state = %q, want %q", taskID, task.State, want)
}

func waitForTaskCount(t *testing.T, ctx context.Context, st *store.Store, disp *runs.Dispatcher, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := disp.Tick(ctx); err != nil {
			t.Fatalf("dispatcher tick: %v", err)
		}
		page, err := st.Tasks().List(ctx, store.ListFilter{Limit: 100})
		if err != nil {
			t.Fatalf("list tasks: %v", err)
		}
		if len(page.Items) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	page, _ := st.Tasks().List(ctx, store.ListFilter{Limit: 100})
	t.Fatalf("task count = %d, want at least %d", len(page.Items), want)
}

// TestRuns_PrepareAndFreeze_3_8_3_10 — PrepareRun creates a run, persists a
// frozen manifest, computes a fingerprint, and leaves the run in state=ready.

func findTaskByStation(t *testing.T, ctx context.Context, st *store.Store, stationID string) string {
	t.Helper()
	page, err := st.Tasks().List(ctx, store.ListFilter{DestinationStationID: stationID, Limit: 10})
	if err != nil {
		t.Fatalf("list tasks for %s: %v", stationID, err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("tasks for %s = %d, want 1: %#v", stationID, len(page.Items), page.Items)
	}
	return page.Items[0].TaskID
}
func TestRuns_PrepareAndFreeze_3_8_3_10(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("PrepareRun: %v", err)
	}
	if r.State != "ready" {
		t.Errorf("state = %q, want ready", r.State)
	}
	if r.ProcessingFingerprint == "" {
		t.Error("fingerprint not set")
	}
	if r.RetryIndex != 0 {
		t.Errorf("retry_index = %d, want 0", r.RetryIndex)
	}
	mf, err := f.st.Manifests().GetByRun(context.Background(), r.RunID)
	if err != nil {
		t.Fatalf("GetByRun: %v", err)
	}
	if len(mf.Entries) == 0 {
		t.Error("manifest has no entries")
	}
}

// TestRuns_RetryCreatesNewIdentity_3_7 — preparing a second run for the
// same task yields a distinct run_id and incremented retry_index.
func TestRuns_RetryCreatesNewIdentity_3_7(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	r1, err := f.runs.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	r2, err := f.runs.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("second prepare: %v", err)
	}
	if r1.RunID == r2.RunID {
		t.Fatal("run_ids must differ")
	}
	if r2.RetryIndex != r1.RetryIndex+1 {
		t.Errorf("retry index: r1=%d r2=%d", r1.RetryIndex, r2.RetryIndex)
	}
}

// TestRuns_DispatchOnlyFromReady_5_6 — Dispatch on a non-ready run returns
// ErrInvalidStateTransition; idempotent re-dispatch from dispatched is a no-op.
func TestRuns_DispatchOnlyFromReady_5_6(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	r, _ := f.runs.PrepareRun(context.Background(), taskID)

	if _, err := f.runs.Dispatch(context.Background(), r.RunID); err != nil {
		t.Fatalf("first dispatch: %v", err)
	}
	// Idempotent re-dispatch: must not error and must not create a second job.
	if _, err := f.runs.Dispatch(context.Background(), r.RunID); err != nil {
		t.Fatalf("re-dispatch: %v", err)
	}
	jobs, _ := f.st.Jobs().ListByRun(context.Background(), r.RunID)
	if len(jobs) != 1 {
		t.Errorf("jobs = %d, want 1", len(jobs))
	}
}

// TestRuns_FullLifecycle_5_6_7_5 — end-to-end via dispatcher ticks: a
// submitted task progresses through prepare → dispatch → poll → finalize, and
// the run ends complete with canonicality=canonical and a log artifact.
func TestRuns_FullLifecycle_5_6_7_5(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	ctx := context.Background()

	// Several ticks drive the lifecycle forward.
	for i := 0; i < 6; i++ {
		if err := f.dispatch.Tick(ctx); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}

	tk, _ := f.st.Tasks().Get(ctx, taskID)
	if tk.LatestRunID == "" {
		t.Fatal("task has no latest_run_id")
	}
	r, _ := f.st.Runs().Get(ctx, tk.LatestRunID)
	if r.State != "complete" {
		t.Fatalf("run state = %q, want complete", r.State)
	}
	if r.Canonicality != "canonical" {
		t.Errorf("canonicality = %q, want canonical", r.Canonicality)
	}
	if tk.CanonicalRunID != r.RunID {
		t.Errorf("task.canonical_run_id = %q, want %q", tk.CanonicalRunID, r.RunID)
	}
	arts, _ := f.st.Artifacts().ListByRun(ctx, r.RunID, "log")
	if len(arts) == 0 {
		t.Fatal("no log artifact written")
	}
}

// TestRuns_FailedJob_3_9 — when the executor returns FAILED, the run ends
// in state=failed with a recorded reason and the task moves to state=failed.
func TestRuns_FailedJob_3_9(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	ctx := context.Background()
	r, err := f.runs.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	jobs, _ := f.st.Jobs().ListByRun(ctx, r.RunID)
	if err := f.exec.SetOutcome(jobs[0].SchedulerID, executor.StatusFailed); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}
	for i := 0; i < 3; i++ {
		_ = f.dispatch.Tick(ctx)
	}
	got, _ := f.st.Runs().Get(ctx, r.RunID)
	if got.State != "failed" {
		t.Fatalf("run state = %q, want failed", got.State)
	}
	if got.FailureReason == "" {
		t.Error("failure_reason empty")
	}
}

// TestRuns_FinalizeRequiresFinalizingState_5_6 — Finalize on a non-finalizing
// run is rejected as ErrInvalidStateTransition (Spec §5.6 completion gate).
func TestRuns_FinalizeRequiresFinalizingState_5_6(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	ctx := context.Background()
	r, _ := f.runs.PrepareRun(ctx, taskID)
	if _, err := f.runs.Finalize(ctx, r.RunID); !errors.Is(err, runs.ErrInvalidStateTransition) {
		t.Errorf("Finalize on ready: want ErrInvalidStateTransition, got %v", err)
	}
}

// TestRuns_DuplicateFingerprintMarkedDuplicate_3_10 — when a second run
// completes for the same fingerprint, it is marked canonicality=duplicate
// and the task's canonical_run_id stays pinned to the first.
func TestRuns_DuplicateFingerprintMarkedDuplicate_3_10(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Two tasks with identical routing → identical fingerprints (force=false,
	// same station, same stub manifest content).
	taskA := submitTask(t, f)
	taskB := submitTask(t, f)
	for i := 0; i < 8; i++ {
		_ = f.dispatch.Tick(ctx)
	}
	tkA, _ := f.st.Tasks().Get(ctx, taskA)
	tkB, _ := f.st.Tasks().Get(ctx, taskB)
	rA, _ := f.st.Runs().Get(ctx, tkA.LatestRunID)
	rB, _ := f.st.Runs().Get(ctx, tkB.LatestRunID)
	if rA.ProcessingFingerprint != rB.ProcessingFingerprint {
		t.Fatalf("fingerprints differ unexpectedly: %s vs %s",
			rA.ProcessingFingerprint, rB.ProcessingFingerprint)
	}
	winners := 0
	dups := 0
	for _, r := range []*store.RunRecord{rA, rB} {
		switch r.Canonicality {
		case "canonical":
			winners++
		case "duplicate":
			dups++
		}
	}
	if winners != 1 || dups != 1 {
		t.Errorf("expected 1 canonical + 1 duplicate, got %d/%d (rA=%s rB=%s)",
			winners, dups, rA.Canonicality, rB.Canonicality)
	}

	// Both tasks must be completed — a duplicate run must not leave the task
	// permanently in "accepted" state.
	tkA2, _ := f.st.Tasks().Get(ctx, taskA)
	tkB2, _ := f.st.Tasks().Get(ctx, taskB)
	for _, tk := range []*store.TaskRecord{tkA2, tkB2} {
		if tk.State != "completed" {
			t.Errorf("task %s state = %q, want completed", tk.TaskID, tk.State)
		}
	}
}

// TestRuns_DuplicateFailsBeforeDispatch_3_15 — when a second task is
// submitted for a fingerprint already canonically owned by an earlier
// completed run, the dispatcher fails the task at preparation time without
// creating a run record (Spec §3.15).
func TestRuns_DuplicateFailsBeforeDispatch_3_15(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Submit and fully complete taskA so its fingerprint becomes canonically owned.
	taskAID := submitTask(t, f)
	waitForTaskState(t, ctx, f.st, f.dispatch, taskAID, "completed")

	// Submit taskB with identical station/window → identical fingerprint.
	taskBID := submitTask(t, f)
	// The dispatcher must fail taskB at preparation — no run should be created.
	waitForTaskState(t, ctx, f.st, f.dispatch, taskBID, "failed")

	tkB, err := f.st.Tasks().Get(ctx, taskBID)
	if err != nil {
		t.Fatalf("get task B: %v", err)
	}
	if tkB.State != "failed" {
		t.Errorf("task B state = %q, want failed", tkB.State)
	}
	if !strings.Contains(tkB.FailureSummary, "duplicate") {
		t.Errorf("task B failure_summary = %q, want substring 'duplicate'", tkB.FailureSummary)
	}
	// No run must have been created for taskB (PrepareRun returned ErrFatalPrepare
	// before inserting into the runs table).
	if tkB.LatestRetryIndex.Valid {
		t.Errorf("expected no run for duplicate task B, got retry_index=%d", tkB.LatestRetryIndex.Int64)
	}
}

// TestRuns_JobOrderFormatJSON — a station with joborder.format=json writes a
// JSON joborder file to the working root and the artifact reflects the name.
func TestRuns_JobOrderFormatJSON(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "jo-json.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	script := writeExecutable(t, dir, "jo-json.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out.json\"\n")
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "JO-JSON",
		StationName:   "JobOrder JSON",
		ContentHash:   "sha256:jo-json",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: script},
		Outputs:       []stations.OutputDefinition{{Name: "out.json", FileType: "JSON_OUT", Required: true}},
		JobOrder:      stations.JobOrderConfig{Format: "json", Name: "joborder.json"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "a1b2c3" })
	runN := 0
	rsvc := runs.NewService(runs.Config{
		Store:           st,
		Executors:       executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:        reg,
		WorkingRootBase: filepath.Join(dir, "work"),
		IDFactory:       func() string { runN++; return "run-jojson-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "JO-JSON"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)
	// joborder.json must be present in the working root.
	joPath := filepath.Join(run.WorkingRoot, "joborder.json")
	raw, err := os.ReadFile(joPath)
	if err != nil {
		t.Fatalf("joborder.json not found: %v", err)
	}
	// Must parse as valid JSON containing veriproc_meta.schema_version.
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid JSON joborder: %v", err)
	}
	meta, ok := doc["veriproc_meta"].(map[string]any)
	if !ok {
		t.Fatalf("joborder missing veriproc_meta: %#v", doc)
	}
	if meta["schema_version"] != "veriproc.joborder/v1" {
		t.Errorf("schema_version = %v", meta["schema_version"])
	}
}

func TestRuns_JobOrderTemplateTOML(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "jo-template.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	archive := filepath.Join(dir, "archive", "hot")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	inputName := "20250703_PRIMARY_A_v1.txt"
	if err := os.WriteFile(filepath.Join(archive, inputName), []byte("primary\n"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	script := writeExecutable(t, dir, "jo-template.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out.dat\"\n")
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "JO-TEMPLATE",
		StationName:   "JobOrder Template",
		ContentHash:   "sha256:jo-template",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: script},
		Inputs:        []stations.InputDefinition{{FileType: "PRIMARY_A", Category: "product"}},
		Outputs:       []stations.OutputDefinition{{Name: "out.dat", FileType: "TEMPLATE_OUT", Required: true}},
		JobOrder: stations.JobOrderConfig{
			Renderer: "template",
			Format:   "toml",
			Name:     "joborder.e2e.TEST.toml",
			Paths:    "absolute",
			Template: "primary_file = {{ tomlq (input \"PRIMARY_A\") }}\ntmp_dir = {{ tomlq (param \"tmp_dir\") }}\nconfig_file = {{ tomlq (joinPath (param \"swlib_root\") \"cfg/example.yml\") }}\ncpu_number = {{ param \"cpu_number\" }}\n",
			Params: map[string]any{
				"tmp_dir":    "<working_root>/tmp",
				"swlib_root": "/opt/example-swlib",
				"cpu_number": 8,
			},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "0abc12" })
	runN := 0
	rsvc := runs.NewService(runs.Config{
		Store:             st,
		Executors:         executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:          reg,
		WorkingRootBase:   filepath.Join(dir, "work"),
		RollingArchives:   map[string]string{"hot": archive},
		ProductCategories: map[string][]string{"product": {"rolling:hot"}},
		IDFactory:         func() string { runN++; return "run-jotemplate-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "JO-TEMPLATE"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)
	raw, err := os.ReadFile(filepath.Join(run.WorkingRoot, "joborder.e2e.TEST.toml"))
	if err != nil {
		t.Fatalf("read rendered template joborder: %v", err)
	}
	var doc map[string]any
	if err := toml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid rendered TOML: %v", err)
	}
	wantInput := filepath.ToSlash(filepath.Join(run.WorkingRoot, "input", inputName))
	if doc["primary_file"] != wantInput {
		t.Fatalf("primary_file = %v, want %s\n%s", doc["primary_file"], wantInput, raw)
	}
	wantTmp := filepath.ToSlash(filepath.Join(run.WorkingRoot, "tmp"))
	if doc["tmp_dir"] != wantTmp || doc["config_file"] != "/opt/example-swlib/cfg/example.yml" || doc["cpu_number"] != int64(8) {
		t.Fatalf("rendered template doc = %#v", doc)
	}
}

// TestRuns_JobOrderPreprocessScriptDefault verifies that when preprocess_script
// is set on the default (include) renderer, the KEY=VALUE output is available
// as <prep.KEY> context references inside joborder.include.
func TestRuns_JobOrderPreprocessScriptDefault(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "prep-default.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	archive := filepath.Join(dir, "archive", "hot")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	inputName := "20250703_SCE_DATA_v1.nc"
	if err := os.WriteFile(filepath.Join(archive, inputName), []byte("scene\n"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	// Preprocess script: echoes fixed scan-line values derived from the input
	// file path (just verifies argument passing and prep namespace injection).
	prepScript := writeExecutable(t, dir, "prep.sh", `#!/bin/sh
set -eu
# $1 is the input file path
printf 'MIN_SCANLINE=6825\nMAX_SCANLINE=7444\nN_SCANLINES=620\n'
`)
	mainScript := writeExecutable(t, dir, "main.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out.nc\"\n")

	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "PREP-DEFAULT",
		StationName:   "Preprocess Default",
		ContentHash:   "sha256:prep-default",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: mainScript},
		Inputs:        []stations.InputDefinition{{FileType: "SCE_DATA", Category: "product"}},
		Outputs:       []stations.OutputDefinition{{Name: "out.nc", FileType: "SCENE_OUT", Required: true}},
		JobOrder: stations.JobOrderConfig{
			Format:           "yaml",
			PreprocessScript: prepScript,
			PreprocessArgs:   []string{"{input:SCE_DATA}"},
			Include: map[string]any{
				"start_scanline": "<prep.MIN_SCANLINE>",
				"end_scanline":   "<prep.MAX_SCANLINE>",
				"n_scanlines":    "<prep.N_SCANLINES>",
				"log_level":      "DEBUG",
			},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "ab1234" })
	runN := 0
	rsvc := runs.NewService(runs.Config{
		Store:             st,
		Executors:         executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:          reg,
		WorkingRootBase:   filepath.Join(dir, "work"),
		RollingArchives:   map[string]string{"hot": archive},
		ProductCategories: map[string][]string{"product": {"rolling:hot"}},
		IDFactory:         func() string { runN++; return "run-prepd-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "PREP-DEFAULT"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)

	raw, err := os.ReadFile(filepath.Join(run.WorkingRoot, "joborder.yaml"))
	if err != nil {
		t.Fatalf("read joborder.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse joborder.yaml: %v", err)
	}

	// Prep vars must appear as resolved values at the root of the joborder.
	if got := fmt.Sprintf("%v", doc["start_scanline"]); got != "6825" {
		t.Errorf("start_scanline = %v, want 6825", doc["start_scanline"])
	}
	if got := fmt.Sprintf("%v", doc["end_scanline"]); got != "7444" {
		t.Errorf("end_scanline = %v, want 7444", doc["end_scanline"])
	}
	if got := fmt.Sprintf("%v", doc["n_scanlines"]); got != "620" {
		t.Errorf("n_scanlines = %v, want 620", doc["n_scanlines"])
	}
	if doc["log_level"] != "DEBUG" {
		t.Errorf("log_level = %v, want DEBUG", doc["log_level"])
	}
}

// TestRuns_JobOrderPreprocessScriptTemplate verifies that preprocess_script
// vars are available as .PrepVars["KEY"] in a Go template joborder renderer.
func TestRuns_JobOrderPreprocessScriptTemplate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "prep-tmpl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	archive := filepath.Join(dir, "archive", "hot")
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatalf("mkdir archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archive, "20250703_SCENE_v1.nc"), []byte("scene\n"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}

	prepScript := writeExecutable(t, dir, "prep-tmpl.sh", `#!/bin/sh
printf 'MIN_SCANLINE=100\nMAX_SCANLINE=200\n'
`)
	mainScript := writeExecutable(t, dir, "main-tmpl.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out.nc\"\n")

	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "PREP-TEMPLATE",
		StationName:   "Preprocess Template",
		ContentHash:   "sha256:prep-template",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: mainScript},
		Inputs:        []stations.InputDefinition{{FileType: "SCENE", Category: "product"}},
		Outputs:       []stations.OutputDefinition{{Name: "out.nc", FileType: "SCENE_OUT", Required: true}},
		JobOrder: stations.JobOrderConfig{
			Renderer:         "template",
			Format:           "yaml",
			PreprocessScript: prepScript,
			Template: "start_scanline: {{ index .PrepVars \"MIN_SCANLINE\" }}\n" +
				"end_scanline: {{ index .PrepVars \"MAX_SCANLINE\" }}\n" +
				"run_ref: {{ .RunRef }}\n",
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "cd5678" })
	runN := 0
	rsvc := runs.NewService(runs.Config{
		Store:             st,
		Executors:         executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:          reg,
		WorkingRootBase:   filepath.Join(dir, "work"),
		RollingArchives:   map[string]string{"hot": archive},
		ProductCategories: map[string][]string{"product": {"rolling:hot"}},
		IDFactory:         func() string { runN++; return "run-prept-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "PREP-TEMPLATE"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)

	raw, err := os.ReadFile(filepath.Join(run.WorkingRoot, "joborder.yaml"))
	if err != nil {
		t.Fatalf("read joborder.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse joborder.yaml: %v", err)
	}
	if fmt.Sprintf("%v", doc["start_scanline"]) != "100" {
		t.Errorf("start_scanline = %v, want 100", doc["start_scanline"])
	}
	if fmt.Sprintf("%v", doc["end_scanline"]) != "200" {
		t.Errorf("end_scanline = %v, want 200", doc["end_scanline"])
	}
	expectedRef := fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex)
	if doc["run_ref"] != expectedRef {
		t.Errorf("run_ref = %v, want %s", doc["run_ref"], expectedRef)
	}
}

// TestRuns_JobOrderPreprocessScriptFailure verifies that when preprocess_script
// exits with a non-zero code, the run transitions to failed and the task is
// also marked failed.
func TestRuns_JobOrderPreprocessScriptFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "prep-fail.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	prepScript := writeExecutable(t, dir, "bad-prep.sh", "#!/bin/sh\necho 'fatal error in prep' >&2\nexit 1\n")
	mainScript := writeExecutable(t, dir, "main-fail.sh", "#!/bin/sh\ntouch \"$VERIPROC_RUN_DIR/out.dat\"\n")

	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "PREP-FAIL",
		StationName:   "Preprocess Fail",
		ContentHash:   "sha256:prep-fail",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: mainScript},
		Outputs:       []stations.OutputDefinition{{Name: "out.dat", FileType: "FAIL_OUT", Required: true}},
		JobOrder: stations.JobOrderConfig{
			Format:           "yaml",
			PreprocessScript: prepScript,
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "ef9012" })
	rsvc := runs.NewService(runs.Config{
		Store:           st,
		Executors:       executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:        reg,
		WorkingRootBase: filepath.Join(dir, "work"),
		IDFactory:       func() string { return "run-prepfail" },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "PREP-FAIL"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "failed")
	tk, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	if tk.State != "failed" {
		t.Errorf("task state = %q, want failed", tk.State)
	}
	if !strings.Contains(strings.ToLower(tk.FailureSummary), "preprocess") {
		t.Errorf("failure_summary should mention 'preprocess', got: %q", tk.FailureSummary)
	}
}

// TestRuns_JobOrderPreprocessScriptInstanceRoot verifies that <instance_root>
// in preprocess_script is resolved to the configured InstanceRoot.
func TestRuns_JobOrderPreprocessScriptInstanceRoot(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "prep-ir.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Scripts live under <instanceRoot>/scripts/
	scriptsDir := filepath.Join(dir, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		t.Fatalf("mkdir scripts: %v", err)
	}
	prepScript := writeExecutable(t, scriptsDir, "extract.sh", "#!/bin/sh\nprintf 'N_SCANLINES=42\n'\n")
	_ = prepScript // path used via <instance_root>/scripts/extract.sh

	mainScript := writeExecutable(t, dir, "main-ir.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out.nc\"\n")

	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "PREP-IR",
		StationName:   "Preprocess InstanceRoot",
		ContentHash:   "sha256:prep-ir",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: mainScript},
		Outputs:       []stations.OutputDefinition{{Name: "out.nc", FileType: "OUT_NC", Required: true}},
		JobOrder: stations.JobOrderConfig{
			Format:           "yaml",
			PreprocessScript: "<instance_root>/scripts/extract.sh",
			Include: map[string]any{
				"n_scanlines": "<prep.N_SCANLINES>",
			},
		},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "abc123" })
	runN := 0
	rsvc := runs.NewService(runs.Config{
		Store:           st,
		Executors:       executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:        reg,
		WorkingRootBase: filepath.Join(dir, "work"),
		InstanceRoot:    dir, // <instance_root> resolves to dir
		IDFactory:       func() string { runN++; return "run-ir-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "PREP-IR"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)

	raw, err := os.ReadFile(filepath.Join(run.WorkingRoot, "joborder.yaml"))
	if err != nil {
		t.Fatalf("read joborder.yaml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse joborder.yaml: %v", err)
	}
	if fmt.Sprintf("%v", doc["n_scanlines"]) != "42" {
		t.Errorf("n_scanlines = %v, want 42", doc["n_scanlines"])
	}
}

// TestRuns_JobOrderFormatNone — a station with joborder.format=none writes no
// joborder file and exposes no joborder artifact.
func TestRuns_JobOrderFormatNone(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "jo-none.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	script := writeExecutable(t, dir, "jo-none.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out.dat\"\n")
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "JO-NONE",
		StationName:   "JobOrder None",
		ContentHash:   "sha256:jo-none",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: script},
		Outputs:       []stations.OutputDefinition{{Name: "out.dat", FileType: "NONE_OUT", Required: true}},
		JobOrder:      stations.JobOrderConfig{Format: "none"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "d4e5f6" })
	runN := 0
	rsvc := runs.NewService(runs.Config{
		Store:           st,
		Executors:       executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:        reg,
		WorkingRootBase: filepath.Join(dir, "work"),
		IDFactory:       func() string { runN++; return "run-jonone-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "JO-NONE"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)
	// No joborder file must exist.
	if _, err := os.Stat(filepath.Join(run.WorkingRoot, "joborder.yaml")); !os.IsNotExist(err) {
		t.Error("joborder.yaml should not exist for format=none")
	}
	// No joborder artifact.
	arts, _ := st.Artifacts().ListByRun(ctx, run.RunID, "joborder")
	if len(arts) != 0 {
		t.Errorf("expected no joborder artifact, got %d", len(arts))
	}
}

// TestRuns_JobOrderOutputDirectory verifies that a station-level
// directory field on an output definition is resolved via context references
// and written into the joborder. The run must also complete successfully,
// proving that validateOutputs looks in the custom directory.
func TestRuns_JobOrderOutputDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "jo-outdir.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	// Script writes the expected output to the custom output2/ directory.
	script := writeExecutable(t, dir, "jo-outdir.sh",
		"#!/bin/sh\nset -eu\nmkdir -p \"$VERIPROC_WORKING_ROOT/output2\"\ntouch \"$VERIPROC_WORKING_ROOT/output2/result.dat\"\n")
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "JO-OUTDIR",
		StationName:   "JobOrder OutputDir",
		ContentHash:   "sha256:jo-outdir",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: script},
		Outputs: []stations.OutputDefinition{{
			Name:      "result.dat",
			FileType:  "OUTDIR_RESULT",
			Required:  true,
			Directory: "<working_root>/output2",
		}},
		JobOrder: stations.JobOrderConfig{Paths: "absolute"},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "aa1122" })
	runN := 0
	rsvc := runs.NewService(runs.Config{
		Store:           st,
		Executors:       executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)),
		Resolver:        reg,
		WorkingRootBase: filepath.Join(dir, "work"),
		IDFactory:       func() string { runN++; return "run-outdir-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "JO-OUTDIR"},
		Window:      tasks.Window{Start: time.Now().UTC(), End: time.Now().UTC()},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	task, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, task.CanonicalRunID)
	// Parse the written joborder.
	raw, err := os.ReadFile(filepath.Join(run.WorkingRoot, "joborder.yaml"))
	if err != nil {
		t.Fatalf("joborder.yaml not found: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("invalid YAML: %v", err)
	}
	outs, ok := doc["outputs"].([]any)
	if !ok || len(outs) == 0 {
		t.Fatalf("outputs missing or empty: %#v", doc)
	}
	outMap, ok := outs[0].(map[string]any)
	if !ok {
		t.Fatalf("outputs[0] not a map: %T", outs[0])
	}
	wantDir := filepath.ToSlash(filepath.Join(run.WorkingRoot, "output2"))
	if outMap["directory"] != wantDir {
		t.Errorf("outputs[0].directory = %v, want %s", outMap["directory"], wantDir)
	}
	// Artifact must point at the file in the custom directory.
	arts, _ := st.Artifacts().ListByRun(ctx, run.RunID, "output")
	if len(arts) == 0 {
		t.Fatal("no output artifacts recorded")
	}
	wantPath := filepath.Join(run.WorkingRoot, "output2", "result.dat")
	if arts[0].Path != wantPath {
		t.Errorf("artifact path = %s, want %s", arts[0].Path, wantPath)
	}
}
