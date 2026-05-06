package runs_test

import (
	"context"
	"errors"
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
test -n "$VERIPROC_RUN_ID"
test -n "$VERIPROC_TASK_ID"
test -f "$VERIPROC_JOBORDER_PATH"
printf '{"station":"%s","run":"%s"}\n' "$VERIPROC_STATION_ID" "$VERIPROC_RUN_ID" > "$VERIPROC_RUN_DIR/result-a.json"
`)
	scriptB := writeExecutable(t, dir, "station-b.sh", `#!/bin/sh
set -eu
test -f input/result-a.json
printf '{"station":"%s","parent_input":"ok"}\n' "$VERIPROC_STATION_ID" > "$VERIPROC_RUN_DIR/result-b.json"
`)

	reg := stations.NewRegistry()
	if err := reg.Seed(ctx, st,
		stations.Spec{StationID: "STATION-A", ProcType: "A_PROC", ContentHash: "sha256:station-a", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "PRIMARY_A", Category: "product"}, {FileType: "AUX_A", Category: "product"}}, Outputs: []stations.OutputDefinition{{Name: "result-a.json", FileType: "A_RESULT", Required: true}}, Downstream: []stations.DownstreamTarget{{StationID: "STATION-B"}}, Publication: stations.PublicationPolicy{Enabled: true, ArchiveID: "hot", Mode: "copy", Outputs: []string{"result-a.json"}}, Scripts: map[string]string{"run": scriptA}},
		stations.Spec{StationID: "STATION-B", ProcType: "B_PROC", ContentHash: "sha256:station-b", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "A_RESULT", Category: "product", Pattern: "result-a.json"}}, Outputs: []stations.OutputDefinition{{Name: "result-b.json", FileType: "B_RESULT", Required: true}}, Scripts: map[string]string{"run": scriptB}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "task-a" })
	runN := 0
	rsvc := runs.NewService(runs.Config{Store: st, Executor: executor.NewLocalExecutor(nil), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), InstanceID: "test-instance", Facility: map[string]string{"environment": "TEST"}, RollingArchives: map[string]string{"hot": archive}, ProductCategories: map[string][]string{"product": {"rolling:hot"}}, Generators: map[string]string{"job_order": "test-generator-v1"}, IDFactory: func() string { runN++; return "run-local-" + strconv.Itoa(runN) }})
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
	if jobOrder["schema_version"] != "veriproc.joborder/v1" || jobOrder["log_level"] == nil || jobOrder["dyn_params"] == nil {
		t.Fatalf("joborder missing required root fields: %#v", jobOrder)
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
	page, _ := st.Tasks().List(ctx, store.ListFilter{ParentRunID: runA.RunID, Limit: 10})
	if len(page.Items) != 1 || page.Items[0].DestinationStationID != "STATION-B" {
		t.Fatalf("downstream tasks = %#v", page.Items)
	}
	waitForTaskState(t, ctx, st, disp, page.Items[0].TaskID, "completed")
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
	if err := reg.Seed(ctx, st, stations.Spec{StationID: "BROKEN", ProcType: "BROKEN", ContentHash: "sha256:broken", SchemaVersion: "veriproc.station/v1", Outputs: []stations.OutputDefinition{{Name: "required.json", FileType: "REQUIRED", Required: true}}, Scripts: map[string]string{"run": script}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tsvc := tasks.NewService(st, reg, nil, func() string { return "task-broken" })
	rsvc := runs.NewService(runs.Config{Store: st, Executor: executor.NewLocalExecutor(nil), Resolver: reg, WorkingRootBase: filepath.Join(dir, "work"), IDFactory: func() string { return "run-broken" }})
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
		stations.Spec{StationID: "SCENE-L2", ProcType: "SCE_2", ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1", Inputs: []stations.InputDefinition{{FileType: "PRIMARY_INPUT", Category: "product"}}},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	now := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	taskN := 0
	taskIDs := func() string {
		taskN++
		return "task-" + strconv.Itoa(taskN)
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
		Executor:          exec,
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

// TestRuns_PrepareAndFreeze_3_8_3_10_M3 — PrepareRun creates a run, persists a
// frozen manifest, computes a fingerprint, and leaves the run in state=ready.
func TestRuns_PrepareAndFreeze_3_8_3_10_M3(t *testing.T) {
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

// TestRuns_RetryCreatesNewIdentity_3_7_M3 — preparing a second run for the
// same task yields a distinct run_id and incremented retry_index.
func TestRuns_RetryCreatesNewIdentity_3_7_M3(t *testing.T) {
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

// TestRuns_DispatchOnlyFromReady_5_6_M3 — Dispatch on a non-ready run returns
// ErrInvalidStateTransition; idempotent re-dispatch from dispatched is a no-op.
func TestRuns_DispatchOnlyFromReady_5_6_M3(t *testing.T) {
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

// TestRuns_FullLifecycle_5_6_7_5_M3 — end-to-end via dispatcher ticks: a
// submitted task progresses through prepare → dispatch → poll → finalize, and
// the run ends complete with canonicality=canonical and a log artifact.
func TestRuns_FullLifecycle_5_6_7_5_M3(t *testing.T) {
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

// TestRuns_FailedJob_3_9_M3 — when the executor returns FAILED, the run ends
// in state=failed with a recorded reason and the task moves to state=failed.
func TestRuns_FailedJob_3_9_M3(t *testing.T) {
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

// TestRuns_FinalizeRequiresFinalizingState_5_6_M3 — Finalize on a non-finalizing
// run is rejected as ErrInvalidStateTransition (Spec §5.6 completion gate).
func TestRuns_FinalizeRequiresFinalizingState_5_6_M3(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	ctx := context.Background()
	r, _ := f.runs.PrepareRun(ctx, taskID)
	if _, err := f.runs.Finalize(ctx, r.RunID); !errors.Is(err, runs.ErrInvalidStateTransition) {
		t.Errorf("Finalize on ready: want ErrInvalidStateTransition, got %v", err)
	}
}

// TestRuns_DuplicateFingerprintMarkedDuplicate_3_10_M3 — when a second run
// completes for the same fingerprint, it is marked canonicality=duplicate
// and the task's canonical_run_id stays pinned to the first.
func TestRuns_DuplicateFingerprintMarkedDuplicate_3_10_M3(t *testing.T) {
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
}
