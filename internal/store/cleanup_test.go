package store

import (
	"context"
	"database/sql"
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

	res, err := s.PurgeRun(ctx, "run-del", true)
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
	if _, err := s.PurgeRun(context.Background(), "nope", true); !errors.Is(err, ErrNotFound) {
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

	res, err := s.PurgeTasks(ctx, []string{"t-parent"}, true)
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
	res, err := s.PurgeTasks(context.Background(), []string{"ghost"}, true)
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

	beforeIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingWindow, "")
	if err != nil {
		t.Fatalf("IDsForCleanup before: %v", err)
	}
	if len(beforeIDs) != 1 || beforeIDs[0] != "early" {
		t.Errorf("before selection = %v, want [early]", beforeIDs)
	}

	afterIDs, err := s.Tasks().IDsForCleanup(ctx, time.Time{}, after, BasisProcessingWindow, "")
	if err != nil {
		t.Fatalf("IDsForCleanup after: %v", err)
	}
	if len(afterIDs) != 1 || afterIDs[0] != "late" {
		t.Errorf("after selection = %v, want [late]", afterIDs)
	}

	// Combined window [after-bound, before-bound] selecting mid.
	rangeIDs, err := s.Tasks().IDsForCleanup(ctx,
		time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC), BasisProcessingWindow, "")
	if err != nil {
		t.Fatalf("IDsForCleanup range: %v", err)
	}
	if len(rangeIDs) != 1 || rangeIDs[0] != "mid" {
		t.Errorf("range selection = %v, want [mid]", rangeIDs)
	}

	// No cutoff is an error.
	if _, err := s.Tasks().IDsForCleanup(ctx, time.Time{}, time.Time{}, BasisProcessingWindow, ""); err == nil {
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

	beforeIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingTime, "")
	if err != nil {
		t.Fatalf("IDsForCleanup before: %v", err)
	}
	if len(beforeIDs) != 1 || beforeIDs[0] != "early" {
		t.Errorf("before selection = %v, want [early]", beforeIDs)
	}

	afterIDs, err := s.Tasks().IDsForCleanup(ctx, time.Time{}, after, BasisProcessingTime, "")
	if err != nil {
		t.Fatalf("IDsForCleanup after: %v", err)
	}
	if len(afterIDs) != 1 || afterIDs[0] != "late" {
		t.Errorf("after selection = %v, want [late]", afterIDs)
	}

	// Range [after, before] selecting mid.
	rangeIDs, err := s.Tasks().IDsForCleanup(ctx,
		time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 14, 0, 0, 0, 0, time.UTC), BasisProcessingTime, "")
	if err != nil {
		t.Fatalf("IDsForCleanup range: %v", err)
	}
	if len(rangeIDs) != 1 || rangeIDs[0] != "mid" {
		t.Errorf("range selection = %v, want [mid]", rangeIDs)
	}
}

// TestIDsForCleanup_StationFilter verifies the station_id filter restricts
// results to tasks whose destination_station_id matches.
func TestIDsForCleanup_StationFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	mk := func(id, stationID string, start, end time.Time) {
		tk := mkTask(id)
		tk.DestinationStationID = stationID
		tk.WindowStart = start
		tk.WindowEnd = end
		if err := s.Tasks().Insert(ctx, tk); err != nil {
			t.Fatalf("task %s: %v", id, err)
		}
	}
	// Two tasks in the same time window but different stations.
	mk("scene-early", "SCENE-L2", time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC))
	mk("map-early", "MAP-L1C", time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC), time.Date(2025, 5, 1, 1, 0, 0, 0, time.UTC))

	before := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)

	// No station filter → both tasks returned.
	allIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingWindow, "")
	if err != nil {
		t.Fatalf("IDsForCleanup all: %v", err)
	}
	if len(allIDs) != 2 {
		t.Errorf("no-station selection = %v, want 2 ids", allIDs)
	}

	// Station filter SCENE-L2 → only scene-early.
	sceneIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingWindow, "SCENE-L2")
	if err != nil {
		t.Fatalf("IDsForCleanup scene: %v", err)
	}
	if len(sceneIDs) != 1 || sceneIDs[0] != "scene-early" {
		t.Errorf("scene selection = %v, want [scene-early]", sceneIDs)
	}

	// Station filter MAP-L1C → only map-early.
	mapIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingWindow, "MAP-L1C")
	if err != nil {
		t.Fatalf("IDsForCleanup map: %v", err)
	}
	if len(mapIDs) != 1 || mapIDs[0] != "map-early" {
		t.Errorf("map selection = %v, want [map-early]", mapIDs)
	}

	// Station filter with no matching tasks → empty.
	noneIDs, err := s.Tasks().IDsForCleanup(ctx, before, time.Time{}, BasisProcessingWindow, "NO2-L2")
	if err != nil {
		t.Fatalf("IDsForCleanup none: %v", err)
	}
	if len(noneIDs) != 0 {
		t.Errorf("none selection = %v, want []", noneIDs)
	}
}

// TestPurgeRun_NoCascade_OrphanArtifacts verifies non-cascade run deletion
// nullifies nullable FK references instead of deleting them.
func TestPurgeRun_NoCascade_OrphanArtifacts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")
	if err := s.Tasks().Insert(ctx, mkTask("t-orphan")); err != nil {
		t.Fatalf("task: %v", err)
	}
	mkRun(t, s, revID, "run-orphan", "t-orphan", 0, "/wr/run-orphan")
	mkArtifact(t, s, "art-orphan", "run-orphan")
	mkJob(t, s, "job-orphan", "run-orphan")

	res, err := s.PurgeRun(ctx, "run-orphan", false)
	if err != nil {
		t.Fatalf("PurgeRun no-cascade: %v", err)
	}
	if res.Counts.Runs != 1 {
		t.Errorf("runs deleted = %d, want 1", res.Counts.Runs)
	}
	if res.Counts.Jobs != 1 {
		t.Errorf("jobs deleted = %d, want 1 (NOT NULL constraint)", res.Counts.Jobs)
	}
	if len(res.WorkingRoots) != 1 || res.WorkingRoots[0] != "/wr/run-orphan" {
		t.Errorf("working roots should report orphan: %v", res.WorkingRoots)
	}
	// Run is gone.
	if _, err := s.Runs().Get(ctx, "run-orphan"); !errors.Is(err, ErrNotFound) {
		t.Errorf("run should be deleted: %v", err)
	}
	// Job is gone (NOT NULL FK).
	if _, err := s.Jobs().Get(ctx, "job-orphan"); !errors.Is(err, ErrNotFound) {
		t.Errorf("job should be deleted (NOT NULL constraint): %v", err)
	}
	// Artifact survives but producing_run_id is NULL.
	art, err := s.Artifacts().Get(ctx, "art-orphan")
	if err != nil {
		t.Fatalf("artifact should survive: %v", err)
	}
	if art.ProducingRunID != "" {
		t.Errorf("artifact producing_run_id should be NULL, got %q", art.ProducingRunID)
	}
}

// TestPurgeTasks_NoCascade_PreservesDescendants verifies non-cascade task
// deletion does not follow descendants and orphans them.
func TestPurgeTasks_NoCascade_PreservesDescendants(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")

	parent := mkTask("t-parent-nc")
	if err := s.Tasks().Insert(ctx, parent); err != nil {
		t.Fatalf("parent task: %v", err)
	}
	child := mkTask("t-child-nc")
	child.ParentTaskID = "t-parent-nc"
	if err := s.Tasks().Insert(ctx, child); err != nil {
		t.Fatalf("child task: %v", err)
	}
	mkRun(t, s, revID, "run-parent-nc", "t-parent-nc", 0, "/wr/run-parent-nc")
	mkRun(t, s, revID, "run-child-nc", "t-child-nc", 0, "/wr/run-child-nc")

	res, err := s.PurgeTasks(ctx, []string{"t-parent-nc"}, false)
	if err != nil {
		t.Fatalf("PurgeTasks no-cascade: %v", err)
	}
	// Only the parent task is in the deletion set.
	if len(res.TaskIDs) != 1 || res.TaskIDs[0] != "t-parent-nc" {
		t.Errorf("task IDs = %v, want [t-parent-nc]", res.TaskIDs)
	}
	if res.Counts.Tasks != 1 {
		t.Errorf("tasks deleted = %d, want 1", res.Counts.Tasks)
	}
	if res.Counts.Runs != 1 {
		t.Errorf("runs deleted = %d, want 1 (only parent's run)", res.Counts.Runs)
	}
	if len(res.WorkingRoots) != 1 {
		t.Errorf("working roots should report 1: %v", res.WorkingRoots)
	}
	// Parent task is gone.
	if _, err := s.Tasks().Get(ctx, "t-parent-nc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("parent task should be deleted: %v", err)
	}
	// Child task survives with parent_task_id = NULL.
	childRec, err := s.Tasks().Get(ctx, "t-child-nc")
	if err != nil {
		t.Fatalf("child should survive: %v", err)
	}
	if childRec.ParentTaskID != "" {
		t.Errorf("child parent_task_id should be NULL, got %q", childRec.ParentTaskID)
	}
	// Child run survives.
	if _, err := s.Runs().Get(ctx, "run-child-nc"); err != nil {
		t.Errorf("child run should survive: %v", err)
	}
}

// TestPurgeTasks_NoCascade_KeepsProvenanceLinks verifies provenance links
// are not deleted in non-cascade mode.
func TestPurgeTasks_NoCascade_KeepsProvenanceLinks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tk := mkTask("t-prov-nc")
	if err := s.Tasks().Insert(ctx, tk); err != nil {
		t.Fatalf("task: %v", err)
	}

	// Insert a provenance link manually (no helper for this).
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO provenance_links (link_id, source_type, source_id, target_type, target_id, relationship_type, created_at)
		 VALUES (?, 'task', ?, 'task', ?, 'contributes_to', ?)`,
		"prov-nc", "t-prov-nc", "t-other-ghost", time.Now().UTC()); err != nil {
		t.Fatalf("provenance insert: %v", err)
	}

	res, err := s.PurgeTasks(ctx, []string{"t-prov-nc"}, false)
	if err != nil {
		t.Fatalf("PurgeTasks no-cascade: %v", err)
	}
	if res.Counts.ProvenanceLinks != 0 {
		t.Errorf("provenance links deleted = %d, want 0", res.Counts.ProvenanceLinks)
	}

	// Provenance link survives (now referencing a ghost).
	var linkID string
	err = s.db.QueryRowContext(ctx,
		`SELECT link_id FROM provenance_links WHERE source_type = 'task' AND source_id = ?`,
		"t-prov-nc").Scan(&linkID)
	if err != nil {
		t.Errorf("provenance link should survive as ghost ref: %v", err)
	}
}

// TestPurgeRun_NoCascade_NullifiesPreviousRunID verifies that a canonicality_audit
// row referencing the deleted run as previous_run_id has its column set to NULL
// (not deleted), because previous_run_id is nullable in the non-cascade path.
func TestPurgeRun_NoCascade_NullifiesPreviousRunID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")
	if err := s.Tasks().Insert(ctx, mkTask("t-can")); err != nil {
		t.Fatalf("task: %v", err)
	}
	mkRun(t, s, revID, "run-prev", "t-can", 0, "/wr/run-prev")
	mkRun(t, s, revID, "run-new", "t-can", 1, "/wr/run-new")

	// Insert processing_fingerprint (required by canonicality_audit FK).
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO processing_fingerprints (fingerprint_id, value, created_at) VALUES (?, 'fp-val', ?)`,
		"fp-1", time.Now().UTC()); err != nil {
		t.Fatalf("fingerprint insert: %v", err)
	}

	// audit-prev: references run-prev as previous_run_id (survives, column NULLed).
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO canonicality_audit (audit_id, fingerprint_id, previous_run_id, new_run_id, action, occurred_at)
		 VALUES (?, ?, ?, ?, 'promoted', ?)`,
		"audit-prev", "fp-1", "run-prev", "run-new", time.Now().UTC()); err != nil {
		t.Fatalf("audit-prev insert: %v", err)
	}
	// audit-new: references run-prev as new_run_id (deleted, NOT NULL column).
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO canonicality_audit (audit_id, fingerprint_id, previous_run_id, new_run_id, action, occurred_at)
		 VALUES (?, ?, NULL, ?, 'demoted', ?)`,
		"audit-new", "fp-1", "run-prev", time.Now().UTC()); err != nil {
		t.Fatalf("audit-new insert: %v", err)
	}

	res, err := s.PurgeRun(ctx, "run-prev", false)
	if err != nil {
		t.Fatalf("PurgeRun no-cascade: %v", err)
	}
	if res.Counts.Runs != 1 {
		t.Errorf("runs deleted = %d, want 1", res.Counts.Runs)
	}
	if res.Counts.CanonicalityAudits != 1 {
		t.Errorf("canonicality_audits deleted = %d, want 1 (only audit-new)", res.Counts.CanonicalityAudits)
	}

	// audit-prev survives with previous_run_id = NULL.
	var prevID sql.NullString
	err = s.db.QueryRowContext(ctx,
		`SELECT previous_run_id FROM canonicality_audit WHERE audit_id = 'audit-prev'`).Scan(&prevID)
	if err != nil {
		t.Fatalf("audit-prev should survive: %v", err)
	}
	if prevID.Valid {
		t.Errorf("previous_run_id should be NULL, got %q", prevID.String)
	}

	// audit-new is gone (referenced run-prev as new_run_id).
	var count int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM canonicality_audit WHERE audit_id = 'audit-new'`).Scan(&count)
	if err != nil {
		t.Fatalf("query audit-new: %v", err)
	}
	if count != 0 {
		t.Errorf("audit-new should have been deleted, count = %d", count)
	}
}
