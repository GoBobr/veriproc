package runs

import (
	"context"
	"errors"
	"fmt"

	"github.com/eum/veriproc/internal/store"
)

// ErrPromoteIneligible is returned when a run cannot be promoted to canonical
// (e.g. it is not in a terminal state, has no fingerprint, or is already
// canonical). Spec §7.4.5.
var ErrPromoteIneligible = errors.New("runs: run not eligible for canonical promotion")

// PromoteOutcome describes the result of a PromoteCanonical call.
type PromoteOutcome struct {
	Run             *store.RunRecord
	PreviousRunID   string // empty if no previous canonical
	FingerprintID   string
	AlreadyCanonical bool
}

// PromoteCanonical promotes runID to canonical for its processing fingerprint.
// The previous canonical run (if any) becomes "duplicate". An audit row is
// recorded in canonicality_audit. Spec §3.16 / §7.4.5.
func (s *Service) PromoteCanonical(ctx context.Context, runID, reason, actor string) (*PromoteOutcome, error) {
	if reason == "" {
		return nil, fmt.Errorf("%w: reason required", ErrPromoteIneligible)
	}
	if actor == "" {
		return nil, fmt.Errorf("%w: actor required", ErrPromoteIneligible)
	}
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	if run.State != "complete" {
		return nil, fmt.Errorf("%w: run state is %q (must be complete)", ErrPromoteIneligible, run.State)
	}
	if run.ProcessingFingerprint == "" {
		return nil, fmt.Errorf("%w: run has no processing fingerprint", ErrPromoteIneligible)
	}
	fp, err := s.store.Fingerprints().GetByValue(ctx, run.ProcessingFingerprint)
	if err != nil {
		return nil, err
	}
	now := s.clock().UTC()

	out := &PromoteOutcome{Run: run, FingerprintID: fp.FingerprintID}
	err = s.store.InTx(ctx, func(tx *store.Tx) error {
		prev, err := tx.Fingerprints().SetCanonical(ctx, fp.FingerprintID, run.RunID)
		if err != nil {
			return err
		}
		out.PreviousRunID = prev
		if prev == run.RunID {
			out.AlreadyCanonical = true
			return nil
		}
		// Demote previous canonical run to "duplicate".
		if prev != "" {
			if err := tx.Runs().SetCanonicality(ctx, prev, "duplicate"); err != nil {
				return err
			}
		}
		// Promote new run to "canonical".
		if err := tx.Runs().SetCanonicality(ctx, run.RunID, "canonical"); err != nil {
			return err
		}
		// Update tasks.canonical_run_id.
		if err := tx.Tasks().SetCanonicalRun(ctx, run.TaskID, run.RunID, now); err != nil {
			return err
		}
		// Record audit.
		action := "promote"
		if prev == "" {
			action = "elect"
		}
		return tx.Canonicality().RecordPromotion(ctx, fp.FingerprintID, prev, run.RunID, action, reason, actor, now)
	})
	if err != nil {
		return nil, err
	}
	out.Run, _ = s.store.Runs().Get(ctx, run.RunID)
	return out, nil
}
