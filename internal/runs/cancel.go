package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/store"
)

// ErrCancellationUnsupported is returned by Cancel when the executor backing
// the run does not support cancellation. Spec §5.8 maps this to API code
// "cancellation_unsupported".
var ErrCancellationUnsupported = errors.New("runs: executor does not support cancellation")

// CancelOutcome describes the result of a Cancel call.
type CancelOutcome struct {
	Run                  *store.RunRecord
	Accepted             bool   // true if the request introduced cancellation
	AlreadyTerminal      bool   // true if the run was already in a terminal state
	CancellationComplete bool   // true if the run is now cancelled (or otherwise terminal)
	ExecutorErr          error  // non-nil if the executor's Cancel returned a non-fatal error
}

// Cancel implements Spec §5.8 cancellation semantics:
//
//   - If the run is unknown → ErrRunNotFound.
//   - If the run is already in a terminal state (complete/failed/cancelled),
//     return AlreadyTerminal=true with no error: cancellation is idempotent.
//   - If the executor reports SupportsCancellation()=false, return
//     ErrCancellationUnsupported (HTTP 409 cancellation_unsupported).
//   - Otherwise, stamp cancellation_requested_at on the run and the most-recent
//     job, ask the executor to cancel, and if the executor confirms the job is
//     now terminal, transition the run to "cancelled". Repeated calls are
//     idempotent: COALESCE preserves the first stamp.
func (s *Service) Cancel(ctx context.Context, runID string) (*CancelOutcome, error) {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	switch run.State {
	case "complete", "failed", "cancelled":
		return &CancelOutcome{Run: run, AlreadyTerminal: true, CancellationComplete: true}, nil
	}
	if !s.exec.SupportsCancellation() {
		return nil, ErrCancellationUnsupported
	}

	now := s.clock().UTC()

	// Stamp the run first so it is observable even if executor.Cancel is slow
	// or fails. Idempotent via COALESCE in the SQL.
	if err := s.store.Runs().MarkCancellationRequested(ctx, runID, now); err != nil {
		// State changed under us into a terminal state; treat as already-terminal.
		if errors.Is(err, store.ErrInvalidTransition) {
			r2, gerr := s.store.Runs().Get(ctx, runID)
			if gerr != nil {
				return nil, gerr
			}
			return &CancelOutcome{Run: r2, AlreadyTerminal: true, CancellationComplete: true}, nil
		}
		return nil, err
	}

	jobs, err := s.store.Jobs().ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	out := &CancelOutcome{Accepted: true}

	if len(jobs) > 0 {
		job := jobs[len(jobs)-1]
		if err := s.store.Jobs().StampCancellationRequested(ctx, job.JobID, now); err != nil {
			return nil, err
		}
		if job.SchedulerID != "" {
			if cerr := s.exec.Cancel(ctx, job.SchedulerID); cerr != nil {
				if errors.Is(cerr, executor.ErrCancellationUnsupported) {
					return nil, ErrCancellationUnsupported
				}
				if errors.Is(cerr, executor.ErrUnknownJob) {
					// Treat as best-effort; record and continue.
					out.ExecutorErr = cerr
				} else {
					return nil, fmt.Errorf("executor cancel: %w", cerr)
				}
			} else {
				// On stub-executor profile, Cancel is synchronous; observe the
				// resulting state so callers can see the run as cancelled.
				if obs, perr := s.exec.Poll(ctx, job.SchedulerID); perr == nil && obs.Status == executor.StatusCancelled {
					if uerr := s.store.Jobs().UpdateState(ctx, job.JobID, string(obs.Status), now, true); uerr != nil {
						return nil, uerr
					}
					if merr := s.store.Runs().MarkCancelled(ctx, runID, now); merr != nil && !errors.Is(merr, store.ErrInvalidTransition) {
						return nil, merr
					}
					out.CancellationComplete = true
				}
			}
		}
	} else {
		// No job has been submitted yet (run still in pending/preparing/ready).
		// Cancel immediately.
		if err := s.store.Runs().MarkCancelled(ctx, runID, now); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return nil, err
		}
		out.CancellationComplete = true
	}

	r2, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	out.Run = r2
	return out, nil
}
