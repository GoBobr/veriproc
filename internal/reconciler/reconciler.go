// Package reconciler implements the executor / store reconciliation worker
// described in Spec §3.13 and §7.8. Its job is to periodically scan for runs
// whose latest observation is too old (the dispatcher may have crashed
// mid-poll, or the executor may have been unreachable) and to reconcile the
// store's view with the executor's authoritative state.
//
// While reconciliation is in progress, ReconciliationStartedAt is stamped on
// the run, which causes Cancel and PromoteCanonical to short-circuit with
// ErrReconciliationInProgress (Spec §3.13). The marker is cleared after the
// reconciler converges (or the run reaches a terminal state during the pass).
//
// The implementation is intentionally minimal but exercises the full path:
// stamp marker → poll executor → forward observation through runs.Service
// (which handles state transitions / finalization) → clear marker.
package reconciler

import (
	"context"
	"errors"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/runs"
	"github.com/eum/veriproc/internal/store"
)

// Service is the reconciliation worker.
type Service struct {
	store          *store.Store
	runs           *runs.Service
	clock          func() time.Time
	staleThreshold time.Duration
	interval       time.Duration
	log            zerolog.Logger
}

// Config configures a Service.
type Config struct {
	Store          *store.Store
	Runs           *runs.Service
	Clock          func() time.Time
	StaleThreshold time.Duration // how old last_observed_at must be to qualify
	Interval       time.Duration // poll interval for Run()
	Logger         zerolog.Logger
}

// New constructs a Service. Defaults: 30s threshold, 10s interval.
func New(cfg Config) *Service {
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.StaleThreshold <= 0 {
		cfg.StaleThreshold = 30 * time.Second
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 10 * time.Second
	}
	return &Service{
		store:          cfg.Store,
		runs:           cfg.Runs,
		clock:          cfg.Clock,
		staleThreshold: cfg.StaleThreshold,
		interval:       cfg.Interval,
		log:            cfg.Logger,
	}
}

// Tick performs one reconciliation pass and returns the number of runs the
// pass attempted to reconcile.
func (s *Service) Tick(ctx context.Context) (int, error) {
	cutoff := s.clock().Add(-s.staleThreshold).UTC()
	stale, err := s.findStale(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	for _, runID := range stale {
		if err := s.reconcileOne(ctx, runID); err != nil {
			s.log.Warn().Err(err).Str("run_id", runID).Msg("reconciler: reconcile failed")
		}
	}
	return len(stale), nil
}

// Run drives Tick on the configured interval until ctx is cancelled.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := s.Tick(ctx); err != nil {
				s.log.Warn().Err(err).Msg("reconciler: tick failed")
			}
		}
	}
}

// findStale returns run ids in dispatched/running whose latest job
// observation is older than cutoff (or never observed at all). The query
// joins runs to the most recent job per run.
func (s *Service) findStale(ctx context.Context, cutoff time.Time) ([]string, error) {
	const q = `
		SELECT r.run_id
		FROM runs r
		LEFT JOIN (
			SELECT run_id, MAX(COALESCE(last_observed_at, submitted_at)) AS observed_at
			FROM jobs GROUP BY run_id
		) j ON j.run_id = r.run_id
		WHERE r.state IN ('dispatched','running')
		  AND r.reconciliation_started_at IS NULL
		  AND (j.observed_at IS NULL OR j.observed_at < ?)
		ORDER BY r.created_at ASC
		LIMIT 50`
	rows, err := s.store.DB().QueryContext(ctx, q, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// reconcileOne stamps the marker, drives a Poll through the runs.Service, and
// clears the marker. Errors during Poll do not prevent the marker from being
// cleared so the run can be picked up again on the next pass.
func (s *Service) reconcileOne(ctx context.Context, runID string) error {
	now := s.clock().UTC()
	if err := s.store.Runs().MarkReconciliationStarted(ctx, runID, now); err != nil {
		return err
	}
	defer func() {
		if err := s.store.Runs().ClearReconciliation(ctx, runID); err != nil {
			s.log.Warn().Err(err).Str("run_id", runID).Msg("reconciler: clear marker failed")
		}
	}()
	if _, err := s.runs.Poll(ctx, runID); err != nil {
		// Run may already be terminal — that's fine.
		if errors.Is(err, runs.ErrJobNotFound) || errors.Is(err, runs.ErrRunNotFound) {
			return nil
		}
		return err
	}
	return nil
}
