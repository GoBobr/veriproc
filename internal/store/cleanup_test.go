package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// helper: insert a run for a task with a working root.
func mkRun(t *testing.T, s *Store, revID, runID, taskID string, retryIdx int, workingRoot string) {
	t.Helper()
	run := &RunRecord{
		RunID:             runID,
		TaskID:            taskID,
		StationRevisionID: revID,
		RetryIndex:        retryIdx,
		WorkingRoot:       workingRoot,
		State:             "succeeded",
		Canonicality:      "canonical",
	}
	if err := s.Runs().Insert(context.Background(), run); err != nil {
		t.Fatalf("run insert %s: %v", runID, err)
	}
}

// helper: insert a job for a run.
func mkJob(t *testing.T, s *Store, jobID, runID string) {
	t.Helper()
	if err := s.Jobs().Insert(context.Background(), &JobRecord{
		JobID:          jobID,
		RunID:          runID,
		Executor:       "local",
		SchedulerState: "succeeded",
	}); err != nil {
		t.Fatalf("job insert %s: %v", jobID, err)
	}
}

// helper: insert an artifact for a run.
func mkArtifact(t *testing.T, s *Store, artifactID, runID string) {
	t.Helper()
	if err := s.Artifacts().Insert(context.Background(), &ArtifactRecord{
		ArtifactID:     artifactID,
		ProducingRunID: runID,
		LogicalType:    "output",
		Path:           "/wr/" + runID + "/" + artifactID,
		Availability:   "available",
	}); err != nil {
		t.Fatalf("artifact insert %s: %v", artifactID, err)
	}
}

// TestPurgeRun_CascadesDependents verifies a single run plus its jobs and
// artifacts are removed while the owning task and sibling runs survive.
func TestPurgeRun_CascadesDependents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")
	if err := s.Tasks().Insert(ctx, mkTask("t-purge-run")); err != nil {
		t.Fatalf("task: %v", err)
	}
	mkRun(t, s, revID, "run-keep", "t-purge-run", 0, "/wr/run-keep")
	mkRun(t, s, revID, "run-del", "t-purge-run", 1, "/wr/run-del")
	mkJob(t, s, "job-del", "run-del")
	mkArtifact(t, s, "art-del", "run-del")

	res, err := s.PurgeRun(ctx, "run-del")
	if err != nil {
		t.Fatalf("PurgeRun: %v", err)
	}
	if res.Counts.Runs != 1 || res.Counts.Jobs != 1 || res.Counts.Artifacts != 1 {
		t.Errorf("counts = %+v, want runs=1 jobs=1 artifacts=1", res.Counts)
	}
	if len(res.WorkingRoots) != 1 || res.WorkingRoots[0] != "/wr/run-del" {
		t.Errorf("working roots = %v, want [/wr/run-del]", res.WorkingRoots)
	}
	// Deleted run and dependents gone.
	if _, err := s.Runs().Get(ctx, "run-del"); !errors.Is(err, ErrNotFound) {
		t.Errorf("run-del still present: %v", err)
	}
	if _, err := s.Jobs().Get(ctx, "job-del"); !errors.Is(err, ErrNotFound) {
		t.Errorf("job-del still present: %v", err)
	}
	if _, err := s.Artifacts().Get(ctx, "art-del"); !errors.Is(err, ErrNotFound) {
		t.Errorf("art-del still present: %v", err)
	}
	// Sibling run and owning task survive.
	if _, err := s.Runs().Get(ctx, "run-keep"); err != nil {
		t.Errorf("run-keep should survive: %v", err)
	}
	if _, err := s.Tasks().Get(ctx, "t-purge-run"); err != nil {
		t.Errorf("task should survive: %v", err)
	}
}

// TestPurgeRun_NotFound verifies a missing run yields ErrNotFound.
func TestPurgeRun_NotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.PurgeRun(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestPurgeTasks_ClosureAndCascade verifies a parent task purge also removes
// descendant tasks and all of their runs.
func TestPurgeTasks_ClosureAndCascade(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")

	parent := mkTask("t-parent")
	if err := s.Tasks().Insert(ctx, parent); err != nil {
		t.Fatalf("parent task: %v", err)
	}
	child := mkTask("t-child")
	child.ParentTaskID = "t-parent"
	if err := s.Tasks().Insert(ctx, child); err != nil {
		t.Fatalf("child task: %v", err)
	}
	mkRun(t, s, revID, "run-parent", "t-parent", 0, "/wr/run-parent")
	mkRun(t, s, revID, "run-child", "t-child", 0, "/wr/run-child")
	mkArtifact(t, s, "art-parent", "run-parent")

	res, err := s.PurgeTasks(ctx, []string{"t-parent"})
	if err != nil {
		t.Fatalf("PurgeTasks: %v", err)
	}
	if res.Counts.Tasks != 2 {
		t.Errorf("tasks deleted = %d, want 2", res.Counts.Tasks)
	}
	if res.Counts.Runs != 2 {
		t.Errorf("runs deleted = %d, want 2", res.Counts.Runs)
	}
	if len(res.WorkingRoots) != 2 {
		t.Errorf("working roots = %v, want 2 entries", res.WorkingRoots)
	}
	for _, id := range []string{"t-parent", "t-child"} {
		if _, err := s.Tasks().Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("task %s still present: %v", id, err)
		}
	}
	for _, id := range []string{"run-parent", "run-child"} {
		if _, err := s.Runs().Get(ctx, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("run %s still present: %v", id, err)
		}
	}
}

// TestPurgeTasks_UnknownIgnored verifies unknown task IDs are silently dropped.
func TestPurgeTasks_UnknownIgnored(t *testing.T) {
	s := newTestStore(t)
	res, err := s.PurgeTasks(context.Background(), []string{"ghost"})
	if err != nil {
		t.Fatalf("PurgeTasks: %v", err)
	}
	if res.Counts.Tasks != 0 || len(res.TaskIDs) != 0 {
		t.Errorf("expected no deletions, got %+v", res)
	}
}

// TestCollectTaskClosure verifies the BFS closure over parent_task_id.
func TestCollectTaskClosure(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Tasks().Insert(ctx, mkTask("a")); err != nil {
		t.Fatalf("a: %v", err)
	}
	b := mkTask("b")
	b.ParentTaskID = "a"
	if err := s.Tasks().Insert(ctx, b); err != nil {
		t.Fatalf("b: %v", err)
	}
	c := mkTask("c")
	c.ParentTaskID = "b"
	if err := s.Tasks().Insert(ctx, c); err != nil {
		t.Fatalf("c: %v", err)
	}
	got, err := s.CollectTaskClosure(ctx, []string{"a", "ghost"})
	if err != nil {
		t.Fatalf("CollectTaskClosure: %v", err)
	}
	want := map[string]bool{"a": true, "b": true, "c": true}
	if len(got) != 3 {
		t.Fatalf("closure = %v, want a,b,c", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected id %q in closure %v", id, got)
		}
	}
}

// TestIDsForCleanup_WindowSelection verifies before/after window filtering.
func TestIDsForCleanup_WindowSelection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mk := func(id string, start, end time.Time) {
		tk := mkTask(id)
		tk.WindowStart = start
		tk.WindowEnd = end
		if err := s.Tasks().Insert(ctx, tk); err != nil {
			t.Fatalf("task %s: %v", id, err)
		}
	}
	// early: window fully before 2025-06-01
	mk("early", time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC))
	// late: window fully after 2025-07-01
	mk("late", time.Date(2025, 7, 2, 0, 0, 0, 0, time.UTC), time.Date(2025, 7, 2, 1, 0, 0, 0, time.UTC))
	// mid: between
	mk("mid", time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC), time.Date(2025, 6, 15, 1, 0, 0, 0, time.UTC))

	before := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	after := time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC)

	beforeIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingWindow)
	if err != nil {
		t.Fatalf("IDsForCleanup before: %v", err)
	}
	if len(beforeIDs) != 1 || beforeIDs[0] != "early" {
		t.Errorf("before selection = %v, want [early]", beforeIDs)
	}

	afterIDs, err := s.Tasks().IDsForCleanup(ctx, time.Time{}, after, BasisProcessingWindow)
	if err != nil {
		t.Fatalf("IDsForCleanup after: %v", err)
	}
	if len(afterIDs) != 1 || afterIDs[0] != "late" {
		t.Errorf("after selection = %v, want [late]", afterIDs)
	}

	// Combined window [after-bound, before-bound] selecting mid.
	rangeIDs, err := s.Tasks().IDsForCleanup(ctx,
		time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), BasisProcessingWindow)
	if err != nil {
		t.Fatalf("IDsForCleanup range: %v", err)
	}
	if len(rangeIDs) != 1 || rangeIDs[0] != "mid" {
		t.Errorf("range selection = %v, want [mid]", rangeIDs)
	}

	// No cutoff is an error.
	if _, err := s.Tasks().IDsForCleanup(ctx, time.Time{}, time.Time{}, BasisProcessingWindow); err == nil {
		t.Error("expected error when no cutoff supplied")
	}
}

// TestIDsForCleanup_ProcessingTimeSelection verifies created_at-based filtering.
func TestIDsForCleanup_ProcessingTimeSelection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mk := func(id string, created time.Time) {
		tk := mkTask(id)
		tk.CreatedAt = created
		// Sensing windows are all in the same range to prove the basis
		// selects on created_at, not window_start/window_end.
		tk.WindowStart = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		tk.WindowEnd = time.Date(2025, 1, 1, 1, 0, 0, 0, time.UTC)
		if err := s.Tasks().Insert(ctx, tk); err != nil {
			t.Fatalf("task %s: %v", id, err)
		}
	}
	mk("early", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	mk("mid", time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC))
	mk("late", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	before := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	after := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)

	beforeIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingTime)
	if err != nil {
		t.Fatalf("IDsForCleanup before: %v", err)
	}
	if len(beforeIDs) != 1 || beforeIDs[0] != "early" {
		t.Errorf("before selection = %v, want [early]", beforeIDs)
	}

	afterIDs, err := s.Tasks().IDsForCleanup(ctx, time.Time{}, after, BasisProcessingTime)
	if err != nil {
		t.Fatalf("IDsForCleanup after: %v", err)
	}
	if len(afterIDs) != 1 || afterIDs[0] != "late" {
		t.Errorf("after selection = %v, want [late]", afterIDs)
	}

	// Range [after, before] selecting mid.
	rangeIDs, err := s.Tasks().IDsForCleanup(ctx,
		time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC), BasisProcessingTime)
	if err != nil {
		t.Fatalf("IDsForCleanup range: %v", err)
	}
	if len(rangeIDs) != 1 || rangeIDs[0] != "mid" {
		t.Errorf("range selection = %v, want [mid]", rangeIDs)
	}
}
