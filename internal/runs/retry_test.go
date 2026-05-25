package runs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/runs"
)

// TestRuns_Retry_FailedTask_3_7 — Retry on a failed task creates a new run
// with RetryIndex 1 and resets the task state to "accepted". Spec §3.7.
func TestRuns_Retry_FailedTask_3_7(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)

	// Prepare and dispatch the first run.
	r0, err := f.runs.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	r0, err = f.runs.Dispatch(ctx, r0.RunID)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Make the first run fail via the stub executor.
	schedID := "stub-" + r0.RunID
	if err := f.exec.SetOutcome(schedID, executor.StatusFailed); err != nil {
		t.Fatalf("SetOutcome: %v", err)
	}
	// Poll until failed.
	for i := 0; i < 5; i++ {
		if _, err := f.runs.Poll(ctx, r0.RunID); err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
	}

	// Confirm task is now in "failed" state.
	tk, err := f.st.Tasks().Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if tk.State != "failed" {
		t.Fatalf("task state = %q before retry, want failed", tk.State)
	}

	// Retry the task.
	out, err := f.runs.Retry(ctx, taskID)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}

	// New run must have RetryIndex 1 (original run was index 0).
	if out.RetryIndex != 1 {
		t.Errorf("RetryIndex = %d, want 1", out.RetryIndex)
	}
	if out.Run.TaskID != taskID {
		t.Errorf("run.TaskID = %q, want %q", out.Run.TaskID, taskID)
	}
	if out.Run.State != "ready" {
		t.Errorf("run.State = %q, want ready (dispatcher will pick it up)", out.Run.State)
	}
	if out.Run.RunID == r0.RunID {
		t.Error("retry run must have a different ID than the original failed run")
	}
	if out.Run.WorkingRoot == r0.WorkingRoot {
		t.Error("retry run must have a distinct working root (Spec §3.7)")
	}

	// Task state must be reset to "accepted".
	tk2, _ := f.st.Tasks().Get(ctx, taskID)
	if tk2.State != "accepted" {
		t.Errorf("task.State = %q after Retry, want accepted", tk2.State)
	}
	// latest_run_id must point at the new run.
	if tk2.LatestRunID != out.Run.RunID {
		t.Errorf("latest_run_id = %q, want %q", tk2.LatestRunID, out.Run.RunID)
	}
}

// TestRuns_Retry_NotFound_3_7 — Retry on an unknown task returns
// ErrTaskNotFound.
func TestRuns_Retry_NotFound_3_7(t *testing.T) {
	f := newFixture(t)
	_, err := f.runs.Retry(context.Background(), "task-does-not-exist")
	if !errors.Is(err, runs.ErrTaskNotFound) {
		t.Errorf("err = %v, want ErrTaskNotFound", err)
	}
}

// TestRuns_Retry_IneligibleAccepted_3_7 — Retry on a task that has just
// been submitted (state "accepted") returns ErrRetryIneligible because there
// is already an active lifecycle in progress.
func TestRuns_Retry_IneligibleAccepted_3_7(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)

	_, err := f.runs.Retry(context.Background(), taskID)
	if !errors.Is(err, runs.ErrRetryIneligible) {
		t.Errorf("err = %v, want ErrRetryIneligible (task is accepted, not failed)", err)
	}
}

// TestRuns_Retry_IneligibleCompleted_3_7 — Retry on a successfully
// completed task returns ErrRetryIneligible.
func TestRuns_Retry_IneligibleCompleted_3_7(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)

	// Drive to completion via the dispatcher.
	waitForTaskState(t, ctx, f.st, f.dispatch, taskID, "completed")

	_, err := f.runs.Retry(ctx, taskID)
	if !errors.Is(err, runs.ErrRetryIneligible) {
		t.Errorf("err = %v, want ErrRetryIneligible (task completed)", err)
	}
}

// TestRuns_Retry_DispatcherPicksUpRetry_3_7 — after Retry creates a new
// run in "ready" state, the dispatcher's dispatchReady pass picks it up and
// drives it to completion. Spec §3.7.
func TestRuns_Retry_DispatcherPicksUpRetry_3_7(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)

	// Prepare and dispatch the first run, then make it fail.
	r0, _ := f.runs.PrepareRun(ctx, taskID)
	r0, _ = f.runs.Dispatch(ctx, r0.RunID)
	schedID := "stub-" + r0.RunID
	_ = f.exec.SetOutcome(schedID, executor.StatusFailed)
	for i := 0; i < 5; i++ {
		_, _ = f.runs.Poll(ctx, r0.RunID)
	}

	// Retry the task.
	out, err := f.runs.Retry(ctx, taskID)
	if err != nil {
		t.Fatalf("Retry: %v", err)
	}
	retryRunID := out.Run.RunID

	// Drive the retry run to completion using the dispatcher. The stub
	// executor uses its default succeeded lifecycle for the new run.
	waitForTaskState(t, ctx, f.st, f.dispatch, taskID, "completed")

	tk, _ := f.st.Tasks().Get(ctx, taskID)
	if tk.State != "completed" {
		t.Errorf("task.State = %q, want completed", tk.State)
	}
	if tk.CanonicalRunID != retryRunID {
		t.Errorf("canonical_run_id = %q, want retry run %q", tk.CanonicalRunID, retryRunID)
	}
}
