package runs

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/store"
)

// Dispatcher drives runs through the lifecycle on a fixed cadence:
//
//   - Newly-created runs in "ready" state are dispatched to the executor.
//   - Active runs (dispatched/running) are polled.
//   - Runs in "finalizing" are finalized.
//
// One Dispatcher is intended per process; it serializes work using the
// service's per-row conditional UPDATEs as the concurrency control.
type Dispatcher struct {
	svc      *Service
	interval time.Duration
	logger   zerolog.Logger
}

// NewDispatcher constructs a Dispatcher that ticks at the supplied interval.
// An interval of 0 defaults to 250ms.
func NewDispatcher(svc *Service, interval time.Duration, logger zerolog.Logger) *Dispatcher {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	return &Dispatcher{svc: svc, interval: interval, logger: logger}
}

// Run blocks, ticking the dispatcher until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) error {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := d.Tick(ctx); err != nil {
				d.logger.Warn().Err(err).Msg("dispatcher tick error")
			}
		}
	}
}

// Tick runs one pass of the dispatcher: admit new tasks, dispatch ready runs,
// poll active runs, finalize completed runs. Exposed publicly so tests can
// drive the lifecycle without spinning up a goroutine.
func (d *Dispatcher) Tick(ctx context.Context) error {
	if err := d.admitNewTasks(ctx); err != nil {
		return err
	}
	if err := d.dispatchReady(ctx); err != nil {
		return err
	}
	if err := d.pollActive(ctx); err != nil {
		return err
	}
	return d.finalizeReady(ctx)
}

// admitNewTasks prepares a run for each task in state "accepted" that has no
// latest_run_id yet. This bridges the tasks service (which only persists the
// task row) and the run lifecycle.
func (d *Dispatcher) admitNewTasks(ctx context.Context) error {
	page, err := d.svc.store.Tasks().List(ctx, store.ListFilter{State: "accepted", Limit: 100})
	if err != nil {
		return err
	}
	for _, t := range page.Items {
		if t.LatestRetryIndex.Valid {
			continue
		}
		if _, err := d.svc.PrepareRun(ctx, t.TaskID); err != nil {
			d.logger.Warn().
				Str("task_id", t.TaskID).
				Str("station_id", t.DestinationStationID).
				Time("window_start", t.WindowStart).
				Time("window_end", t.WindowEnd).
				Err(err).
				Msg("prepare run failed")
			switch {
			case errors.Is(err, ErrWaitingInputs):
				if _, ferr := d.svc.store.Tasks().SetStateIfCurrent(ctx, t.TaskID, "accepted", "waiting_inputs", err.Error()); ferr != nil {
					d.logger.Error().Str("task_id", t.TaskID).Err(ferr).Msg("could not mark task waiting for inputs")
				} else {
					d.logger.Info().Str("task_id", t.TaskID).Err(err).Msg("task waiting for mandatory inputs")
				}
			case errors.Is(err, ErrFatalPrepare):
				if ferr := d.svc.store.Tasks().SetState(ctx, t.TaskID, "failed", err.Error()); ferr != nil {
					d.logger.Error().Str("task_id", t.TaskID).Err(ferr).Msg("could not mark task failed after fatal prepare error")
				} else {
					d.logger.Error().Str("task_id", t.TaskID).Err(err).Msg("task failed due to fatal prepare error")
				}
			}
		}
	}
	return nil
}

func (d *Dispatcher) dispatchReady(ctx context.Context) error {
	ready, err := d.svc.store.Runs().ListByStates(ctx, "ready")
	if err != nil {
		return err
	}
	for _, r := range ready {
		paused, err := d.svc.IsStationPausedForRun(ctx, r.StationRevisionID)
		if err != nil {
			d.logger.Warn().Str("run_id", r.RunID).Err(err).Msg("could not resolve station pause state")
			continue
		}
		if paused {
			continue
		}
		if _, err := d.svc.Dispatch(ctx, r.RunID); err != nil {
			d.logger.Warn().Str("run_id", r.RunID).Err(err).Msg("dispatch failed")
			if errors.Is(err, ErrFatalDispatch) {
				reason := err.Error()
				if ferr := d.svc.store.Runs().MarkFailed(ctx, r.RunID, reason, d.svc.clock()); ferr != nil {
					d.logger.Error().Str("run_id", r.RunID).Err(ferr).Msg("could not mark run failed after fatal dispatch error")
				}
				if ferr := d.svc.store.Tasks().SetState(ctx, r.TaskID, "failed", reason); ferr != nil {
					d.logger.Error().Str("run_id", r.RunID).Err(ferr).Msg("could not mark task failed after fatal dispatch error")
				} else {
					d.logger.Error().Str("run_id", r.RunID).Str("task_id", r.TaskID).Err(err).Msg("task failed due to fatal dispatch error")
				}
			}
		}
	}
	return nil
}

func (d *Dispatcher) pollActive(ctx context.Context) error {
	active, err := d.svc.store.Runs().ListByStates(ctx, "dispatched", "running")
	if err != nil {
		return err
	}
	for _, r := range active {
		if _, err := d.svc.Poll(ctx, r.RunID); err != nil &&
			!errors.Is(err, executor.ErrUnknownJob) {
			d.logger.Warn().Str("run_id", r.RunID).Err(err).Msg("poll failed")
		}
	}
	return nil
}

func (d *Dispatcher) finalizeReady(ctx context.Context) error {
	finalizing, err := d.svc.store.Runs().ListByStates(ctx, "finalizing")
	if err != nil {
		return err
	}
	for _, r := range finalizing {
		if _, err := d.svc.Finalize(ctx, r.RunID); err != nil {
			d.logger.Warn().Str("run_id", r.RunID).Err(err).Msg("finalize failed")
		}
	}
	return nil
}
