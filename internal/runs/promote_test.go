package runs_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eum/veriproc/internal/runs"
	"github.com/eum/veriproc/internal/tasks"
)

// finalizeTaskRun runs the standard happy-path lifecycle for the given task
// and returns the resulting run record (state=complete).
func finalizeTaskRun(t *testing.T, f *fixture, taskID string) string {
	t.Helper()
	ctx := context.Background()
	r, err := f.runs.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	for i := 0; i < 8; i++ {
		if err := f.dispatch.Tick(ctx); err != nil {
			t.Fatalf("tick: %v", err)
		}
		got, err := f.st.Runs().Get(ctx, r.RunID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.State == "complete" {
			return r.RunID
		}
		if got.State == "failed" || got.State == "cancelled" {
			t.Fatalf("unexpected state %q", got.State)
		}
	}
	t.Fatalf("run did not complete")
	return r.RunID
}

// TestRuns_ForcedRerunNonCanonical_3_15_M6 — submitting a forced rerun of an
// already-canonical task yields a "forced" run that does NOT replace the
// task's canonical_run_id (Spec §3.15).
func TestRuns_ForcedRerunNonCanonical_3_15_M6(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)
	canonRunID := finalizeTaskRun(t, f, taskID)

	// Force a rerun by submitting a task with Force=true and the same window.
	res, err := f.tasks.Submit(ctx, tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "SCENE-L2"},
		Window: tasks.Window{
			Start: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC),
			End:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
		},
		Force: true,
	})
	if err != nil {
		t.Fatalf("submit forced: %v", err)
	}
	forcedTaskID := res.Task.TaskID
	if forcedTaskID == taskID {
		t.Fatalf("forced submit should produce a new task; got %s", forcedTaskID)
	}
	forcedRunID := finalizeTaskRun(t, f, forcedTaskID)

	forced, err := f.st.Runs().Get(ctx, forcedRunID)
	if err != nil {
		t.Fatalf("get forced run: %v", err)
	}
	if forced.Canonicality != "forced" {
		t.Errorf("forced run canonicality = %q, want forced", forced.Canonicality)
	}

	// Original canonical task still points at canonRunID.
	origTask, err := f.st.Tasks().Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get orig task: %v", err)
	}
	if origTask.CanonicalRunID != canonRunID {
		t.Errorf("original task canonical_run_id = %q, want %s",
			origTask.CanonicalRunID, canonRunID)
	}

	// Forced task has no canonical run.
	forcedTask, err := f.st.Tasks().Get(ctx, forcedTaskID)
	if err != nil {
		t.Fatalf("get forced task: %v", err)
	}
	if forcedTask.CanonicalRunID != "" {
		t.Errorf("forced task canonical_run_id = %q, want empty",
			forcedTask.CanonicalRunID)
	}
}

// TestRuns_PromoteCanonical_M6 — a non-canonical complete run can be promoted
// by an operator. The previous canonical run becomes "duplicate" and an audit
// entry is recorded. Spec §3.16, §7.4.5.
func TestRuns_PromoteCanonical_M6(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)
	canonRunID := finalizeTaskRun(t, f, taskID)

	// Create a duplicate run for the same task (retry of the same window).
	dupRunID := finalizeTaskRun(t, f, taskID)
	if dupRunID == canonRunID {
		t.Fatalf("duplicate run id collision")
	}

	dup, err := f.st.Runs().Get(ctx, dupRunID)
	if err != nil {
		t.Fatalf("get dup: %v", err)
	}
	if dup.Canonicality != "duplicate" {
		t.Fatalf("dup canonicality = %q, want duplicate", dup.Canonicality)
	}

	out, err := f.runs.PromoteCanonical(ctx, dupRunID, "operator-rebuild", "alice")
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if out.PreviousRunID != canonRunID {
		t.Errorf("previous_run_id = %q, want %s", out.PreviousRunID, canonRunID)
	}
	if out.Run.Canonicality != "canonical" {
		t.Errorf("promoted canonicality = %q, want canonical", out.Run.Canonicality)
	}

	prev, err := f.st.Runs().Get(ctx, canonRunID)
	if err != nil {
		t.Fatalf("get prev: %v", err)
	}
	if prev.Canonicality != "duplicate" {
		t.Errorf("previous run canonicality = %q, want duplicate", prev.Canonicality)
	}

	// Audit row recorded.
	audits, err := f.st.Canonicality().ListByFingerprint(ctx, out.FingerprintID)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(audits) < 2 {
		t.Fatalf("audit rows = %d, want >= 2 (elect + promote)", len(audits))
	}
	last := audits[len(audits)-1]
	if last.Action != "promote" || last.NewRunID != dupRunID || last.Actor != "alice" {
		t.Errorf("last audit = %+v", last)
	}
}

// TestRuns_PromoteIneligible_M6 — promoting a non-complete run fails with
// ErrPromoteIneligible.
func TestRuns_PromoteIneligible_M6(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(ctx, taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	_, err = f.runs.PromoteCanonical(ctx, r.RunID, "x", "alice")
	if !errors.Is(err, runs.ErrPromoteIneligible) {
		t.Errorf("err = %v, want ErrPromoteIneligible", err)
	}
}
