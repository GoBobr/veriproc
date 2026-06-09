package runs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/runs"
)

// TestCancel_BlockedByReconciliation — when reconciliation_started_at is
// stamped, Cancel must short-circuit with ErrReconciliationInProgress so the
// HTTP layer can return 409 reconciliation_in_progress (Spec §3.13 / §7.8).
func TestCancel_BlockedByReconciliation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := f.st.Runs().MarkReconciliationStarted(ctx, r.RunID, time.Now().UTC()); err != nil {
		t.Fatalf("mark: %v", err)
	}
	_, err = f.runs.Cancel(ctx, r.RunID)
	if !errors.Is(err, runs.ErrReconciliationInProgress) {
		t.Fatalf("err = %v, want ErrReconciliationInProgress", err)
	}
}

// TestPromote_BlockedByReconciliation — likewise PromoteCanonical must
// fail-fast while reconciliation is in progress.
func TestPromote_BlockedByReconciliation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if err := f.st.Runs().MarkReconciliationStarted(ctx, r.RunID, time.Now().UTC()); err != nil {
		t.Fatalf("mark: %v", err)
	}
	_, err = f.runs.PromoteCanonical(ctx, r.RunID, "operator override", "alice")
	if !errors.Is(err, runs.ErrReconciliationInProgress) {
		t.Fatalf("err = %v, want ErrReconciliationInProgress", err)
	}
}
