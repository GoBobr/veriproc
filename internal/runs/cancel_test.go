package runs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/runs"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

// TestRuns_CancelDispatched_5_8_M5 — happy-path: cancel a dispatched run.
// Spec §5.8 + §7.5.7.
func TestRuns_CancelDispatched_5_8_M5(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.runs.Dispatch(context.Background(), r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	out, err := f.runs.Cancel(context.Background(), r.RunID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if !out.Accepted {
		t.Errorf("Accepted=false, want true")
	}
	if !out.CancellationComplete {
		t.Errorf("CancellationComplete=false, want true (stub cancels synchronously)")
	}
	if out.Run.State != "cancelled" {
		t.Errorf("run.State = %q, want cancelled", out.Run.State)
	}
	if !out.Run.CancellationRequestedAt.Valid {
		t.Errorf("cancellation_requested_at not stamped")
	}
}

// TestRuns_CancelTerminal_5_8_M5 — re-cancelling a terminal run is a no-op
// returning AlreadyTerminal=true with no error.
func TestRuns_CancelTerminal_5_8_M5(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.runs.Dispatch(context.Background(), r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, err := f.runs.Cancel(context.Background(), r.RunID); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	out, err := f.runs.Cancel(context.Background(), r.RunID)
	if err != nil {
		t.Fatalf("second cancel: %v", err)
	}
	if !out.AlreadyTerminal {
		t.Errorf("AlreadyTerminal=false, want true on idempotent re-cancel")
	}
}

// TestRuns_CancelNotFound_5_8_M5 — unknown run returns ErrRunNotFound.
func TestRuns_CancelNotFound_5_8_M5(t *testing.T) {
	f := newFixture(t)
	_, err := f.runs.Cancel(context.Background(), "run-does-not-exist")
	if !errors.Is(err, runs.ErrRunNotFound) {
		t.Errorf("err = %v, want ErrRunNotFound", err)
	}
}

// TestRuns_CancelBeforeDispatch_5_8_M5 — a ready run can be cancelled before
// any job is submitted.
func TestRuns_CancelBeforeDispatch_5_8_M5(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out, err := f.runs.Cancel(context.Background(), r.RunID)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !out.Accepted || !out.CancellationComplete {
		t.Errorf("expected Accepted+CancellationComplete=true, got %+v", out)
	}
	if out.Run.State != "cancelled" {
		t.Errorf("state = %q, want cancelled", out.Run.State)
	}
}

// TestRuns_CancelUnsupported_5_8_M5 — an executor that reports
// SupportsCancellation()=false yields ErrCancellationUnsupported.
func TestRuns_CancelUnsupported_5_8_M5(t *testing.T) {
	f := newFixture(t)
	taskID := submitTask(t, f)
	r, err := f.runs.PrepareRun(context.Background(), taskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := f.runs.Dispatch(context.Background(), r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(context.Background(), f.st,
		stations.Spec{StationID: "SCENE-L2", StationName: "SCE_2", ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1"},
	); err != nil {
		t.Fatalf("reseed: %v", err)
	}
	noCancelSvc := runs.NewService(runs.Config{
		Store:           f.st,
		Executor:        nonCancellableExec{f.exec},
		Resolver:        reg,
		WorkingRootBase: t.TempDir(),
	})
	_, err = noCancelSvc.Cancel(context.Background(), r.RunID)
	if !errors.Is(err, runs.ErrCancellationUnsupported) {
		t.Errorf("err = %v, want ErrCancellationUnsupported", err)
	}
}

// nonCancellableExec wraps an Executor with SupportsCancellation()=false.
type nonCancellableExec struct{ inner executor.Executor }

func (e nonCancellableExec) Type() string               { return e.inner.Type() }
func (e nonCancellableExec) SupportsCancellation() bool { return false }
func (e nonCancellableExec) Submit(ctx context.Context, d executor.JobDescription) (string, error) {
	return e.inner.Submit(ctx, d)
}
func (e nonCancellableExec) Poll(ctx context.Context, id string) (executor.Observation, error) {
	return e.inner.Poll(ctx, id)
}
func (e nonCancellableExec) Cancel(ctx context.Context, id string) error {
	return executor.ErrCancellationUnsupported
}

var _ = store.RunListFilter{}
