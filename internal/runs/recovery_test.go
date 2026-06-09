package runs_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/executor"
	"github.com/gobobr/veriproc/internal/runs"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/gobobr/veriproc/internal/tasks"
)

// TestRecovery_RestartReadsPersistedRun_2_15 — after a "restart" (closing
// and reopening the store + service against the same on-disk DB), runs that
// were prepared but not yet dispatched are still visible and the dispatcher
// picks them up. Spec §2.15 / §7.8.
func TestRecovery_RestartReadsPersistedRun_2_15(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover.db")
	workRoot := t.TempDir()

	openStack := func() (*store.Store, *tasks.Service, *runs.Service) {
		st, err := store.Open("sqlite://" + dbPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := store.Migrate(context.Background(), st); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		reg := stations.NewRegistry()
		if err := reg.Seed(context.Background(), st,
			stations.Spec{StationID: "SCENE-L2", StationName: "SCE_2",
				ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1"},
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
		taskN := 0
		runN := 0
		clk := func() time.Time { return time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC) }
		ts := tasks.NewService(st, reg, clk, func() string { taskN++; return fmt.Sprintf("%06x", taskN) })
		exec := executor.NewStubExecutor(clk)
		rs := runs.NewService(runs.Config{
			Store: st, Executors: executor.NewSingleExecutorRegistry(exec), Resolver: reg,
			WorkingRootBase: workRoot, Clock: clk,
			IDFactory: func() string { runN++; return "run-r" + strconv.Itoa(runN) },
		})
		return st, ts, rs
	}

	// 1st boot: submit + prepare a run, then "crash" (close the store).
	st1, ts1, rs1 := openStack()
	res, err := ts1.Submit(context.Background(), tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "SCENE-L2"},
		Window: tasks.Window{
			Start: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC),
			End:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	r, err := rs1.PrepareRun(context.Background(), res.Task.TaskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	preparedRunID := r.RunID
	if r.State != "ready" {
		t.Fatalf("state = %q, want ready", r.State)
	}
	_ = st1.Close()

	// 2nd boot: rebuild the entire stack against the same DB.
	st2, _, rs2 := openStack()
	defer st2.Close()
	got, err := rs2.GetRun(context.Background(), preparedRunID)
	if err != nil {
		t.Fatalf("post-restart GetRun: %v", err)
	}
	if got.State != "ready" {
		t.Errorf("post-restart state = %q, want ready", got.State)
	}
	if got.ProcessingFingerprint == "" {
		t.Error("fingerprint not persisted across restart")
	}
	// Dispatcher (or a manual Dispatch) must still be able to drive the run.
	if _, err := rs2.Dispatch(context.Background(), preparedRunID); err != nil {
		t.Fatalf("dispatch after restart: %v", err)
	}
}

// TestRecovery_CancelStampPersistedAcrossRestart_2_15_5_8 — a cancellation
// request stamped before a crash is observable after restart. Spec §2.15.
func TestRecovery_CancelStampPersistedAcrossRestart_2_15_5_8(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "recover2.db")
	workRoot := t.TempDir()
	build := func() (*store.Store, *tasks.Service, *runs.Service) {
		st, err := store.Open("sqlite://" + dbPath)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if err := store.Migrate(context.Background(), st); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		reg := stations.NewRegistry()
		if err := reg.Seed(context.Background(), st,
			stations.Spec{StationID: "SCENE-L2", StationName: "SCE_2",
				ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1"},
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
		clk := func() time.Time { return time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC) }
		taskN := 0
		runN := 0
		ts := tasks.NewService(st, reg, clk, func() string { taskN++; return fmt.Sprintf("00%04x", taskN) })
		exec := executor.NewStubExecutor(clk)
		rs := runs.NewService(runs.Config{
			Store: st, Executors: executor.NewSingleExecutorRegistry(exec), Resolver: reg,
			WorkingRootBase: workRoot, Clock: clk,
			IDFactory: func() string { runN++; return "rn-" + strconv.Itoa(runN) },
		})
		return st, ts, rs
	}

	st1, ts1, rs1 := build()
	res, err := ts1.Submit(context.Background(), tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "SCENE-L2"},
		Window: tasks.Window{
			Start: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC),
			End:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	r, err := rs1.PrepareRun(context.Background(), res.Task.TaskID)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := rs1.Cancel(context.Background(), r.RunID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	_ = st1.Close()

	st2, _, rs2 := build()
	defer st2.Close()
	got, err := rs2.GetRun(context.Background(), r.RunID)
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if got.State != "cancelled" {
		t.Errorf("state = %q, want cancelled", got.State)
	}
	if !got.CancellationRequestedAt.Valid {
		t.Errorf("cancellation_requested_at not persisted across restart")
	}
}
