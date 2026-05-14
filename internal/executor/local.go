package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// LocalExecutor runs station scripts as local OS processes. It is intended for
// sandbox and developer-workstation use; production deployments use a remote
// scheduler adapter (SLURM, Kubernetes, etc.).
//
// Each Submit call launches the script in a goroutine. Poll returns the
// current status without blocking. The executor is safe for concurrent use.
type LocalExecutor struct {
	mu    sync.Mutex
	clock func() time.Time
	jobs  map[string]*localJob
}

type localJob struct {
	id       string
	mu       sync.Mutex
	status   Status
	exitCode int
	failMsg  string
}

// NewLocalExecutor returns a LocalExecutor.
func NewLocalExecutor(clock func() time.Time) *LocalExecutor {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &LocalExecutor{
		clock: clock,
		jobs:  map[string]*localJob{},
	}
}

// Type returns the stable type tag "local".
func (e *LocalExecutor) Type() string { return "local" }

// SupportsCancellation reports false; OS-process cancellation is not yet
// wired (the goroutine runs to completion regardless).
func (e *LocalExecutor) SupportsCancellation() bool { return false }

// Submit launches the script described by desc.Executable and returns
// immediately. The script is executed with the runtime contract from
// Spec §3.6.2 / §6.5:
//
//	VERIPROC_RUN_DIR        = {WorkingRoot}/output
//	VERIPROC_TASK_ID        = desc.TaskID
//	VERIPROC_RETRY_INDEX    = desc.RetryIndex (decimal)
//	VERIPROC_RUN_REF        = desc.RunRef ("<task_id>/r<retry_index>")
//	VERIPROC_STATION_ID     = desc.StationID
//	VERIPROC_WORKING_ROOT   = desc.WorkingRoot
//	VERIPROC_JOBORDER_PATH  = desc.JobOrderPath  (only when non-empty)
//	VERIPROC_WINDOW_START   = desc.WindowStart  (YYYYMMDDTHHmmSS, UTC)
//	VERIPROC_WINDOW_END     = desc.WindowEnd    (YYYYMMDDTHHmmSS, UTC)
//
// VERIPROC_RUN_ID is intentionally NOT exposed: the surrogate run_id is
// internal-only per Spec §3.6.1.
func (e *LocalExecutor) Submit(_ context.Context, desc JobDescription) (string, error) {
	if desc.Executable == "" {
		return "", fmt.Errorf("local executor: Executable is empty for run %s (station has no executable?)", desc.RunID)
	}

	id := "local-" + desc.RunID
	job := &localJob{id: id, status: StatusQueued}

	e.mu.Lock()
	e.jobs[id] = job
	e.mu.Unlock()

	if err := os.MkdirAll(filepath.Join(desc.WorkingRoot, "logs"), 0o755); err != nil {
		return "", fmt.Errorf("local executor: mkdir logs: %w", err)
	}
	outDir := filepath.Join(desc.WorkingRoot, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("local executor: mkdir output: %w", err)
	}

	go func() {
		job.mu.Lock()
		job.status = StatusRunning
		job.mu.Unlock()

		var out bytes.Buffer
		cmd := exec.Command(desc.Executable, desc.Args...) // #nosec G204 – operator-supplied station executable
		cmd.Dir = desc.WorkingRoot
		baseEnv := append(os.Environ(),
			"VERIPROC_RUN_DIR="+outDir,
			"VERIPROC_TASK_ID="+desc.TaskID,
			"VERIPROC_RETRY_INDEX="+fmt.Sprintf("%d", desc.RetryIndex),
			"VERIPROC_RUN_REF="+desc.RunRef,
			"VERIPROC_STATION_ID="+desc.StationID,
			"VERIPROC_WORKING_ROOT="+desc.WorkingRoot,
			"VERIPROC_WINDOW_START="+desc.WindowStart.UTC().Format("20060102T150405"),
			"VERIPROC_WINDOW_END="+desc.WindowEnd.UTC().Format("20060102T150405"),
		)
		if desc.JobOrderPath != "" {
			baseEnv = append(baseEnv, "VERIPROC_JOBORDER_PATH="+desc.JobOrderPath)
		}
		cmd.Env = baseEnv
		cmd.Stdout = &out
		cmd.Stderr = &out

		err := cmd.Run()
		logPath := filepath.Join(desc.WorkingRoot, "logs", "run.log")
		if writeErr := os.WriteFile(logPath, out.Bytes(), 0o644); writeErr != nil && err == nil {
			err = writeErr
		}

		job.mu.Lock()
		defer job.mu.Unlock()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				job.exitCode = exitErr.ExitCode()
			}
			if out.Len() > 0 {
				job.failMsg = err.Error() + ": " + out.String()
			} else {
				job.failMsg = err.Error()
			}
			job.status = StatusFailed
		} else {
			job.status = StatusSucceeded
		}
	}()

	return id, nil
}

// Poll returns the current status of a previously-submitted job.
func (e *LocalExecutor) Poll(_ context.Context, schedulerID string) (Observation, error) {
	e.mu.Lock()
	job, ok := e.jobs[schedulerID]
	e.mu.Unlock()
	if !ok {
		return Observation{}, ErrUnknownJob
	}

	job.mu.Lock()
	defer job.mu.Unlock()
	return Observation{
		Status:      job.status,
		ObservedAt:  e.clock(),
		ExitCode:    job.exitCode,
		FailureMsg:  job.failMsg,
		NativeState: string(job.status),
	}, nil
}

// Cancel is a no-op for the local executor (in-flight goroutines run to
// completion).
func (e *LocalExecutor) Cancel(_ context.Context, schedulerID string) error {
	e.mu.Lock()
	_, ok := e.jobs[schedulerID]
	e.mu.Unlock()
	if !ok {
		return ErrUnknownJob
	}
	return ErrCancellationUnsupported
}
