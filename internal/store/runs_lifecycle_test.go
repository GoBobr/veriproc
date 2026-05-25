package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func mkRunReady(t *testing.T, s *Store, taskID, retryIdx string) string {
	t.Helper()
	ctx := context.Background()
	revID := mkStation(t, s, "ST-"+taskID)
	tk := mkTask(taskID)
	tk.DestinationStationID = "ST-" + taskID
	if err := s.Tasks().Insert(ctx, tk); err != nil {
		t.Fatalf("task insert: %v", err)
	}
	r := &RunRecord{
		RunID:             "run-" + taskID + "-" + retryIdx,
		TaskID:            taskID,
		StationRevisionID: revID,
		RetryIndex:        0,
		WorkingRoot:       "/tmp/wr/" + taskID + "/" + retryIdx,
		State:             "preparing",
		Canonicality:      "pending",
		CreatedAt:         time.Now().UTC(),
	}
	if err := s.Runs().Insert(ctx, r); err != nil {
		t.Fatalf("run insert: %v", err)
	}
	return r.RunID
}

// TestStore_RunStateTransitions_3_7 — state machine progresses pending→
// preparing→ready→dispatched→running→finalizing→complete via conditional
// updates; conflicting transitions are rejected.
func TestStore_RunStateTransitions_3_7(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	runID := mkRunReady(t, s, "task-A", "0")
	now := time.Now().UTC()

	if err := s.Runs().MarkPrepared(ctx, runID, "fp-1", now); err != nil {
		t.Fatalf("MarkPrepared: %v", err)
	}
	r, _ := s.Runs().Get(ctx, runID)
	if r.State != "ready" || r.ProcessingFingerprint != "fp-1" {
		t.Fatalf("after MarkPrepared: state=%s fp=%s", r.State, r.ProcessingFingerprint)
	}

	// Dispatch only allowed from ready.
	if err := s.Runs().MarkDispatched(ctx, runID, now); err != nil {
		t.Fatalf("MarkDispatched: %v", err)
	}
	if err := s.Runs().MarkDispatched(ctx, runID, now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("re-dispatch: want ErrInvalidTransition, got %v", err)
	}

	if err := s.Runs().MarkRunning(ctx, runID, now); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := s.Runs().MarkReadyForFinalization(ctx, runID); err != nil {
		t.Fatalf("MarkReadyForFinalization: %v", err)
	}
	if err := s.Runs().MarkComplete(ctx, runID, "canonical", now); err != nil {
		t.Fatalf("MarkComplete: %v", err)
	}
	r, _ = s.Runs().Get(ctx, runID)
	if r.State != "complete" || r.Canonicality != "canonical" || !r.TerminalAt.Valid {
		t.Fatalf("after MarkComplete: %+v", r)
	}

	// Cannot re-complete a complete run.
	if err := s.Runs().MarkComplete(ctx, runID, "canonical", now); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("re-complete: want ErrInvalidTransition, got %v", err)
	}
}

// TestStore_RunListByStates — ListByStates returns ordered records.
func TestStore_RunListByStates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	r1 := mkRunReady(t, s, "task-1", "0")
	r2 := mkRunReady(t, s, "task-2", "0")
	now := time.Now().UTC()
	_ = s.Runs().MarkPrepared(ctx, r1, "fp-x", now)
	_ = s.Runs().MarkPrepared(ctx, r2, "fp-y", now)

	got, err := s.Runs().ListByStates(ctx, "ready")
	if err != nil {
		t.Fatalf("ListByStates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

// TestStore_Manifest_FrozenAtConstruction_3_8 — manifest insert is atomic
// header+entries; one manifest per run.
func TestStore_Manifest_FrozenAtConstruction_3_8(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	runID := mkRunReady(t, s, "task-mf", "0")

	m := &ManifestRecord{
		ManifestID: "mf-1",
		RunID:      runID,
		Entries: []ManifestEntry{
			{EntryID: "e1", FileType: "L1B", Optional: false, Present: true, EffectiveFilenamePattern: "CDM?_L1B_____________*", FilenameComponents: `{"file_type":"L1B"}`, WindowMatch: "overlaps"},
			{EntryID: "e2", FileType: "AUX", Optional: true, Present: false},
		},
	}
	if err := s.Manifests().Insert(ctx, m); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := s.Manifests().GetByRun(ctx, runID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.Entries))
	}
	if got.Entries[0].EffectiveFilenamePattern == "" || got.Entries[0].FilenameComponents == "" || got.Entries[0].WindowMatch != "overlaps" {
		t.Fatalf("structured manifest metadata not persisted: %#v", got.Entries[0])
	}
	// Second insert for same run must conflict (one manifest per run).
	m2 := &ManifestRecord{ManifestID: "mf-2", RunID: runID}
	if err := s.Manifests().Insert(ctx, m2); !errors.Is(err, ErrConflict) {
		t.Fatalf("dup manifest: want ErrConflict, got %v", err)
	}
}

// TestStore_Fingerprint_UniqueAndClaim_3_10 — fingerprints are unique by
// value and canonical claim is exclusive (first wins).
func TestStore_Fingerprint_UniqueAndClaim_3_10(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	runID1 := mkRunReady(t, s, "task-fp1", "0")
	runID2 := mkRunReady(t, s, "task-fp2", "0")

	rec1, err := s.Fingerprints().Upsert(ctx, "fp-shared")
	if err != nil {
		t.Fatalf("upsert1: %v", err)
	}
	rec2, err := s.Fingerprints().Upsert(ctx, "fp-shared")
	if err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if rec1.FingerprintID != rec2.FingerprintID {
		t.Fatalf("upsert returned different ids: %s vs %s", rec1.FingerprintID, rec2.FingerprintID)
	}
	won1, err := s.Fingerprints().ClaimCanonical(ctx, rec1.FingerprintID, runID1)
	if err != nil || !won1 {
		t.Fatalf("claim1: won=%v err=%v", won1, err)
	}
	won2, err := s.Fingerprints().ClaimCanonical(ctx, rec1.FingerprintID, runID2)
	if err != nil || won2 {
		t.Fatalf("claim2: must lose, got won=%v err=%v", won2, err)
	}
}

// TestStore_Job_FK_4_3_8 — job referencing an unknown run is rejected.
func TestStore_Job_FK_4_3_8(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	j := &JobRecord{JobID: "j1", RunID: "no-such-run", Executor: "stub"}
	if err := s.Jobs().Insert(ctx, j); !errors.Is(err, ErrConflict) {
		t.Fatalf("orphan job insert: want ErrConflict, got %v", err)
	}
}
