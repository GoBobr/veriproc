// Package executor defines the abstraction that the run lifecycle uses to
// submit work to a scheduler and observe its progress.
//
// VeriProc's lifecycle is intentionally executor-agnostic: SLURM, Kubernetes,
// or local-process executors all implement the same Executor interface. The
// only implementation in this milestone is StubExecutor, an in-memory
// deterministic backend used for tests and local development. The SLURM
// adapter is tracked as deferred work (M0–M4 report, deviation D10).
//
// Spec references:
//   - §3.9 Job lifecycle
//   - §5.6 Run completion gate ("a run must not be reported as complete solely
//     because a scheduler job succeeded")
package executor

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Status enumerates the executor-facing job states. These map onto Spec §3.9
// scheduler-native states; callers translate them into run-level state via
// the runs service.
type Status string

const (
	StatusUnknown   Status = "unknown"
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

// IsTerminal reports whether the status is a terminal state.
func (s Status) IsTerminal() bool {
	switch s {
	case StatusSucceeded, StatusFailed, StatusCancelled:
		return true
	}
	return false
}

// JobDescription is what the lifecycle hands to the executor at submission
// time. The lifecycle has already prepared the working root and frozen the
// input manifest; the executor's job is purely to dispatch.
type JobDescription struct {
	// RunID is the internal surrogate, retained for executor bookkeeping
	// (scheduler IDs, log paths) but NOT exposed to the workload via env.
	RunID       string
	TaskID      string
	RetryIndex  int
	RunRef      string
	StationID   string
	WorkingRoot string
	// JobOrderPath is the absolute path of the written joborder file.
	// Empty when joborder.format is "none" — VERIPROC_JOBORDER_PATH is not
	// injected in that case.
	JobOrderPath string
	// Executable is the resolved absolute path of the binary or script to run.
	Executable string
	// Args are the resolved argument strings to pass to the executable.
	Args        []string
	WindowStart time.Time
	WindowEnd   time.Time
	// Mode is the station-declared execution mode. Empty means use the executor
	// default selected by instance configuration.
	Mode      string
	Resources ResourceRequest
	Slurm     SlurmOverrides
	Container ContainerConfig
	// StationConfigDir is the directory that contains the station definition
	// and its auxiliary scripts/templates. Exposed to the workload as
	// VERIPROC_STATION_DIR so scripts can reference co-located files.
	StationConfigDir string
	// Environment contains operator-defined environment variables from instance
	// configuration. Names must not use the reserved VERIPROC_ prefix.
	Environment map[string]string
	// SplitGroupID is the split-group this run belongs to, if any. Exposed
	// to the workload as VERIPROC_SPLIT_GROUP_ID (empty string → not set).
	SplitGroupID string
}

// ResourceRequest captures scheduler resource requirements for one run.
type ResourceRequest struct {
	CPUsPerTask int
	MemGB       int
	Walltime    string
}

// SlurmOverrides captures station-level scheduler option overrides.
type SlurmOverrides struct {
	Partition string
	Account   string
	QOS       string
	ExtraArgs []string
}

// ContainerConfig captures runtime container settings for containerized executors.
type ContainerConfig struct {
	Image  string
	Mounts []string
	User   string
}

// Submission is the result of a scheduler submission.
type Submission struct {
	SchedulerID  string
	ExecutorType string
}

// Observation is the result of polling.
type Observation struct {
	Status      Status
	ObservedAt  time.Time
	ExitCode    int
	FailureMsg  string
	NativeState string // free-form executor-native state string for diagnostics
	Node        string // scheduler-allocated node(s), if available (e.g. sacct NodeList)
}

// Executor is the abstraction used by the runs service. Implementations must
// be safe for concurrent use.
type Executor interface {
	// Type returns a stable type tag (e.g. "stub", "slurm").
	Type() string

	// SupportsCancellation indicates whether Cancel may succeed for jobs of
	// this executor. Used by the runs service to surface
	// CodeCancellationUnsupported (Spec §5.8).
	SupportsCancellation() bool

	// Submit dispatches a job and returns the scheduler-assigned identifier and
	// the effective executor type used for the submitted job.
	Submit(ctx context.Context, desc JobDescription) (Submission, error)

	// Poll returns the current status of a previously-submitted job.
	Poll(ctx context.Context, schedulerID string) (Observation, error)

	// Cancel asks the executor to stop a job. Implementations must be
	// idempotent; cancelling an already-terminal job must not error.
	Cancel(ctx context.Context, schedulerID string) error
}

// ErrUnknownJob is returned by Poll/Cancel when the scheduler-id is not known.
var ErrUnknownJob = errors.New("executor: unknown scheduler id")

// ErrFatalSubmit is returned by Submit when the failure is permanent and
// retrying the same run will never succeed without a configuration change.
// Examples: SSH transport failure (exit 255), missing executable, unknown
// executor mode. The dispatcher wraps this in ErrFatalDispatch and
// transitions both the run and the task to "failed".
var ErrFatalSubmit = errors.New("executor: fatal submit error")

// ErrCancellationUnsupported is returned by Cancel on executors whose
// SupportsCancellation reports false. Stub returns nil here (it supports
// cancellation); a SLURM-without-scancel deployment would return this.
var ErrCancellationUnsupported = errors.New("executor: cancellation unsupported")

// StubExecutor is a deterministic in-memory executor used for tests and the
// stub-executor conformance profile.
//
// Default lifecycle for a submitted job, observed through successive Poll()
// calls: queued → running → succeeded. Use the With* options on submit to
// override per-job behavior.
type StubExecutor struct {
	mu      sync.Mutex
	clock   func() time.Time
	counter int
	jobs    map[string]*stubJob
}

type stubJob struct {
	id          string
	pollCount   int
	transitions []Status // remaining states; popped from front on each poll
	terminal    Status   // sticky once reached
	failMsg     string
	exitCode    int
	cancelled   bool
}

// NewStubExecutor returns a StubExecutor.
func NewStubExecutor(clock func() time.Time) *StubExecutor {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &StubExecutor{
		clock: clock,
		jobs:  map[string]*stubJob{},
	}
}

// Type returns the stable type tag "stub".
func (e *StubExecutor) Type() string { return "stub" }

// SupportsCancellation reports true for the stub executor.
func (e *StubExecutor) SupportsCancellation() bool { return true }

// SubmitOption customizes the per-job behavior of the stub.
type SubmitOption func(*stubJob)

// WithTransitions overrides the default queued→running→succeeded sequence.
func WithTransitions(states ...Status) SubmitOption {
	return func(j *stubJob) {
		j.transitions = append(j.transitions[:0], states...)
	}
}

// WithFailure makes the job terminate with StatusFailed and the supplied
// message after the natural transition sequence.
func WithFailure(msg string, exitCode int) SubmitOption {
	return func(j *stubJob) {
		j.transitions = []Status{StatusQueued, StatusRunning, StatusFailed}
		j.failMsg = msg
		j.exitCode = exitCode
	}
}

// SubmitWith is the option-aware submit entry-point used by tests.
func (e *StubExecutor) SubmitWith(ctx context.Context, desc JobDescription, opts ...SubmitOption) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.counter++
	id := "stub-" + desc.RunID
	if _, ok := e.jobs[id]; ok {
		// uniqueness fallback if same run id submits twice
		id = id + "-retry"
	}
	j := &stubJob{
		id:          id,
		transitions: []Status{StatusQueued, StatusRunning, StatusSucceeded},
	}
	for _, opt := range opts {
		opt(j)
	}
	e.jobs[id] = j
	return id, nil
}

// Submit dispatches a job using the default transition sequence.
func (e *StubExecutor) Submit(ctx context.Context, desc JobDescription) (Submission, error) {
	id, err := e.SubmitWith(ctx, desc)
	if err != nil {
		return Submission{}, err
	}
	return Submission{SchedulerID: id, ExecutorType: e.Type()}, nil
}

// SetOutcome rewrites the planned transitions for an already-submitted job;
// it is intended for tests that need to fail a specific run mid-flight.
func (e *StubExecutor) SetOutcome(schedulerID string, states ...Status) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[schedulerID]
	if !ok {
		return ErrUnknownJob
	}
	j.transitions = append(j.transitions[:0], states...)
	j.terminal = ""
	return nil
}

// Poll returns the next planned status. Once the transition list is exhausted,
// the last status is sticky.
func (e *StubExecutor) Poll(ctx context.Context, schedulerID string) (Observation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[schedulerID]
	if !ok {
		return Observation{}, ErrUnknownJob
	}
	if j.cancelled {
		return Observation{Status: StatusCancelled, ObservedAt: e.clock(), NativeState: "CANCELLED"}, nil
	}
	if j.terminal != "" {
		return Observation{
			Status:      j.terminal,
			ObservedAt:  e.clock(),
			ExitCode:    j.exitCode,
			FailureMsg:  j.failMsg,
			NativeState: string(j.terminal),
		}, nil
	}
	var next Status
	if len(j.transitions) == 0 {
		next = StatusSucceeded
	} else {
		next = j.transitions[0]
		j.transitions = j.transitions[1:]
	}
	j.pollCount++
	if next.IsTerminal() {
		j.terminal = next
	}
	return Observation{
		Status:      next,
		ObservedAt:  e.clock(),
		ExitCode:    j.exitCode,
		FailureMsg:  j.failMsg,
		NativeState: string(next),
	}, nil
}

// Cancel marks a job as cancelled. It is a no-op for already-terminal jobs.
func (e *StubExecutor) Cancel(ctx context.Context, schedulerID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	j, ok := e.jobs[schedulerID]
	if !ok {
		return ErrUnknownJob
	}
	if j.terminal != "" {
		return nil
	}
	j.cancelled = true
	j.terminal = StatusCancelled
	return nil
}
