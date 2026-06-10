package cleaner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "veriproc.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}

func newService(t *testing.T) *Service {
	return New(newStore(t), zerolog.Nop())
}

func mkStation(t *testing.T, st *store.Store, id string) string {
	t.Helper()
	rev := &store.StationRevisionRecord{
		RevisionID:    "rev-" + id,
		StationID:     id,
		StationName:   "Name " + id,
		ContentHash:   "sha256:test-" + id,
		SchemaVersion: "veriproc.station/v1",
	}
	if err := st.Stations().Insert(context.Background(), rev); err != nil {
		t.Fatalf("station insert: %v", err)
	}
	return rev.RevisionID
}

func mkTask(t *testing.T, st *store.Store, id string, start, end time.Time) {
	t.Helper()
	tk := &store.TaskRecord{
		TaskID:               id,
		SchemaVersion:        "veriproc.task-submission/v1",
		DestinationStationID: "SCENE-L2",
		WindowStart:          start,
		WindowEnd:            end,
		ClientMetadata:       json.RawMessage(`{"mission":"CO2M"}`),
		RoutingContent:       json.RawMessage(`{"destination":{"station_id":"SCENE-L2"}}`),
		RoutingContentHash:   "sha256:routing-" + id,
		SubmissionOrigin:     "client",
		State:                "accepted",
	}
	if err := st.Tasks().Insert(context.Background(), tk); err != nil {
		t.Fatalf("task insert %s: %v", id, err)
	}
}

// mkRunDir inserts a run whose working root is a real temp directory and
// returns the directory path.
func mkRunDir(t *testing.T, st *store.Store, revID, runID, taskID string, retryIdx int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "marker"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	run := &store.RunRecord{
		RunID:             runID,
		TaskID:            taskID,
		StationRevisionID: revID,
		RetryIndex:        retryIdx,
		WorkingRoot:       dir,
		State:             "succeeded",
		Canonicality:      "canonical",
	}
	if err := st.Runs().Insert(context.Background(), run); err != nil {
		t.Fatalf("run insert %s: %v", runID, err)
	}
	return dir
}

func ts(month, day int) time.Time {
	return time.Date(2025, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

// TestDeleteRun removes a run's DB rows and working root from disk.
func TestDeleteRun(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "t1", ts(7, 3), ts(7, 4))
	dir := mkRunDir(t, st, revID, "run-1", "t1", 0)

	rep, err := svc.DeleteRun(ctx, "run-1", false, true)
	if err != nil {
		t.Fatalf("DeleteRun: %v", err)
	}
	if rep.Counts.Runs != 1 {
		t.Errorf("runs deleted = %d, want 1", rep.Counts.Runs)
	}
	if rep.WorkingRootsRemoved != 1 {
		t.Errorf("working roots removed = %d, want 1", rep.WorkingRootsRemoved)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("working root still on disk: %v", err)
	}
	if _, err := st.Runs().Get(ctx, "run-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run still in store: %v", err)
	}
}

// TestDeleteRun_DryRun leaves everything intact.
func TestDeleteRun_DryRun(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "t1", ts(7, 3), ts(7, 4))
	dir := mkRunDir(t, st, revID, "run-1", "t1", 0)

	rep, err := svc.DeleteRun(ctx, "run-1", true, true)
	if err != nil {
		t.Fatalf("DeleteRun dry: %v", err)
	}
	if !rep.DryRun || rep.WorkingRootsRemoved != 0 {
		t.Errorf("dry-run report unexpected: %+v", rep)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dry-run removed working root: %v", err)
	}
	if _, err := st.Runs().Get(ctx, "run-1"); err != nil {
		t.Errorf("dry-run deleted run: %v", err)
	}
}

// TestDeleteRun_NotFound maps store miss to ErrRunNotFound.
func TestDeleteRun_NotFound(t *testing.T) {
	svc := newService(t)
	if _, err := svc.DeleteRun(context.Background(), "ghost", false, true); !errors.Is(err, ErrRunNotFound) {
		t.Errorf("err = %v, want ErrRunNotFound", err)
	}
}

// TestDeleteTask removes the task, its runs, and their working roots.
func TestDeleteTask(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "t1", ts(7, 3), ts(7, 4))
	d0 := mkRunDir(t, st, revID, "run-0", "t1", 0)
	d1 := mkRunDir(t, st, revID, "run-1", "t1", 1)

	rep, err := svc.DeleteTask(ctx, "t1", false, true)
	if err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	if rep.Counts.Tasks != 1 || rep.Counts.Runs != 2 {
		t.Errorf("counts = %+v, want tasks=1 runs=2", rep.Counts)
	}
	if rep.WorkingRootsRemoved != 2 {
		t.Errorf("working roots removed = %d, want 2", rep.WorkingRootsRemoved)
	}
	for _, d := range []string{d0, d1} {
		if _, err := os.Stat(d); !os.IsNotExist(err) {
			t.Errorf("working root %s still present: %v", d, err)
		}
	}
	if _, err := st.Tasks().Get(ctx, "t1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("task still present: %v", err)
	}
}

// TestDeleteTask_NotFound maps store miss to ErrTaskNotFound.
func TestDeleteTask_NotFound(t *testing.T) {
	svc := newService(t)
	if _, err := svc.DeleteTask(context.Background(), "ghost", false, true); !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("err = %v, want ErrTaskNotFound", err)
	}
}

// TestClean_WindowSelection deletes only tasks in the window.
func TestClean_WindowSelection(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "early", ts(5, 1), ts(5, 2))
	mkTask(t, st, "late", ts(7, 2), ts(7, 3))
	mkRunDir(t, st, revID, "run-early", "early", 0)
	keepDir := mkRunDir(t, st, revID, "run-late", "late", 0)

	rep, err := svc.Clean(ctx, CleanFilter{Before: ts(6, 1), Basis: store.BasisProcessingWindow}, false, true)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if rep.Counts.Tasks != 1 || len(rep.TaskIDs) != 1 || rep.TaskIDs[0] != "early" {
		t.Errorf("clean selected %+v, want only early", rep.TaskIDs)
	}
	if _, err := st.Tasks().Get(ctx, "early"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("early task should be gone: %v", err)
	}
	if _, err := st.Tasks().Get(ctx, "late"); err != nil {
		t.Errorf("late task should survive: %v", err)
	}
	if _, err := os.Stat(keepDir); err != nil {
		t.Errorf("late working root should survive: %v", err)
	}
}

// TestClean_ProcessingTimeSelection deletes by created_at (the default basis).
func TestClean_ProcessingTimeSelection(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()

	mkAt := func(id string, created time.Time) {
		t.Helper()
		tk := &store.TaskRecord{
			TaskID:               id,
			SchemaVersion:        "veriproc.task-submission/v1",
			DestinationStationID: "SCENE-L2",
			CreatedAt:            created,
			// Identical sensing windows prove processing-time selects on created_at.
			WindowStart:        ts(1, 1),
			WindowEnd:          ts(1, 2),
			ClientMetadata:     json.RawMessage(`{"mission":"CO2M"}`),
			RoutingContent:     json.RawMessage(`{"destination":{"station_id":"SCENE-L2"}}`),
			RoutingContentHash: "sha256:routing-" + id,
			SubmissionOrigin:   "client",
			State:              "accepted",
		}
		if err := st.Tasks().Insert(ctx, tk); err != nil {
			t.Fatalf("task insert %s: %v", id, err)
		}
	}
	mkAt("early", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	mkAt("late", time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))

	rep, err := svc.Clean(ctx, CleanFilter{Before: time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)}, false, true)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if rep.Counts.Tasks != 1 || len(rep.TaskIDs) != 1 || rep.TaskIDs[0] != "early" {
		t.Errorf("clean selected %+v, want only early", rep.TaskIDs)
	}
	if _, err := st.Tasks().Get(ctx, "late"); err != nil {
		t.Errorf("late task should survive: %v", err)
	}
}

// TestClean_DryRun reports without deleting.
func TestClean_DryRun(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "early", ts(5, 1), ts(5, 2))
	dir := mkRunDir(t, st, revID, "run-early", "early", 0)

	rep, err := svc.Clean(ctx, CleanFilter{Before: ts(6, 1), Basis: store.BasisProcessingWindow}, true, true)
	if err != nil {
		t.Fatalf("Clean dry: %v", err)
	}
	if !rep.DryRun || rep.Counts.Tasks != 1 || rep.WorkingRootsRemoved != 0 {
		t.Errorf("dry-run report unexpected: %+v", rep)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dry-run removed working root: %v", err)
	}
	if _, err := st.Tasks().Get(ctx, "early"); err != nil {
		t.Errorf("dry-run deleted task: %v", err)
	}
}

// TestClean_NoCutoff errors when neither bound is set.
func TestClean_NoCutoff(t *testing.T) {
	svc := newService(t)
	if _, err := svc.Clean(context.Background(), CleanFilter{}, false, true); !errors.Is(err, ErrNoCutoff) {
		t.Errorf("err = %v, want ErrNoCutoff", err)
	}
}

// TestClean_EmptyMatch is a no-op success when nothing matches.
func TestClean_EmptyMatch(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	mkTask(t, st, "late", ts(7, 2), ts(7, 3))
	rep, err := svc.Clean(context.Background(), CleanFilter{Before: ts(6, 1), Basis: store.BasisProcessingWindow}, false, true)
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if rep.Counts.Tasks != 0 || len(rep.TaskIDs) != 0 {
		t.Errorf("expected empty report, got %+v", rep)
	}
}

// TestDeleteRun_NoCascade_RemovesWorkingRoot verifies non-cascade run
// deletion still removes the working root from disk.
func TestDeleteRun_NoCascade_RemovesWorkingRoot(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "t1", ts(7, 3), ts(7, 4))
	dir := mkRunDir(t, st, revID, "run-1", "t1", 0)

	rep, err := svc.DeleteRun(ctx, "run-1", false, false)
	if err != nil {
		t.Fatalf("DeleteRun no-cascade: %v", err)
	}
	if rep.Counts.Runs != 1 {
		t.Errorf("runs deleted = %d, want 1", rep.Counts.Runs)
	}
	if rep.WorkingRootsRemoved != 1 {
		t.Errorf("working roots removed = %d, want 1", rep.WorkingRootsRemoved)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("working root should be removed: %v", err)
	}
}

// TestDeleteTask_NoCascade_PreservesDescendants verifies non-cascade task
// deletion does not remove descendant tasks but removes working roots.
func TestDeleteTask_NoCascade_PreservesDescendants(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "t1", ts(7, 3), ts(7, 4))
	// Insert child task.
	child := &store.TaskRecord{
		TaskID:               "t2",
		ParentTaskID:         "t1",
		SchemaVersion:        "veriproc.task-submission/v1",
		DestinationStationID: "SCENE-L2",
		ClientMetadata:       json.RawMessage(`{}`),
		RoutingContent:       json.RawMessage(`{"destination":{"station_id":"SCENE-L2"}}`),
		RoutingContentHash:   "sha256:routing-t2",
		SubmissionOrigin:     "client",
		State:                "accepted",
	}
	if err := st.Tasks().Insert(ctx, child); err != nil {
		t.Fatalf("child task insert: %v", err)
	}
	dir1 := mkRunDir(t, st, revID, "run-1", "t1", 0)
	dir2 := mkRunDir(t, st, revID, "run-2", "t2", 0)

	rep, err := svc.DeleteTask(ctx, "t1", false, false)
	if err != nil {
		t.Fatalf("DeleteTask no-cascade: %v", err)
	}
	if len(rep.TaskIDs) != 1 || rep.TaskIDs[0] != "t1" {
		t.Errorf("task IDs = %v, want [t1]", rep.TaskIDs)
	}
	if rep.Counts.Runs != 1 {
		t.Errorf("runs deleted = %d, want 1 (only t1's run)", rep.Counts.Runs)
	}
	if rep.WorkingRootsRemoved != 1 {
		t.Errorf("working roots removed = %d, want 1 (t1's run)", rep.WorkingRootsRemoved)
	}
	// dir1 removed (belongs to deleted task t1).
	if _, err := os.Stat(dir1); !os.IsNotExist(err) {
		t.Errorf("working root %s should be removed: %v", dir1, err)
	}
	// dir2 still preserved (belongs to surviving child task t2).
	if _, err := os.Stat(dir2); err != nil {
		t.Errorf("child working root %s should be preserved: %v", dir2, err)
	}
	// Child task survives with orphaned parent_task_id.
	childRec, err := st.Tasks().Get(ctx, "t2")
	if err != nil {
		t.Fatalf("child task should survive: %v", err)
	}
	if childRec.ParentTaskID != "" {
		t.Errorf("child parent_task_id should be NULL, got %q", childRec.ParentTaskID)
	}
}

// TestClean_NoCascade_PreservesDescendants verifies clean without cascade
// only deletes matching tasks, not their descendants.
func TestClean_NoCascade_PreservesDescendants(t *testing.T) {
	st := newStore(t)
	svc := New(st, zerolog.Nop())
	ctx := context.Background()
	revID := mkStation(t, st, "SCENE-L2")
	mkTask(t, st, "early", ts(5, 1), ts(5, 2))
	// Insert child task with window AFTER the cutoff so IDsForCleanup only returns "early".
	child := &store.TaskRecord{
		TaskID:               "early-child",
		ParentTaskID:         "early",
		SchemaVersion:        "veriproc.task-submission/v1",
		DestinationStationID: "SCENE-L2",
		WindowStart:          ts(7, 1),
		WindowEnd:            ts(7, 2),
		ClientMetadata:       json.RawMessage(`{}`),
		RoutingContent:       json.RawMessage(`{"destination":{"station_id":"SCENE-L2"}}`),
		RoutingContentHash:   "sha256:routing-early-child",
		SubmissionOrigin:     "client",
		State:                "accepted",
	}
	if err := st.Tasks().Insert(ctx, child); err != nil {
		t.Fatalf("child task insert: %v", err)
	}
	mkRunDir(t, st, revID, "run-early", "early", 0)
	dirChild := mkRunDir(t, st, revID, "run-early-child", "early-child", 0)

	rep, err := svc.Clean(ctx, CleanFilter{Before: ts(6, 1), Basis: store.BasisProcessingWindow}, false, false)
	if err != nil {
		t.Fatalf("Clean no-cascade: %v", err)
	}
	if len(rep.TaskIDs) != 1 || rep.TaskIDs[0] != "early" {
		t.Errorf("task IDs = %v, want [early]", rep.TaskIDs)
	}
	// Child should NOT be in the deletion set.
	childRec, err := st.Tasks().Get(ctx, "early-child")
	if err != nil {
		t.Fatalf("child task should survive: %v", err)
	}
	if childRec.ParentTaskID != "" {
		t.Errorf("child parent_task_id should be NULL, got %q", childRec.ParentTaskID)
	}
	// Child working root preserved.
	if _, err := os.Stat(dirChild); err != nil {
		t.Errorf("child working root should be preserved: %v", err)
	}
}
