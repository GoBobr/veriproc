package executor

import (
	"context"
	"testing"
	"time"
)

// TestExecutorStub_DefaultLifecycle_3_9_M3 — submit then poll twice yields
// queued → running → succeeded; further polls are sticky on succeeded.
func TestExecutorStub_DefaultLifecycle_3_9_M3(t *testing.T) {
	e := NewStubExecutor(func() time.Time { return time.Unix(100, 0).UTC() })
	id, err := e.Submit(context.Background(), JobDescription{RunID: "r1"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if id == "" {
		t.Fatal("empty scheduler id")
	}
	wantSeq := []Status{StatusQueued, StatusRunning, StatusSucceeded, StatusSucceeded}
	for i, want := range wantSeq {
		obs, err := e.Poll(context.Background(), id)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if obs.Status != want {
			t.Errorf("poll %d: got %s want %s", i, obs.Status, want)
		}
	}
}

// TestExecutorStub_FailureOption_M3 — WithFailure makes the job terminate
// failed and surfaces the message + exit code.
func TestExecutorStub_FailureOption_M3(t *testing.T) {
	e := NewStubExecutor(nil)
	id, err := e.SubmitWith(context.Background(), JobDescription{RunID: "r-fail"},
		WithFailure("boom", 7))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var last Observation
	for i := 0; i < 3; i++ {
		last, err = e.Poll(context.Background(), id)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
	}
	if last.Status != StatusFailed || last.ExitCode != 7 || last.FailureMsg != "boom" {
		t.Errorf("final = %+v", last)
	}
}

// TestExecutorStub_Cancel_M3 — cancel marks an unfinished job cancelled.
func TestExecutorStub_Cancel_M3(t *testing.T) {
	e := NewStubExecutor(nil)
	id, _ := e.Submit(context.Background(), JobDescription{RunID: "r-c"})
	if err := e.Cancel(context.Background(), id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	obs, err := e.Poll(context.Background(), id)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if obs.Status != StatusCancelled {
		t.Errorf("status = %s, want cancelled", obs.Status)
	}
}

// TestExecutorStub_UnknownJob_M3 — poll/cancel on unknown ids returns ErrUnknownJob.
func TestExecutorStub_UnknownJob_M3(t *testing.T) {
	e := NewStubExecutor(nil)
	if _, err := e.Poll(context.Background(), "no-such"); err != ErrUnknownJob {
		t.Errorf("poll unknown: got %v", err)
	}
	if err := e.Cancel(context.Background(), "no-such"); err != ErrUnknownJob {
		t.Errorf("cancel unknown: got %v", err)
	}
}
