package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/eum/veriproc/internal/store"
)

// ErrRetryIneligible is returned when a task cannot be retried because it is
// not in a terminal failed or cancelled state. Spec §3.7.
var ErrRetryIneligible = errors.New("runs: task not eligible for retry")

// RetryOutcome describes the result of a Retry call.
type RetryOutcome struct {
	// Run is the newly created run. Its State will be "ready" and the
	// dispatcher will pick it up on the next dispatchReady pass.
	Run *store.RunRecord
	// RetryIndex is the retry_index assigned to the new run.
	RetryIndex int
}

// Retry creates a new run for a task in a terminal (failed) state and resets
// the task state to "accepted" so the normal dispatch loop picks it up.
//
// Only tasks in state "failed" or "cancelled" are eligible. A new run is
// created via PrepareRun, which assigns the next retry index and allocates a
// fresh working root so retry artifacts cannot overwrite prior-run artifacts.
//
// Spec §3.7: "Retry must create a new run."
func (s *Service) Retry(ctx context.Context, taskID string) (*RetryOutcome, error) {
	task, err := s.store.Tasks().Get(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}

	switch task.State {
	case "failed", "cancelled":
		// eligible
	default:
		return nil, fmt.Errorf("%w: task state is %q (want failed or cancelled)",
			ErrRetryIneligible, task.State)
	}

	run, err := s.PrepareRun(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("prepare retry run: %w", err)
	}

	// Reset task state to "accepted" so the lifecycle reflects an in-progress
	// retry rather than a prior failure.
	if err := s.store.Tasks().SetState(ctx, taskID, "accepted", ""); err != nil {
		return nil, fmt.Errorf("reset task state: %w", err)
	}

	return &RetryOutcome{Run: run, RetryIndex: run.RetryIndex}, nil
}
