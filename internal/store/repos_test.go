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

// TestTasks_InsertAndGet_4_3_1_M1 — round-trip a task with all fields preserved.
func TestTasks_InsertAndGet_4_3_1_M1(t *testing.T) {
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

// TestTasks_GetMissing_M1 — Get returns ErrNotFound for unknown id.
func TestTasks_GetMissing_M1(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Tasks().Get(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// TestTasks_DuplicateID_4_3_1_M1 — re-inserting same task_id is ErrConflict.
func TestTasks_DuplicateID_4_3_1_M1(t *testing.T) {
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

// TestRuns_InsertWithFK_4_3_7_M1 — run insert succeeds when task and station
// revision exist.
func TestRuns_InsertWithFK_4_3_7_M1(t *testing.T) {
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

// TestRuns_RejectMissingFK_4_3_7_M1 — inserting a run without a task is conflict.
func TestRuns_RejectMissingFK_4_3_7_M1(t *testing.T) {
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

// TestRuns_UniqueRetryIndex_7_4_1_M1 — two runs for the same task must have
// distinct retry_index values (Spec §7.4.1: retries create new identity).
func TestRuns_UniqueRetryIndex_7_4_1_M1(t *testing.T) {
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

// TestRuns_UniqueWorkingRoot_7_4_1_M1 — working_root must be globally unique.
func TestRuns_UniqueWorkingRoot_7_4_1_M1(t *testing.T) {
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

// TestStations_DuplicateContentHash_M1 — (station_id, content_hash) is unique.
func TestStations_DuplicateContentHash_M1(t *testing.T) {
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

// TestIdempotency_InsertAndGet_M1 — basic round-trip with (scope, key) uniqueness.
func TestIdempotency_InsertAndGet_M1(t *testing.T) {
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

// TestStore_InTx_RollbackOnError_M1 — failed transaction rolls back.
func TestStore_InTx_RollbackOnError_M1(t *testing.T) {
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
