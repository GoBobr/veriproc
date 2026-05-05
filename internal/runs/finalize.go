package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eum/veriproc/internal/store"
)

// Finalize completes a run that has finished executing. It performs the
// "completion gate" required by Spec §5.6:
//
//  1. Write a stub log artifact to disk and persist its metadata.
//  2. Decide canonicality: the first run to claim the fingerprint becomes
//     "canonical"; subsequent runs sharing that fingerprint become "duplicate".
//     (M3/M4 simplification — see deviation D9 in the M0–M4 report.)
//  3. Transition run "finalizing" → "complete".
//  4. Update the parent task's latest_run_id and (when canonical) canonical_run_id.
//
// Finalize is idempotent: a second invocation on a complete run is a no-op.
//
// Spec §3.10 (canonicality), §5.6 (completion gate), §7.5.4 (finalization).
func (s *Service) Finalize(ctx context.Context, runID string) (*store.RunRecord, error) {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	switch run.State {
	case "complete":
		return run, nil
	case "finalizing":
		// continue
	default:
		return nil, fmt.Errorf("%w: cannot finalize run in state %q", ErrInvalidStateTransition, run.State)
	}

	logArtifact, err := s.writeStubLog(run)
	if err != nil {
		return nil, fmt.Errorf("write log: %w", err)
	}

	// Spec §2.14.1 / §3.15: forced reruns must not silently overwrite the
	// previous canonical record. They are persisted as "forced" runs and do
	// not compete for the canonical claim.
	task, err := s.store.Tasks().Get(ctx, run.TaskID)
	if err != nil {
		return nil, err
	}
	canonicality := "canonical"
	switch {
	case task.Force:
		canonicality = "forced"
	case run.ProcessingFingerprint != "":
		fp, err := s.store.Fingerprints().GetByValue(ctx, run.ProcessingFingerprint)
		if err != nil {
			return nil, err
		}
		won, err := s.store.Fingerprints().ClaimCanonical(ctx, fp.FingerprintID, run.RunID)
		if err != nil {
			return nil, err
		}
		if !won {
			canonicality = "duplicate"
		} else {
			// Record the initial canonical election in the audit log.
			_ = s.store.Canonicality().RecordElection(ctx,
				fp.FingerprintID, run.RunID, "auto:first-finalize", "system", s.clock().UTC())
		}
	}

	now := s.clock().UTC()
	err = s.store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.Artifacts().Insert(ctx, logArtifact); err != nil {
			return err
		}
		if err := tx.Runs().MarkComplete(ctx, runID, canonicality, now); err != nil {
			return err
		}
		if err := tx.Tasks().SetLatestRun(ctx, run.TaskID, run.RunID); err != nil {
			return err
		}
		if canonicality == "canonical" {
			if err := tx.Tasks().SetCanonicalRun(ctx, run.TaskID, run.RunID, now); err != nil {
				return err
			}
			if err := tx.Tasks().SetState(ctx, run.TaskID, "completed", ""); err != nil {
				return err
			}
		} else if canonicality == "forced" {
			// Forced reruns mark the task completed but do not overwrite
			// canonical_run_id (Spec §3.15).
			if err := tx.Tasks().SetState(ctx, run.TaskID, "completed", ""); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.store.Runs().Get(ctx, runID)
}

// writeStubLog writes a deterministic log file to {workingRoot}/logs/run.log
// and returns the artifact metadata to persist.
func (s *Service) writeStubLog(run *store.RunRecord) (*store.ArtifactRecord, error) {
	dir := filepath.Join(run.WorkingRoot, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "run.log")
	body := fmt.Sprintf("run=%s task=%s state=finalizing executor=%s\n",
		run.RunID, run.TaskID, s.exec.Type())
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(body))
	return &store.ArtifactRecord{
		ArtifactID:       "art-" + sha12(run.RunID+":log"),
		ProducingRunID:   run.RunID,
		LogicalType:      "log",
		FileType:         "RUN_LOG",
		Path:             path,
		Size:             int64(len(body)),
		Checksum:         hex.EncodeToString(sum[:]),
		ChecksumAlgo:     "sha256",
		ValidationStatus: "validated",
		Availability:     "available",
		CreatedAt:        s.clock().UTC(),
	}, nil
}

// MarkFailedAndLog handles the failure path: writes a failure-log artifact
// (best-effort) and ensures the run is marked failed. Used by tests and the
// dispatcher when Poll returns a terminal failure.
func (s *Service) MarkFailedAndLog(ctx context.Context, runID, reason string) error {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return mapStoreErr(err)
	}
	if run.State == "failed" {
		return nil
	}
	now := s.clock().UTC()
	if err := s.store.Runs().MarkFailed(ctx, runID, reason, now); err != nil &&
		!errors.Is(err, store.ErrInvalidTransition) {
		return err
	}
	// Best-effort log artifact for the failure path.
	if logArt, werr := s.writeStubLog(run); werr == nil {
		logArt.ValidationStatus = "failed"
		_ = s.store.Artifacts().Insert(ctx, logArt)
	}
	return s.store.Tasks().SetState(ctx, run.TaskID, "failed", reason)
}
