package runs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/gobobr/veriproc/internal/executor"
	"github.com/gobobr/veriproc/internal/runs"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/gobobr/veriproc/internal/tasks"
)

// TestRuns_TaskOutInputsPayload_StageManifestJobOrder covers the full Part B
// flow: a station writes task-out.yaml with an `inputs:` payload for a
// downstream station whose input declares `source: upstream`. The created
// task must carry the payload; run preparation must stage the files as
// symlinks into <working_root>/input/; the manifest must list them; and the
// default joborder renderer must emit inputs: [{file_type, files}] with no
// renderer changes.
func TestRuns_TaskOutInputsPayload_StageManifestJobOrder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "t.db"))
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
	inputFile := filepath.Join(archive, "VNP10A1.A2026298.h32v11.fake.h5")
	if err := os.WriteFile(inputFile, []byte("viirs granule\n"), 0o644); err != nil {
		t.Fatalf("write upstream input: %v", err)
	}

	scriptA := writeExecutable(t, dir, "station-a.sh", fmt.Sprintf(`#!/bin/sh
set -eu
mkdir -p "$VERIPROC_RUN_DIR"
printf 'out\n' > "$VERIPROC_RUN_DIR/result-a.json"
cat > "$VERIPROC_WORKING_ROOT/task-out.yaml" <<EOF
schema_version: veriproc.task-out/v1
downstream:
  - key: day-298
    station_id: STATION-B
    window:
      start: "2025-07-03T00:00:00Z"
      end: "2025-07-03T23:59:59Z"
    inputs:
      - file_type: GRANULE_H5
        files:
          - %s
EOF
`, inputFile))
	scriptB := writeExecutable(t, dir, "station-b.sh", `#!/bin/sh
set -eu
ls input/
printf 'b\n' > "$VERIPROC_RUN_DIR/result-b.json"
`)

	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st,
		stations.Spec{StationID: "STATION-A", StationName: "A_PROC", ContentHash: "sha256:station-a", SchemaVersion: "veriproc.station/v1", Outputs: []stations.OutputDefinition{{Name: "result-a.json", FileType: "A_RESULT", Required: true, Publish: nil}}, Downstream: []stations.DownstreamTarget{{StationID: "STATION-B", Mode: "task_out"}}, Execution: stations.Execution{Executable: scriptA}},
		stations.Spec{StationID: "STATION-B", StationName: "B_PROC", ContentHash: "sha256:station-b", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "GRANULE_H5", Category: "product", Source: "upstream"}}, Outputs: []stations.OutputDefinition{{Name: "result-b.json", FileType: "B_RESULT", Required: true}}, Execution: stations.Execution{Executable: scriptB}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "0000ab" })
	runN := 0
	rsvc := runs.NewService(runs.Config{Store: st, Executors: executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), RollingArchives: map[string]string{"hot": archive}, ProductCategories: map[string][]string{"product": {"rolling:hot"}}, IDFactory: func() string { runN++; return "run-up-" + fmt.Sprint(runN) }})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "STATION-A"}, Window: tasks.Window{Start: time.Date(2025, 7, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 7, 3, 23, 59, 59, 0, time.UTC)}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	waitForTaskCount(t, ctx, st, disp, 2)

	bTaskID := findTaskByStation(t, ctx, st, "STATION-B")
	// B1/B2: payload persisted with the created task. Wait until the
	// dispatcher has prepared the downstream run.
	bCompleted := false
	for i := 0; i < 500; i++ {
		disp.Tick(ctx)
		tk, err := st.Tasks().Get(ctx, bTaskID)
		if err == nil && tk.State == "completed" {
			bCompleted = true
			break
		}
		if err == nil && tk.State == "failed" {
			if tk.LatestRetryIndex.Valid {
				if r, rerr := st.Runs().GetByTaskRetry(ctx, bTaskID, int(tk.LatestRetryIndex.Int64)); rerr == nil {
					bTask, _ := st.Tasks().Get(ctx, bTaskID)
					errLog, _ := os.ReadFile(filepath.Join(r.WorkingRoot, "logs", "run_err.log"))
					outLog, _ := os.ReadFile(filepath.Join(r.WorkingRoot, "logs", "run_out.log"))
					inputs, _ := os.ReadDir(filepath.Join(r.WorkingRoot, "input"))
					names := []string{}
					for _, e := range inputs {
						names = append(names, e.Name())
					}
					t.Logf("payload=%s", string(bTask.DeclaredInputs))
					t.Logf("input dir=%v errlog=%q outlog=%q", names, errLog, outLog)
					t.Fatalf("downstream failed: %s (task failure_summary=%q)", r.FailureReason, tk.FailureSummary)
				}
			}
			t.Fatalf("downstream failed without run: %q", tk.FailureSummary)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !bCompleted {
		t.Fatalf("downstream task did not complete")
	}
	bTask, err := st.Tasks().Get(ctx, bTaskID)
	if err != nil {
		t.Fatalf("get b task: %v", err)
	}
	if len(bTask.DeclaredInputs) == 0 {
		t.Fatalf("downstream task has no declared_inputs payload")
	}
	var payload []struct {
		FileType string   `json:"file_type"`
		Files    []string `json:"files"`
	}
	if err := json.Unmarshal(bTask.DeclaredInputs, &payload); err != nil {
		t.Fatalf("parse declared_inputs: %v", err)
	}
	if len(payload) != 1 || payload[0].FileType != "GRANULE_H5" || len(payload[0].Files) != 1 || payload[0].Files[0] != inputFile {
		t.Fatalf("declared_inputs payload = %#v", payload)
	}
	if !bTask.LatestRetryIndex.Valid {
		t.Fatalf("downstream task has no run")
	}
	bRun, err := st.Runs().GetByTaskRetry(ctx, bTaskID, int(bTask.LatestRetryIndex.Int64))
	if err != nil {
		t.Fatalf("get b run: %v", err)
	}

	// B4: staged symlink in <working_root>/input/.
	mf, err := st.Manifests().GetByRun(ctx, bRun.RunID)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	staged := 0
	for _, e := range mf.Entries {
		if e.FileType != "GRANULE_H5" || !e.Present {
			continue
		}
		staged++
		if !strings.HasPrefix(e.Path, "input/") {
			t.Fatalf("manifest entry path = %q, want input/ staging", e.Path)
		}
		link := filepath.Join(bRun.WorkingRoot, filepath.FromSlash(e.Path))
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatalf("staged input missing: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("staged input %s is not a symlink", link)
		}
		target, err := os.Readlink(link)
		if err != nil || target != inputFile {
			t.Fatalf("symlink target = %q (%v), want %q", target, err, inputFile)
		}
		if e.SelectionReason == "" {
			t.Fatalf("manifest entry missing SelectionReason: %#v", e)
		}
	}
	if staged != 1 {
		t.Fatalf("staged upstream entries = %d, want 1 (manifest: %#v)", staged, mf.Entries)
	}

	// B5: default renderer emits inputs: [{file_type, files: [./input/...]}]
	// from the manifest with no renderer changes.
	jobOrderRaw, err := os.ReadFile(filepath.Join(bRun.WorkingRoot, "joborder.yaml"))
	if err != nil {
		t.Fatalf("read joborder: %v", err)
	}
	var jobOrder map[string]any
	if err := yaml.Unmarshal(jobOrderRaw, &jobOrder); err != nil {
		t.Fatalf("parse joborder: %v", err)
	}
	inputs, ok := jobOrder["inputs"].([]any)
	if !ok || len(inputs) != 1 {
		t.Fatalf("joborder inputs = %#v, want one upstream entry", jobOrder["inputs"])
	}
	in := inputs[0].(map[string]any)
	if in["file_type"] != "GRANULE_H5" {
		t.Fatalf("joborder input file_type = %#v", in)
	}
	files, ok := in["files"].([]any)
	if !ok || len(files) != 1 {
		t.Fatalf("joborder input files = %#v", in)
	}
	if fstr, ok := files[0].(string); !ok || !strings.HasPrefix(fstr, "input/") && !strings.HasPrefix(fstr, "./input/") {
		t.Fatalf("joborder input file = %#v, want staged input path", files[0])
	}
}

// TestRuns_TaskOutInputsNegative covers task-out validation failures:
// a listed file that does not exist, and a file_type not declared by the
// target station. Both must fail the parent run finalization with a clear
// message rather than creating a downstream task.
func TestRuns_TaskOutInputsNegative(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inputs  string
		wantErr string
	}{
		{"missing file", `
      - file_type: GRANULE_H5
        files:
          - /nonexistent/path/granule.h5
`, "no such file"},
		{"undeclared file_type", `
      - file_type: NOT_DECLARED
        files:
          - /etc/hostname
`, "not declared as an input"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			st, err := store.Open("sqlite://" + filepath.Join(dir, "t.db"))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })
			if err := store.Migrate(ctx, st); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			scriptA := writeExecutable(t, dir, "station-a.sh", fmt.Sprintf(`#!/bin/sh
set -eu
mkdir -p "$VERIPROC_RUN_DIR"
printf 'out\n' > "$VERIPROC_RUN_DIR/result-a.json"
cat > "$VERIPROC_WORKING_ROOT/task-out.yaml" <<EOF
schema_version: veriproc.task-out/v1
downstream:
  - key: k
    station_id: STATION-B
    window:
      start: "2025-07-03T00:00:00Z"
      end: "2025-07-03T23:59:59Z"
    inputs:%s
EOF
`, tc.inputs))
			scriptB := writeExecutable(t, dir, "station-b.sh", `#!/bin/sh
printf 'b\n' > "$VERIPROC_RUN_DIR/result-b.json"
`)
			reg := stations.NewRegistry()
			if err := reg.Seed(ctx, st,
				stations.Spec{StationID: "STATION-A", StationName: "A_PROC", ContentHash: "sha256:station-a", SchemaVersion: "veriproc.station/v1", Outputs: []stations.OutputDefinition{{Name: "result-a.json", FileType: "A_RESULT", Required: true}}, Downstream: []stations.DownstreamTarget{{StationID: "STATION-B", Mode: "task_out"}}, Execution: stations.Execution{Executable: scriptA}},
				stations.Spec{StationID: "STATION-B", StationName: "B_PROC", ContentHash: "sha256:station-b", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "GRANULE_H5", Category: "product", Source: "upstream"}}, Execution: stations.Execution{Executable: scriptB}},
			); err != nil {
				t.Fatalf("seed: %v", err)
			}
			tsvc := tasks.NewService(st, reg, nil, func() string { return "0000ac" })
			runN := 0
			rsvc := runs.NewService(runs.Config{Store: st, Executors: executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), ProductCategories: map[string][]string{}, IDFactory: func() string { runN++; return "run-neg-" + fmt.Sprint(runN) }})
			disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
			res, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "STATION-A"}, Window: tasks.Window{Start: time.Date(2025, 7, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 7, 3, 23, 59, 59, 0, time.UTC)}})
			if err != nil {
				t.Fatalf("submit: %v", err)
			}
			// Parent run must fail with the validation error, and no
			// downstream task may be created.
			waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "failed")
			waitForTaskCount(t, ctx, st, disp, 1)
			taskRec, _ := st.Tasks().Get(ctx, res.Task.TaskID)
			if !taskRec.LatestRetryIndex.Valid {
				t.Fatalf("task has no run")
			}
			run, err := st.Runs().GetByTaskRetry(ctx, res.Task.TaskID, int(taskRec.LatestRetryIndex.Int64))
			if err != nil {
				t.Fatalf("get run: %v", err)
			}
			if run.State != "failed" || !strings.Contains(run.FailureReason, tc.wantErr) {
				t.Fatalf("run state = %s failure = %q, want failure containing %q", run.State, run.FailureReason, tc.wantErr)
			}
			_ = taskRec
		})
	}
}

// TestRuns_UpstreamInputWithoutPayloadFails verifies that a station input
// declared `source: upstream` whose task carries no task-out inputs payload
// fails run preparation with a clear failure_reason.
func TestRuns_UpstreamInputWithoutPayloadFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(ctx, st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	scriptA := writeExecutable(t, dir, "station-a.sh", `#!/bin/sh
set -eu
mkdir -p "$VERIPROC_RUN_DIR"
printf 'out\n' > "$VERIPROC_RUN_DIR/result-a.json"
cat > "$VERIPROC_WORKING_ROOT/task-out.yaml" <<'EOF'
schema_version: veriproc.task-out/v1
downstream:
  - key: k
    station_id: STATION-B
    window:
      start: "2025-07-03T00:00:00Z"
      end: "2025-07-03T23:59:59Z"
EOF
`)
	scriptB := writeExecutable(t, dir, "station-b.sh", `#!/bin/sh
printf 'b\n' > "$VERIPROC_RUN_DIR/result-b.json"
`)
	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st,
		stations.Spec{StationID: "STATION-A", StationName: "A_PROC", ContentHash: "sha256:station-a", SchemaVersion: "veriproc.station/v1", Outputs: []stations.OutputDefinition{{Name: "result-a.json", FileType: "A_RESULT", Required: true}}, Downstream: []stations.DownstreamTarget{{StationID: "STATION-B", Mode: "task_out"}}, Execution: stations.Execution{Executable: scriptA}},
		stations.Spec{StationID: "STATION-B", StationName: "B_PROC", ContentHash: "sha256:station-b", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "GRANULE_H5", Category: "product", Source: "upstream"}}, Execution: stations.Execution{Executable: scriptB}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "0000ad" })
	runN := 0
	rsvc := runs.NewService(runs.Config{Store: st, Executors: executor.NewSingleExecutorRegistry(executor.NewLocalExecutor(nil)), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), ProductCategories: map[string][]string{}, IDFactory: func() string { runN++; return "run-nop-" + fmt.Sprint(runN) }})
	disp := runs.NewDispatcher(rsvc, time.Millisecond, testLogger())
	res, err := tsvc.Submit(ctx, tasks.SubmitInput{Destination: tasks.Destination{StationID: "STATION-A"}, Window: tasks.Window{Start: time.Date(2025, 7, 3, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 7, 3, 23, 59, 59, 0, time.UTC)}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForTaskState(t, ctx, st, disp, res.Task.TaskID, "completed")
	waitForTaskCount(t, ctx, st, disp, 2)
	bTaskID := findTaskByStation(t, ctx, st, "STATION-B")
	waitForTaskState(t, ctx, st, disp, bTaskID, "failed")
	bTaskAfter, err := st.Tasks().Get(ctx, bTaskID)
	if err != nil {
		t.Fatalf("get b task: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !bTaskAfter.LatestRetryIndex.Valid {
		disp.Tick(ctx)
		time.Sleep(10 * time.Millisecond)
		bTaskAfter, _ = st.Tasks().Get(ctx, bTaskID)
	}
	var run *store.RunRecord
	if bTaskAfter.LatestRetryIndex.Valid {
		run, err = st.Runs().GetByTaskRetry(ctx, bTaskID, int(bTaskAfter.LatestRetryIndex.Int64))
	} else if !bTaskAfter.LatestRetryIndex.Valid {
		// Run preparation failures can abort before the run is persisted;
		// the task failure summary carries the reason in that case.
		run = nil
	}
	var reason string
	if run != nil {
		reason = run.FailureReason
	} else {
		reason = bTaskAfter.FailureSummary
	}
	if !strings.Contains(reason, "upstream") && !strings.Contains(reason, "GRANULE_H5") {
		t.Fatalf("failure reason = %q, want clear upstream-input failure", reason)
	}
}
