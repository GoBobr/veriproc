package runs_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/runs"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
	"github.com/eum/veriproc/internal/tasks"
)

// TestTaskHistory_RootTask_2_6_1 — a root task (no parent, no history) must
// produce a task.yaml with the correct schema fields and no parent/history
// section (Spec §2.6.1).
func TestTaskHistory_RootTask_2_6_1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "hist-root.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	script := writeExecutable(t, dir, "root.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/result.dat\"\n")
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st, stations.Spec{
		StationID:     "HIST-A",
		StationName:   "Hist Station A",
		ContentHash:   "sha256:hist-a",
		SchemaVersion: "veriproc.station/v1",
		Execution:     stations.Execution{Executable: script},
		Outputs:       []stations.OutputDefinition{{Name: "result.dat", FileType: "RESULT", Required: true}},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	now := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	runN := 0
	tsvc := tasks.NewService(st, reg, fixedClock(now), func() string { return "000001" })
	rsvc := runs.NewService(runs.Config{
		Store: st, Executor: executor.NewLocalExecutor(nil), Resolver: reg,
		WorkingRootBase: filepath.Join(dir, "work"),
		Clock:           fixedClock(now),
		IDFactory:       func() string { runN++; return "run-root-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())

	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "HIST-A"},
		Window:      tasks.Window{Start: now.Add(-15 * time.Minute), End: now},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")

	tk, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	run, _ := st.Runs().Get(ctx, tk.CanonicalRunID)

	raw, err := os.ReadFile(filepath.Join(run.WorkingRoot, "task.yaml"))
	if err != nil {
		t.Fatalf("task.yaml not written: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse task.yaml: %v", err)
	}

	// Required top-level fields.
	if doc["schema_version"] != "veriproc.task/v1" {
		t.Errorf("schema_version = %v, want veriproc.task/v1", doc["schema_version"])
	}
	if doc["task_id"] != res.Task.TaskID {
		t.Errorf("task_id = %v, want %s", doc["task_id"], res.Task.TaskID)
	}
	wantRunRef := res.Task.TaskID + "/r0"
	if doc["run_ref"] != wantRunRef {
		t.Errorf("run_ref = %v, want %s", doc["run_ref"], wantRunRef)
	}
	dest, ok := doc["destination"].(map[string]any)
	if !ok || dest["station_id"] != "HIST-A" {
		t.Errorf("destination.station_id = %v", doc["destination"])
	}
	window, ok := doc["window"].(map[string]any)
	if !ok || window["start"] == nil || window["end"] == nil {
		t.Errorf("window = %v", doc["window"])
	}
	if doc["created_at"] == nil {
		t.Error("created_at missing")
	}

	// Root task must not have a parent or history section.
	if doc["parent"] != nil {
		t.Errorf("root task.yaml must not have parent section, got: %v", doc["parent"])
	}
	if doc["history"] != nil {
		t.Errorf("root task.yaml must not have history, got: %v", doc["history"])
	}

	// Artifact must be recorded in the store.
	arts, _ := st.Artifacts().ListByRun(ctx, run.RunID, "task_history")
	if len(arts) != 1 || arts[0].FileType != "TASK_HISTORY" {
		t.Errorf("task_history artifact = %#v", arts)
	}
}

// TestTaskHistory_DownstreamTask_2_6_1 — a downstream task must carry the
// parent run_ref and a history chain entry for the upstream run (Spec §2.6.1).
func TestTaskHistory_DownstreamTask_2_6_1(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "hist-ds.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	scriptA := writeExecutable(t, dir, "hist-a.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out-a.dat\"\n")
	scriptB := writeExecutable(t, dir, "hist-b.sh", "#!/bin/sh\nset -eu\ntouch \"$VERIPROC_RUN_DIR/out-b.dat\"\n")
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st,
		stations.Spec{
			StationID:     "H-ALPHA",
			StationName:   "History Alpha",
			ContentHash:   "sha256:h-alpha",
			SchemaVersion: "veriproc.station/v1",
			Execution:     stations.Execution{Executable: scriptA},
			Outputs:       []stations.OutputDefinition{{Name: "out-a.dat", FileType: "H_ALPHA_OUT", Required: true}},
			Downstream:    []stations.DownstreamTarget{{StationID: "H-BETA"}},
		},
		stations.Spec{
			StationID:     "H-BETA",
			StationName:   "History Beta",
			ContentHash:   "sha256:h-beta",
			SchemaVersion: "veriproc.station/v1",
			Execution:     stations.Execution{Executable: scriptB},
			Outputs:       []stations.OutputDefinition{{Name: "out-b.dat", FileType: "H_BETA_OUT", Required: true}},
		},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}

	now := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	runN := 0
	tsvc := tasks.NewService(st, reg, fixedClock(now), func() string { return "000002" })
	rsvc := runs.NewService(runs.Config{
		Store: st, Executor: executor.NewLocalExecutor(nil), Resolver: reg,
		WorkingRootBase: filepath.Join(dir, "work"),
		Clock:           fixedClock(now),
		IDFactory:       func() string { runN++; return "run-ds-" + strconv.Itoa(runN) },
	})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())

	res, err := tsvc.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "H-ALPHA"},
		Window:      tasks.Window{Start: now.Add(-15 * time.Minute), End: now},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	// Wait for H-ALPHA to complete and H-BETA to be created.
	waitForTaskCount(t, ctx, st, disp, 2)
	// Wait for H-BETA to complete.
	page, _ := st.Tasks().List(ctx, store.ListFilter{Limit: 10})
	var betaTaskID string
	for _, tk := range page.Items {
		if tk.DestinationStationID == "H-BETA" {
			betaTaskID = tk.TaskID
		}
	}
	if betaTaskID == "" {
		t.Fatalf("H-BETA task not created")
	}
	waitForTaskState(t, ctx, st, disp, betaTaskID, "completed")

	// Fetch the alpha run to know its run_ref.
	alphaTk, _ := st.Tasks().Get(ctx, res.Task.TaskID)
	alphaRun, _ := st.Runs().Get(ctx, alphaTk.CanonicalRunID)
	alphaRunRef := res.Task.TaskID + "/r0"

	// Read and parse B's task.yaml.
	betaTk, _ := st.Tasks().Get(ctx, betaTaskID)
	betaRun, _ := st.Runs().Get(ctx, betaTk.CanonicalRunID)
	raw, err := os.ReadFile(filepath.Join(betaRun.WorkingRoot, "task.yaml"))
	if err != nil {
		t.Fatalf("beta task.yaml not written: %v", err)
	}
	_ = alphaRun // confirm it was queried

	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse beta task.yaml: %v", err)
	}

	// Beta task_id / run_ref.
	if doc["task_id"] != betaTaskID {
		t.Errorf("task_id = %v, want %s", doc["task_id"], betaTaskID)
	}
	wantBetaRunRef := betaTaskID + "/r0"
	if doc["run_ref"] != wantBetaRunRef {
		t.Errorf("run_ref = %v, want %s", doc["run_ref"], wantBetaRunRef)
	}

	// Parent must carry alpha's run_ref.
	parent, ok := doc["parent"].(map[string]any)
	if !ok {
		t.Fatalf("downstream task.yaml missing parent section, got: %v", doc["parent"])
	}
	if parent["run_ref"] != alphaRunRef {
		t.Errorf("parent.run_ref = %v, want %s", parent["run_ref"], alphaRunRef)
	}

	// History must contain exactly one entry for alpha.
	history, ok := doc["history"].([]any)
	if !ok || len(history) != 1 {
		t.Fatalf("history = %v, want 1 entry", doc["history"])
	}
	entry, ok := history[0].(map[string]any)
	if !ok {
		t.Fatalf("history[0] not a map: %v", history[0])
	}
	if entry["station_id"] != "H-ALPHA" {
		t.Errorf("history[0].station_id = %v, want H-ALPHA", entry["station_id"])
	}
	if entry["run_ref"] != alphaRunRef {
		t.Errorf("history[0].run_ref = %v, want %s", entry["run_ref"], alphaRunRef)
	}
	if entry["completed_at"] == nil || entry["completed_at"] == "" {
		t.Error("history[0].completed_at missing")
	}
	if entry["summary"] == nil || entry["summary"] == "" {
		t.Error("history[0].summary missing")
	}
}
