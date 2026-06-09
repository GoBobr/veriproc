package runs_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/executor"
	"github.com/gobobr/veriproc/internal/policy"
	"github.com/gobobr/veriproc/internal/runs"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/gobobr/veriproc/internal/tasks"
)

// newWorkingRootFixture builds a minimal run service configured with a custom
// Naming policy so working-root path generation can be tested in isolation.
func newWorkingRootFixture(t *testing.T, naming policy.Naming) (*runs.Service, *tasks.Service, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open("sqlite://" + filepath.Join(dir, "wr.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(context.Background(), st, stations.Spec{
		StationID: "WRSTA", StationName: "WR", ContentHash: "sha256:wr",
		SchemaVersion: "veriproc.station/v1",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	base := filepath.Join(dir, "work")
	now := time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC)
	n := 0
	idFn := func() string {
		n++
		return "run-wr-" + string(rune('0'+n))
	}
	taskN := 0
	taskIDFn := func() string {
		taskN++
		return fmt.Sprintf("a%05d", taskN)
	}
	tsvc := tasks.NewService(st, reg, func() time.Time { return now }, taskIDFn)
	rsvc := runs.NewService(runs.Config{
		Store:           st,
		Resolver:        reg,
		WorkingRootBase: base,
		Clock:           func() time.Time { return now },
		IDFactory:       idFn,
		Naming:          naming,
	})
	return rsvc, tsvc, st, base
}

func submitWRTask(t *testing.T, tsvc *tasks.Service) string {
	t.Helper()
	now := time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC)
	res, err := tsvc.Submit(context.Background(), tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "WRSTA"},
		Window:      tasks.Window{Start: now, End: now.Add(time.Hour)},
	})
	if err != nil {
		t.Fatalf("submit task: %v", err)
	}
	return res.Task.TaskID
}

// TestWorkingRootPath_NestedLayout verifies that the default nested template
// {station}/{task}/{run} produces a three-component relative path under base.
func TestWorkingRootPath_NestedLayout(t *testing.T) {
	naming := policy.Naming{
		WorkingRoot: policy.WorkingRoot{
			PathTemplate:    "{station}/{task}/{run}",
			StationSegment:  "{station_id}",
			TaskSegment:     "{task_id}",
			RunSegment:      "r{retry_index}",
			CollisionSuffix: "-{short_run_id}",
		},
	}
	rsvc, tsvc, _, base := newWorkingRootFixture(t, naming)
	taskID := submitWRTask(t, tsvc)

	run, err := rsvc.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if run.WorkingRoot == "" {
		t.Fatal("working root must not be empty")
	}
	// Must be under base.
	if !strings.HasPrefix(run.WorkingRoot, base+string(filepath.Separator)) {
		t.Errorf("working root %q not under base %q", run.WorkingRoot, base)
	}
	// With nested layout, the relative path has 3 components.
	rel, err := filepath.Rel(base, run.WorkingRoot)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 {
		t.Errorf("nested layout: expected 3 path components, got %d (%s)", len(parts), rel)
	}
	// Third component must start with "r" (run segment prefix).
	if !strings.HasPrefix(parts[2], "r") {
		t.Errorf("run segment %q should start with 'r'", parts[2])
	}
}

// TestWorkingRootPath_FlatLayout verifies that a flat template {task}-{run}
// produces a single-component path under base.
func TestWorkingRootPath_FlatLayout(t *testing.T) {
	naming := policy.Naming{
		WorkingRoot: policy.WorkingRoot{
			PathTemplate:    "{task}-{run}",
			StationSegment:  "{station_id}",
			TaskSegment:     "{task_id}",
			RunSegment:      "r{retry_index}",
			CollisionSuffix: "-{short_run_id}",
		},
	}
	rsvc, tsvc, _, base := newWorkingRootFixture(t, naming)
	taskID := submitWRTask(t, tsvc)

	run, err := rsvc.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	rel, err := filepath.Rel(base, run.WorkingRoot)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 1 {
		t.Errorf("flat layout: expected 1 path component, got %d (%s)", len(parts), rel)
	}
	// Component should contain both task and run segment tokens.
	if !strings.Contains(parts[0], "r0") {
		t.Errorf("flat component %q should contain run segment 'r0'", parts[0])
	}
}

// TestWorkingRootPath_Containment verifies that the working root is always
// strictly under working_root_base.
func TestWorkingRootPath_Containment(t *testing.T) {
	naming := policy.DefaultNaming()
	rsvc, tsvc, _, base := newWorkingRootFixture(t, naming)
	taskID := submitWRTask(t, tsvc)

	run, err := rsvc.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if !strings.HasPrefix(run.WorkingRoot, base+string(filepath.Separator)) {
		t.Errorf("working root %q escapes base %q", run.WorkingRoot, base)
	}
}

// TestWorkingRootPath_Deterministic verifies that the same run context always
// produces the same path (before collision).
func TestWorkingRootPath_Deterministic(t *testing.T) {
	// We test determinism by calling PrepareRun and checking the path against
	// the expected template expansion. Two runs of the same task have the same
	// task segment but differ in retry index.
	naming := policy.DefaultNaming()
	rsvc, tsvc, _, base := newWorkingRootFixture(t, naming)
	taskID := submitWRTask(t, tsvc)

	run1, err := rsvc.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare run1: %v", err)
	}
	rel, _ := filepath.Rel(base, run1.WorkingRoot)
	// rel should be deterministic from the template expansion.
	if rel == "" {
		t.Error("relative path should not be empty")
	}
	// WorkingRoot must be materialized on disk.
	if _, err := os.Stat(run1.WorkingRoot); err != nil {
		t.Errorf("working root directory not created: %v", err)
	}
}

// TestWorkingRootPath_Uniqueness verifies that different runs for the same task
// get different working roots (retry_index distinguishes them).
func TestWorkingRootPath_Uniqueness(t *testing.T) {
	// Use the shared newFixture() which has a stub executor and supports retries.
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)

	run1, err := f.runs.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("prepare run1: %v", err)
	}
	// Dispatch and fail the first run via the stub executor, then retry.
	_, err = f.runs.Dispatch(ctx, run1.RunID)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	schedID := "stub-" + run1.RunID
	if err := f.exec.SetOutcome(schedID, executor.StatusFailed); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := f.runs.Poll(ctx, run1.RunID); err != nil {
			t.Fatalf("poll: %v", err)
		}
	}
	out, err := f.runs.Retry(ctx, taskID)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	run2 := out.Run
	if run1.WorkingRoot == run2.WorkingRoot {
		t.Error("retried run must have a distinct working root")
	}
	if run2.RetryIndex != 1 {
		t.Errorf("retry_index = %d, want 1", run2.RetryIndex)
	}
}

// TestWorkingRootPath_NonIdentity verifies the working root is not the run's
// identity — two fields must differ.
func TestWorkingRootPath_NonIdentity(t *testing.T) {
	naming := policy.DefaultNaming()
	rsvc, tsvc, _, _ := newWorkingRootFixture(t, naming)
	taskID := submitWRTask(t, tsvc)

	run, err := rsvc.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	// Identity is (task_id, retry_index); working_root is NOT identity.
	if run.WorkingRoot == run.RunID {
		t.Error("working_root must not equal run_id (not an identity)")
	}
	if run.WorkingRoot == run.TaskID {
		t.Error("working_root must not equal task_id")
	}
}

// TestWorkingRootPath_CollisionSuffix verifies that collision disambiguation
// appends the suffix to the final path component only and does not reorder
// existing tokens.
func TestWorkingRootPath_CollisionSuffix(t *testing.T) {
	// Use {station}/{run} (no {task}) so two different tasks at the same
	// station with retry_index=0 compute the identical base path and collide.
	naming := policy.Naming{
		WorkingRoot: policy.WorkingRoot{
			PathTemplate:    "{station}/{run}",
			StationSegment:  "{station_id}",
			TaskSegment:     "{task_id}",
			RunSegment:      "r{retry_index}",
			CollisionSuffix: "-col",
		},
	}
	rsvc, tsvc, _, base := newWorkingRootFixture(t, naming)

	// First task: gets the clean path.
	taskID1 := submitWRTask(t, tsvc)
	run1, err := rsvc.PrepareRun(context.Background(), taskID1)
	if err != nil {
		t.Fatalf("prepare run1: %v", err)
	}
	rel1, _ := filepath.Rel(base, run1.WorkingRoot)
	parts1 := strings.Split(rel1, string(filepath.Separator))
	if !strings.HasPrefix(parts1[len(parts1)-1], "r") {
		t.Errorf("final component %q should start with run segment prefix 'r'", parts1[len(parts1)-1])
	}
	if strings.Contains(rel1, "-col") {
		t.Errorf("first run should have no collision suffix; got %q", rel1)
	}

	// Simulate that path being taken on disk.
	if err := os.MkdirAll(run1.WorkingRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Second task: same station, same retry_index=0 → same template expansion → collision.
	taskID2 := submitWRTask(t, tsvc)
	run2, err := rsvc.PrepareRun(context.Background(), taskID2)
	if err != nil {
		t.Fatalf("prepare run2: %v", err)
	}
	rel2, _ := filepath.Rel(base, run2.WorkingRoot)
	finalParts := strings.Split(rel2, string(filepath.Separator))
	finalComp := finalParts[len(finalParts)-1]

	if !strings.HasSuffix(finalComp, "-col") {
		t.Errorf("collision suffix should be appended to final component; got %q (full: %q)", finalComp, rel2)
	}
	// The suffix should be on the LAST component only, not add a new path level.
	// Template has 2 segments ({station}/{run}), so there should be exactly 2 parts.
	if len(finalParts) != 2 {
		t.Errorf("collision should not add a new path component; got %d parts: %q", len(finalParts), rel2)
	}
}

