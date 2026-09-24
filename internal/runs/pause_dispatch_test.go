package runs_test

import (
	"context"
	"testing"
	"time"
)

func TestDispatcher_PausedStationSkipsDispatchUntilUnpaused(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if err := f.st.StationControls().SetPaused(ctx, "SCENE-L2", true, time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)); err != nil {
		t.Fatalf("pause station: %v", err)
	}
	taskID := submitTask(t, f)
	if _, err := f.dispatch.Tick(ctx); err != nil {
		t.Fatalf("tick while paused: %v", err)
	}
	taskRec, err := f.st.Tasks().Get(ctx, taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	runRec, err := f.st.Runs().Get(ctx, taskRec.LatestRunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if runRec.State != "ready" {
		t.Fatalf("run state while paused = %s, want ready", runRec.State)
	}

	if err := f.st.StationControls().SetPaused(ctx, "SCENE-L2", false, time.Date(2025, 7, 3, 11, 51, 0, 0, time.UTC)); err != nil {
		t.Fatalf("unpause station: %v", err)
	}
	if _, err := f.dispatch.Tick(ctx); err != nil {
		t.Fatalf("tick after unpause: %v", err)
	}
	runRec, err = f.st.Runs().Get(ctx, taskRec.LatestRunID)
	if err != nil {
		t.Fatalf("get run after unpause: %v", err)
	}
	if runRec.State == "ready" {
		t.Fatalf("run stayed ready after unpause")
	}
}
