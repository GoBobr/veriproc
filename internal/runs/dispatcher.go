package runs

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/executor"
	"github.com/gobobr/veriproc/internal/store"
)

// Dispatcher drives runs through the lifecycle on a fixed cadence:
//
//   - Newly-created runs in "ready" state are dispatched to the executor.
//   - Active runs (dispatched/running) are polled.
//   - Runs in "finalizing" are finalized.
//
// One Dispatcher is intended per process; it serializes work using the
// service's per-row conditional UPDATEs as the concurrency control.
//
// The tick interval is adaptive: when a tick finds no work to do the next
// interval grows exponentially (250ms → 500ms → 1s → ... capped at
// maxInterval, default 5s). Any tick that performs work resets the interval
// to the base value. This keeps dispatch latency at the base interval under
// load while reducing idle CPU consumption to near zero (the dispatcher
// otherwise issues unconditional store queries every tick, which is
// expensive against SQLite on network filesystems).
type Dispatcher struct {
	svc         *Service
	interval    time.Duration
	maxInterval time.Duration
	logger      zerolog.Logger
}

// NewDispatcher constructs a Dispatcher that ticks at the supplied interval.
// An interval of 0 defaults to 250ms. The idle backoff ceiling defaults to
// 5s (20× the base interval).
func NewDispatcher(svc *Service, interval time.Duration, logger zerolog.Logger) *Dispatcher {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	return &Dispatcher{svc: svc, interval: interval, maxInterval: 20 * interval, logger: logger}
}

// Run blocks, ticking the dispatcher until ctx is cancelled. After each idle
// tick the interval doubles (up to maxInterval); after any tick that did work
// it snaps back to the base interval.
func (d *Dispatcher) Run(ctx context.Context) error {
	t := time.NewTicker(d.interval)
	defer t.Stop()
	current := d.interval
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			busy, err := d.Tick(ctx)
			if err != nil {
				d.logger.Warn().Err(err).Msg("dispatcher tick error")
			}
			// Adaptive cadence: back off when idle, snap back when busy.
			if busy {
				current = d.interval
			} else {
				current *= 2
				if current > d.maxInterval {
					current = d.maxInterval
				}
			}
			t.Reset(current)
		}
	}
}

// Tick runs one pass of the dispatcher: admit new tasks, dispatch ready runs,
// poll active runs, finalize completed runs. It returns true if any phase
// found work to do. Exposed publicly so tests can drive the lifecycle
// without spinning up a goroutine.
func (d *Dispatcher) Tick(ctx context.Context) (bool, error) {
	busy := false
	did, err := d.admitNewTasks(ctx)
	if err != nil {
		return busy, err
	}
	busy = busy || did
	did, err = d.dispatchReady(ctx)
	if err != nil {
		return busy, err
	}
	busy = busy || did
	did, err = d.pollActive(ctx)
	if err != nil {
		return busy, err
	}
	busy = busy || did
	did, err = d.finalizeReady(ctx)
	if err != nil {
		return busy, err
	}
	return busy || did, nil
}

// admitNewTasks prepares a run for each task in state "accepted" that has no
// latest_run_id yet. This bridges the tasks service (which only persists the
// task row) and the run lifecycle.
//
// The candidate query filters in SQL on latest_retry_index IS NULL so tasks
// that already have a run are not fetched (with their routing_content JSON
// blobs) and re-scanned on every tick.
func (d *Dispatcher) admitNewTasks(ctx context.Context) (bool, error) {
	page, err := d.svc.store.Tasks().List(ctx, store.ListFilter{State: "accepted", UnpreparedOnly: true, Limit: 100})
	if err != nil {
		return false, err
	}
	if len(page.Items) == 0 {
		return false, nil
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
	return true, nil
}

func (d *Dispatcher) dispatchReady(ctx context.Context) (bool, error) {
	ready, err := d.svc.store.Runs().ListByStates(ctx, "ready")
	if err != nil {
		return false, err
	}
	if len(ready) == 0 {
		return false, nil
	}
	// Resolve pause state for all candidate stations in two batched queries
	// instead of two queries per run.
	revIDs := make([]string, 0, len(ready))
	for _, r := range ready {
		revIDs = append(revIDs, r.StationRevisionID)
	}
	revToStation, err := d.svc.store.Stations().StationIDsForRevisions(ctx, revIDs)
	if err != nil {
		return false, err
	}
	pausedStations, err := d.svc.store.StationControls().ListPaused(ctx)
	if err != nil {
		return false, err
	}
	for _, r := range ready {
		if pausedStations[revToStation[r.StationRevisionID]] {
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
	return true, nil
}

func (d *Dispatcher) pollActive(ctx context.Context) (bool, error) {
	active, err := d.svc.store.Runs().ListByStates(ctx, "dispatched", "running")
	if err != nil {
		return false, err
	}
	if len(active) == 0 {
		return false, nil
	}
	for _, r := range active {
		if _, err := d.svc.Poll(ctx, r.RunID); err != nil &&
			!errors.Is(err, executor.ErrUnknownJob) {
			d.logger.Warn().Str("run_id", r.RunID).Err(err).Msg("poll failed")
		}
	}
	return true, nil
}

func (d *Dispatcher) finalizeReady(ctx context.Context) (bool, error) {
	finalizing, err := d.svc.store.Runs().ListByStates(ctx, "finalizing")
	if err != nil {
		return false, err
	}
	if len(finalizing) == 0 {
		return false, nil
	}
	for _, r := range finalizing {
		if _, err := d.svc.Finalize(ctx, r.RunID); err != nil {
			d.logger.Warn().Str("run_id", r.RunID).Err(err).Msg("finalize failed")
		}
	}
	return true, nil
}
