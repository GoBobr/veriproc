package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func mkStation(t *testing.T, s *Store, id string) string {
	t.Helper()
	rev := &StationRevisionRecord{
		RevisionID:    "rev-" + id,
		StationID:     id,
		StationName:   "Name " + id,
		ContentHash:   "sha256:test-" + id,
		SchemaVersion: "veriproc.station/v1",
	}
	if err := s.Stations().Insert(context.Background(), rev); err != nil {
		t.Fatalf("station insert: %v", err)
	}
	return rev.RevisionID
}

func mkTask(taskID string) *TaskRecord {
	return &TaskRecord{
		TaskID:               taskID,
		SchemaVersion:        "veriproc.task-submission/v1",
		DestinationStationID: "SCENE-L2",
		WindowStart:          time.Date(2025, 7, 3, 11, 15, 39, 0, time.UTC),
		WindowEnd:            time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
		Force:                false,
		ClientMetadata:       json.RawMessage(`{"mission":"CO2M"}`),
		RoutingContent:       json.RawMessage(`{"destination":{"station_id":"SCENE-L2"}}`),
		RoutingContentHash:   "sha256:routing-test",
		SubmissionOrigin:     "client",
		State:                "accepted",
	}
}

// TestTasks_InsertAndGet_4_3_1 — round-trip a task with all fields preserved.
func TestTasks_InsertAndGet_4_3_1(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	in := mkTask("t-1")
	if err := s.Tasks().Insert(ctx, in); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.Tasks().Get(ctx, "t-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.TaskID != "t-1" || got.DestinationStationID != "SCENE-L2" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Force != false {
		t.Errorf("force = %v, want false", got.Force)
	}
	if !got.WindowStart.Equal(in.WindowStart) || !got.WindowEnd.Equal(in.WindowEnd) {
		t.Errorf("window mismatch: got [%s,%s] want [%s,%s]",
			got.WindowStart, got.WindowEnd, in.WindowStart, in.WindowEnd)
	}
	if got.RoutingContentHash != in.RoutingContentHash {
		t.Errorf("routing hash mismatch")
	}
}

// TestTasks_GetMissing — Get returns ErrNotFound for unknown id.
func TestTasks_GetMissing(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Tasks().Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestTasks_DuplicateID_4_3_1 — re-inserting same task_id is ErrConflict.
func TestTasks_DuplicateID_4_3_1(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.Tasks().Insert(ctx, mkTask("t-dup")); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	err := s.Tasks().Insert(ctx, mkTask("t-dup"))
	if !errors.Is(err, ErrConflict) {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

// TestRuns_InsertWithFK_4_3_7 — run insert succeeds when task and station
// revision exist.
func TestRuns_InsertWithFK_4_3_7(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")
	if err := s.Tasks().Insert(ctx, mkTask("t-r1")); err != nil {
		t.Fatalf("task: %v", err)
	}
	run := &RunRecord{
		RunID:             "run-1",
		TaskID:            "t-r1",
		StationRevisionID: revID,
		RetryIndex:        0,
		WorkingRoot:       "/wr/run-1",
		State:             "pending",
		Canonicality:      "pending",
	}
	if err := s.Runs().Insert(ctx, run); err != nil {
		t.Fatalf("run insert: %v", err)
	}
	got, err := s.Runs().Get(ctx, "run-1")
	if err != nil {
		t.Fatalf("run get: %v", err)
	}
	if got.WorkingRoot != "/wr/run-1" {
		t.Errorf("working_root mismatch: %q", got.WorkingRoot)
	}
}

// TestRuns_RejectMissingFK_4_3_7 — inserting a run without a task is conflict.
func TestRuns_RejectMissingFK_4_3_7(t *testing.T) {
	s := newTestStore(t)
	revID := mkStation(t, s, "SCENE-L2")
	err := s.Runs().Insert(context.Background(), &RunRecord{
		RunID: "x", TaskID: "missing", StationRevisionID: revID,
		RetryIndex: 0, WorkingRoot: "/wr/x", State: "pending", Canonicality: "pending",
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict for missing task FK, got %v", err)
	}
}

// TestRuns_UniqueRetryIndex_7_4_1 — two runs for the same task must have
// distinct retry_index values (Spec §7.4.1: retries create new identity).
func TestRuns_UniqueRetryIndex_7_4_1(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")
	if err := s.Tasks().Insert(ctx, mkTask("t-retry")); err != nil {
		t.Fatalf("task: %v", err)
	}
	r := func(id string, idx int) *RunRecord {
		return &RunRecord{RunID: id, TaskID: "t-retry", StationRevisionID: revID,
			RetryIndex: idx, WorkingRoot: "/wr/" + id, State: "pending", Canonicality: "pending"}
	}
	if err := s.Runs().Insert(ctx, r("run-a", 0)); err != nil {
		t.Fatalf("first run: %v", err)
	}
	err := s.Runs().Insert(ctx, r("run-b", 0))
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict for duplicate (task, retry_index), got %v", err)
	}
	if err := s.Runs().Insert(ctx, r("run-c", 1)); err != nil {
		t.Errorf("retry_index=1 should succeed: %v", err)
	}
}

// TestRuns_UniqueWorkingRoot_7_4_1 — working_root must be globally unique.
func TestRuns_UniqueWorkingRoot_7_4_1(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SCENE-L2")
	if err := s.Tasks().Insert(ctx, mkTask("t-wr1")); err != nil {
		t.Fatal(err)
	}
	if err := s.Tasks().Insert(ctx, mkTask("t-wr2")); err != nil {
		// mkTask reuses task id "t-wr1" — fix:
	}
	t1 := mkTask("t-wr-a")
	t2 := mkTask("t-wr-b")
	_ = s.Tasks().Insert(ctx, t1)
	_ = s.Tasks().Insert(ctx, t2)
	if err := s.Runs().Insert(ctx, &RunRecord{
		RunID: "wra", TaskID: "t-wr-a", StationRevisionID: revID, RetryIndex: 0,
		WorkingRoot: "/wr/shared", State: "pending", Canonicality: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	err := s.Runs().Insert(ctx, &RunRecord{
		RunID: "wrb", TaskID: "t-wr-b", StationRevisionID: revID, RetryIndex: 0,
		WorkingRoot: "/wr/shared", State: "pending", Canonicality: "pending",
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict for duplicate working_root, got %v", err)
	}
}

// TestStations_DuplicateContentHash — (station_id, content_hash) is unique.
func TestStations_DuplicateContentHash(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	mkStation(t, s, "S1")
	err := s.Stations().Insert(ctx, &StationRevisionRecord{
		RevisionID: "rev-other", StationID: "S1", ContentHash: "sha256:test-S1",
		SchemaVersion: "veriproc.station/v1",
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

// TestRuns_ListSorting — verifies that SortBy and SortDir are honoured.
func TestRuns_ListSorting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "SORT-ST")

	// Insert tasks and runs with known ordering.
	tasks := []string{"task-C", "task-A", "task-B"}
	for _, tid := range tasks {
		tk := mkTask(tid)
		tk.DestinationStationID = "SORT-ST"
		if err := s.Tasks().Insert(ctx, tk); err != nil {
			t.Fatalf("insert task %s: %v", tid, err)
		}
	}

	runs := []struct {
		runID     string
		taskID    string
		createdAt time.Time
	}{
		{"run-3", "task-C", time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)},
		{"run-1", "task-A", time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)},
		{"run-2", "task-B", time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC)},
	}
	for _, r := range runs {
		rec := &RunRecord{
			RunID:             r.runID,
			TaskID:            r.taskID,
			StationRevisionID: revID,
			RetryIndex:        0,
			WorkingRoot:       "/wr/" + r.runID,
			State:             "pending",
			Canonicality:      "pending",
			CreatedAt:         r.createdAt,
		}
		if err := s.Runs().Insert(ctx, rec); err != nil {
			t.Fatalf("insert run %s: %v", r.runID, err)
		}
	}

	tests := []struct {
		name    string
		sortBy  string
		sortDir string
		want    []string // run_ids in expected order
	}{
		{"run_id DESC (default)", "", "", []string{"run-3", "run-2", "run-1"}},
		{"run_id ASC", "run_id", "ASC", []string{"run-1", "run-2", "run-3"}},
		{"created_at DESC", "created_at", "DESC", []string{"run-3", "run-2", "run-1"}},
		{"created_at ASC", "created_at", "ASC", []string{"run-1", "run-2", "run-3"}},
		{"task_id ASC", "task_id", "ASC", []string{"run-1", "run-2", "run-3"}},
		{"task_id DESC", "task_id", "DESC", []string{"run-3", "run-2", "run-1"}},
		{"invalid sort falls back", "'; DROP TABLE", "", []string{"run-3", "run-2", "run-1"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			page, err := s.Runs().List(ctx, RunListFilter{
				StationID: "SORT-ST",
				Limit:     50,
				SortBy:    tc.sortBy,
				SortDir:   tc.sortDir,
			})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(page.Items) != len(tc.want) {
				t.Fatalf("got %d runs, want %d", len(page.Items), len(tc.want))
			}
			for i, r := range page.Items {
				if r.RunID != tc.want[i] {
					t.Errorf("position %d: got %s, want %s", i, r.RunID, tc.want[i])
				}
			}
		})
	}
}

// TestNormaliseRunSortBy — whitelist validation.
func TestNormaliseRunSortBy(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"run_id", "run_id"},
		{"created_at", "created_at"},
		{"task_id", "task_id"},
		{"", "run_id"},
		{"malicious", "run_id"},
		{"'; DROP TABLE runs;--", "run_id"},
	}
	for _, tc := range tests {
		if got := NormaliseRunSortBy(tc.input); got != tc.want {
			t.Errorf("NormaliseRunSortBy(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestNormaliseRunSortDir — ASC/DESC normalisation.
func TestNormaliseRunSortDir(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ASC", "ASC"},
		{"DESC", "DESC"},
		{"asc", "ASC"},
		{"desc", "DESC"},
		{"", "DESC"},
		{"random", "DESC"},
	}
	for _, tc := range tests {
		if got := NormaliseRunSortDir(tc.input); got != tc.want {
			t.Errorf("NormaliseRunSortDir(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// TestRuns_ListCursorWithSortByTaskID — verifies that cursor pagination
// works correctly when sorting by task_id. The cursor must filter on
// task_id, not created_at, otherwise rows are incorrectly dropped.
func TestRuns_ListCursorWithSortByTaskID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "CUR-ST")

	// Insert 5 runs across 3 tasks with interleaved created_at values so
	// that a created_at-based cursor would give wrong results.
	runs := []struct {
		runID     string
		taskID    string
		retryIdx  int
		createdAt time.Time
	}{
		{"r-1", "task-A", 0, time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)},
		{"r-2", "task-B", 0, time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC)},  // earlier created_at but later task_id
		{"r-3", "task-A", 1, time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC)},
		{"r-4", "task-C", 0, time.Date(2026, 5, 28, 8, 0, 0, 0, time.UTC)},
		{"r-5", "task-B", 1, time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)},
	}
	for _, r := range runs {
		tk := mkTask(r.taskID)
		tk.DestinationStationID = "CUR-ST"
		// Avoid duplicate task_id conflict — only insert if not already present.
		_ = s.Tasks().Insert(ctx, tk)
		rec := &RunRecord{
			RunID:             r.runID,
			TaskID:            r.taskID,
			StationRevisionID: revID,
			RetryIndex:        r.retryIdx,
			WorkingRoot:       "/wr/" + r.runID,
			State:             "pending",
			Canonicality:      "pending",
			CreatedAt:         r.createdAt,
		}
		if err := s.Runs().Insert(ctx, rec); err != nil {
			t.Fatalf("insert run %s: %v", r.runID, err)
		}
	}

	// Sort by task_id ASC, page size 2.
	// Expected order: task-A/r-1, task-A/r-3, task-B/r-2, task-B/r-5, task-C/r-4
	page1, err := s.Runs().List(ctx, RunListFilter{
		StationID: "CUR-ST",
		Limit:     2,
		SortBy:    "task_id",
		SortDir:   "ASC",
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 2 {
		t.Fatalf("page1: got %d items, want 2", len(page1.Items))
	}
	if page1.Items[0].RunID != "r-1" || page1.Items[1].RunID != "r-3" {
		t.Fatalf("page1 order: got %s,%s want r-1,r-3", page1.Items[0].RunID, page1.Items[1].RunID)
	}
	if !page1.HasMore {
		t.Fatal("page1: expected HasMore=true")
	}

	// Page 2: use cursor from page1.
	page2, err := s.Runs().List(ctx, RunListFilter{
		StationID:    "CUR-ST",
		Limit:        2,
		SortBy:       "task_id",
		SortDir:      "ASC",
		CursorRunID:  page1.NextRunID,
		CursorTaskID: page1.NextTaskID,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Items) != 2 {
		t.Fatalf("page2: got %d items, want 2", len(page2.Items))
	}
	// Should get task-B/r-2, task-B/r-5
	if page2.Items[0].RunID != "r-2" || page2.Items[1].RunID != "r-5" {
		t.Fatalf("page2 order: got %s,%s want r-2,r-5", page2.Items[0].RunID, page2.Items[1].RunID)
	}

	// Page 3: should get the last item task-C/r-4
	page3, err := s.Runs().List(ctx, RunListFilter{
		StationID:    "CUR-ST",
		Limit:        2,
		SortBy:       "task_id",
		SortDir:      "ASC",
		CursorRunID:  page2.NextRunID,
		CursorTaskID: page2.NextTaskID,
	})
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3.Items) != 1 {
		t.Fatalf("page3: got %d items, want 1", len(page3.Items))
	}
	if page3.Items[0].RunID != "r-4" {
		t.Fatalf("page3: got %s, want r-4", page3.Items[0].RunID)
	}
	if page3.HasMore {
		t.Fatal("page3: expected HasMore=false")
	}
}

// TestRuns_ListCursorWithSortByRunID — cursor pagination when sorting by
// run_id (the default). The cursor should filter on run_id only.
func TestRuns_ListCursorWithSortByRunID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	revID := mkStation(t, s, "RID-ST")

	for _, r := range []struct {
		runID    string
		taskID   string
		retryIdx int
	}{
		{"run-5", "task-A", 0},
		{"run-3", "task-A", 1},
		{"run-1", "task-B", 0},
		{"run-4", "task-B", 1},
		{"run-2", "task-C", 0},
	} {
		tk := mkTask(r.taskID)
		tk.DestinationStationID = "RID-ST"
		_ = s.Tasks().Insert(ctx, tk)
		rec := &RunRecord{
			RunID: r.runID, TaskID: r.taskID, StationRevisionID: revID,
			RetryIndex: r.retryIdx, WorkingRoot: "/wr/" + r.runID, State: "pending", Canonicality: "pending",
		}
		if err := s.Runs().Insert(ctx, rec); err != nil {
			t.Fatalf("insert %s: %v", r.runID, err)
		}
	}

	// Sort by run_id DESC, page size 3.
	// Expected order: run-5, run-4, run-3, run-2, run-1
	page1, err := s.Runs().List(ctx, RunListFilter{
		StationID: "RID-ST",
		Limit:     3,
		SortBy:    "run_id",
		SortDir:   "DESC",
	})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Items) != 3 {
		t.Fatalf("page1: got %d, want 3", len(page1.Items))
	}
	if page1.Items[0].RunID != "run-5" || page1.Items[1].RunID != "run-4" || page1.Items[2].RunID != "run-3" {
		t.Fatalf("page1: got %s,%s,%s want run-5,run-4,run-3",
			page1.Items[0].RunID, page1.Items[1].RunID, page1.Items[2].RunID)
	}

	page2, err := s.Runs().List(ctx, RunListFilter{
		StationID:   "RID-ST",
		Limit:       3,
		SortBy:      "run_id",
		SortDir:     "DESC",
		CursorRunID: page1.NextRunID,
	})
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Items) != 2 {
		t.Fatalf("page2: got %d, want 2", len(page2.Items))
	}
	if page2.Items[0].RunID != "run-2" || page2.Items[1].RunID != "run-1" {
		t.Fatalf("page2: got %s,%s want run-2,run-1", page2.Items[0].RunID, page2.Items[1].RunID)
	}
}

// TestIdempotency_InsertAndGet — basic round-trip with (scope, key) uniqueness.
func TestIdempotency_InsertAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := &IdempotencyRecord{
		ID: "ir-1", Scope: "tasks.submit", Key: "k1",
		RequestHash: "sha256:abc", TaskID: "t-1",
	}
	if err := s.Idempotency().Insert(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Idempotency().GetByKey(ctx, "tasks.submit", "k1")
	if err != nil {
		t.Fatal(err)
	}
	if got.RequestHash != "sha256:abc" || got.TaskID != "t-1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}

	// Re-insert with same key → ErrConflict.
	err = s.Idempotency().Insert(ctx, &IdempotencyRecord{
		ID: "ir-2", Scope: "tasks.submit", Key: "k1",
		RequestHash: "sha256:other",
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

// TestStore_InTx_RollbackOnError — failed transaction rolls back.
func TestStore_InTx_RollbackOnError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	wantErr := errors.New("intentional")
	err := s.InTx(ctx, func(tx *Tx) error {
		if err := tx.Tasks().Insert(ctx, mkTask("t-tx")); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v", err)
	}
	if _, err := s.Tasks().Get(ctx, "t-tx"); !errors.Is(err, ErrNotFound) {
		t.Errorf("task should not exist after rollback, got %v", err)
	}
}
