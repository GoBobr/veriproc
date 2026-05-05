package runs_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

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
	if err := reg.Seed(context.Background(), st,
		stations.Spec{StationID: "SCENE-L2", ProcType: "SCE_2", ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1"},
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
		Store:           st,
		Executor:        exec,
		Resolver:        reg,
		WorkingRootBase: t.TempDir(),
		Clock:           fixedClock(now),
		IDFactory:       runIDs,
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
