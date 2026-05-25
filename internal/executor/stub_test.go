package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExecutorStub_DefaultLifecycle_3_9 — submit then poll twice yields
// queued → running → succeeded; further polls are sticky on succeeded.
func TestExecutorStub_DefaultLifecycle_3_9(t *testing.T) {
	e := NewStubExecutor(func() time.Time { return time.Unix(100, 0).UTC() })
	submission, err := e.Submit(context.Background(), JobDescription{RunID: "r1"})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submission.SchedulerID == "" {
		t.Fatal("empty scheduler id")
	}
	wantSeq := []Status{StatusQueued, StatusRunning, StatusSucceeded, StatusSucceeded}
	for i, want := range wantSeq {
		obs, err := e.Poll(context.Background(), submission.SchedulerID)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if obs.Status != want {
			t.Errorf("poll %d: got %s want %s", i, obs.Status, want)
		}
	}
}

// TestExecutorStub_FailureOption — WithFailure makes the job terminate
// failed and surfaces the message + exit code.
func TestExecutorStub_FailureOption(t *testing.T) {
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

// TestExecutorStub_Cancel — cancel marks an unfinished job cancelled.
func TestExecutorStub_Cancel(t *testing.T) {
	e := NewStubExecutor(nil)
	submission, _ := e.Submit(context.Background(), JobDescription{RunID: "r-c"})
	if err := e.Cancel(context.Background(), submission.SchedulerID); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	obs, err := e.Poll(context.Background(), submission.SchedulerID)
	if err != nil {
		t.Fatalf("poll: %v", err)
	}
	if obs.Status != StatusCancelled {
		t.Errorf("status = %s, want cancelled", obs.Status)
	}
}

// TestExecutorStub_UnknownJob — poll/cancel on unknown ids returns ErrUnknownJob.
func TestExecutorStub_UnknownJob(t *testing.T) {
	e := NewStubExecutor(nil)
	if _, err := e.Poll(context.Background(), "no-such"); err != ErrUnknownJob {
		t.Errorf("poll unknown: got %v", err)
	}
	if err := e.Cancel(context.Background(), "no-such"); err != ErrUnknownJob {
		t.Errorf("cancel unknown: got %v", err)
	}
}

func TestLocalExecutor_SplitsStdoutAndStderrLogs(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "run.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nset -eu\necho out-line\necho err-line >&2\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	e := NewLocalExecutor(nil)
	submission, err := e.Submit(context.Background(), JobDescription{
		RunID:       "run-log-split",
		TaskID:      "task-log-split",
		RetryIndex:  0,
		RunRef:      "task-log-split/r0",
		StationID:   "station-log-split",
		WorkingRoot: dir,
		Executable:  script,
		WindowStart: time.Unix(0, 0).UTC(),
		WindowEnd:   time.Unix(1, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	var obs Observation
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		obs, err = e.Poll(context.Background(), submission.SchedulerID)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if obs.Status.IsTerminal() {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if obs.Status != StatusSucceeded {
		t.Fatalf("status = %s, want succeeded: %+v", obs.Status, obs)
	}
	out, err := os.ReadFile(filepath.Join(dir, "logs", "run_out.log"))
	if err != nil {
		t.Fatalf("read stdout log: %v", err)
	}
	errLog, err := os.ReadFile(filepath.Join(dir, "logs", "run_err.log"))
	if err != nil {
		t.Fatalf("read stderr log: %v", err)
	}
	if strings.TrimSpace(string(out)) != "out-line" {
		t.Fatalf("stdout log = %q", out)
	}
	if strings.TrimSpace(string(errLog)) != "err-line" {
		t.Fatalf("stderr log = %q", errLog)
	}
	if _, err := os.Stat(filepath.Join(dir, "logs", "run.log")); !os.IsNotExist(err) {
		t.Fatalf("legacy run.log should not exist, stat err=%v", err)
	}
}
