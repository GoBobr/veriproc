// Package runs implements the run lifecycle: it prepares runs for tasks,
// freezes input manifests, computes processing fingerprints, dispatches work
// through an executor, observes job progress, and finalizes runs by recording
// log artifacts and electing canonical runs.
//
// Spec references:
//   - §3.7 Run identity, retry, working root
//   - §3.8 Resolved input manifest (frozen at preparation)
//   - §3.10 Processing fingerprint
//   - §5.6 Run completion gate
//   - §7.5 Lifecycle scenarios
package runs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/eum/veriproc/internal/canonjson"
	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

// Sentinel errors. Package-level so HTTP handlers can map them to API codes.
var (
	ErrRunNotFound          = errors.New("runs: run not found")
	ErrInvalidStateTransition = errors.New("runs: invalid state transition")
	ErrTaskNotFound         = errors.New("runs: task not found")
	ErrUnknownStation       = errors.New("runs: unknown station")
	ErrJobNotFound          = errors.New("runs: job not found")
	// ErrReconciliationInProgress is returned by Cancel/PromoteCanonical when
	// the reconciler currently holds soft ownership of the run (Spec §3.13 /
	// §7.8). The HTTP layer maps this to 409 reconciliation_in_progress.
	ErrReconciliationInProgress = errors.New("runs: reconciliation in progress")
)

// Service drives the run lifecycle. It is safe for concurrent use; per-run
// state transitions are guarded by conditional UPDATE statements at the store
// layer (Spec §5.6 lag tolerance).
type Service struct {
	store           *store.Store
	exec            executor.Executor
	resolver        stations.Resolver
	workingRootBase string
	clock           func() time.Time
	idFactory       func() string
	registerGroup   GroupRegistrar
}

// GroupRegistrar is the optional callback invoked after PrepareRun when a
// task carries a non-empty SplitGroupID (Spec §3.11). The runs package does
// not import the groups package directly to keep the dependency one-way.
type GroupRegistrar func(ctx context.Context, splitGroupID, runID, taskID string) error

// Config configures a Service.
type Config struct {
	Store           *store.Store
	Executor        executor.Executor
	Resolver        stations.Resolver
	WorkingRootBase string
	Clock           func() time.Time
	IDFactory       func() string
	RegisterGroup   GroupRegistrar
}

// NewService constructs a Service. WorkingRootBase defaults to "/var/lib/veriproc/runs"
// when empty; tests typically supply a t.TempDir().
func NewService(cfg Config) *Service {
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.IDFactory == nil {
		cfg.IDFactory = defaultRunID
	}
	if cfg.WorkingRootBase == "" {
		cfg.WorkingRootBase = "/var/lib/veriproc/runs"
	}
	return &Service{
		store:           cfg.Store,
		exec:            cfg.Executor,
		resolver:        cfg.Resolver,
		workingRootBase: cfg.WorkingRootBase,
		clock:           cfg.Clock,
		idFactory:       cfg.IDFactory,
		registerGroup:   cfg.RegisterGroup,
	}
}

func defaultRunID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fall back to v4 to avoid surfacing clock errors; collisions are
		// astronomically unlikely.
		id = uuid.New()
	}
	return "run-" + id.String()
}

// PrepareRun creates a new run for a task, freezes its (stub) manifest,
// computes its processing fingerprint, and transitions it to "ready".
//
// The retry index is assigned by counting existing runs for the task; the
// UNIQUE(task_id, retry_index) constraint serializes any race.
//
// Spec §3.7, §3.8, §3.10, §7.5.2.
func (s *Service) PrepareRun(ctx context.Context, taskID string) (*store.RunRecord, error) {
	task, err := s.store.Tasks().Get(ctx, taskID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	rev, err := s.resolver.Resolve(ctx, task.DestinationStationID, task.DestinationProcType)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnknownStation, err)
	}

	existing, err := s.store.Runs().ListByStates(ctx,
		"pending", "preparing", "ready", "dispatched", "running",
		"finalizing", "complete", "failed", "cancelled")
	if err != nil {
		return nil, err
	}
	retryIndex := 0
	for _, r := range existing {
		if r.TaskID == taskID && r.RetryIndex >= retryIndex {
			retryIndex = r.RetryIndex + 1
		}
	}

	runID := s.idFactory()
	now := s.clock().UTC()
	workingRoot := filepath.Join(s.workingRootBase, rev.StationID, taskID, runID)

	run := &store.RunRecord{
		RunID:             runID,
		TaskID:            taskID,
		StationRevisionID: rev.RevisionID,
		RetryIndex:        retryIndex,
		WorkingRoot:       workingRoot,
		State:             "preparing",
		Canonicality:      "pending",
		CreatedAt:         now,
	}

	manifest := buildStubManifest(runID)
	fingerprintValue := computeFingerprint(rev, manifest, task.Force)

	err = s.store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.Runs().Insert(ctx, run); err != nil {
			return err
		}
		if err := tx.Manifests().Insert(ctx, manifest); err != nil {
			return err
		}
		if _, err := tx.Fingerprints().Upsert(ctx, fingerprintValue); err != nil {
			return err
		}
		if err := tx.Runs().MarkPrepared(ctx, runID, fingerprintValue, now); err != nil {
			return err
		}
		return tx.Tasks().SetLatestRun(ctx, taskID, runID)
	})
	if err != nil {
		return nil, err
	}
	if s.registerGroup != nil && task.SplitGroupID != "" {
		if err := s.registerGroup(ctx, task.SplitGroupID, runID, taskID); err != nil {
			return nil, fmt.Errorf("register split group: %w", err)
		}
	}
	return s.store.Runs().Get(ctx, runID)
}

// Dispatch submits a "ready" run to the executor and records the resulting
// job. Idempotent: if the run is already dispatched (or further), it returns
// the existing run unchanged.
//
// Spec §3.9, §7.5.3.
func (s *Service) Dispatch(ctx context.Context, runID string) (*store.RunRecord, error) {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	switch run.State {
	case "dispatched", "running", "finalizing", "complete", "failed", "cancelled":
		return run, nil
	case "ready":
		// continue
	default:
		return nil, fmt.Errorf("%w: cannot dispatch run in state %q", ErrInvalidStateTransition, run.State)
	}

	desc := executor.JobDescription{
		RunID:        run.RunID,
		WorkingRoot:  run.WorkingRoot,
		JobOrderPath: filepath.Join(run.WorkingRoot, "job-order.yaml"),
		Command:      "run",
	}
	schedID, err := s.exec.Submit(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("executor submit: %w", err)
	}

	now := s.clock().UTC()
	job := &store.JobRecord{
		JobID:             "job-" + sha12(run.RunID+"-"+schedID),
		RunID:             run.RunID,
		Executor:          s.exec.Type(),
		SchedulerID:       schedID,
		SchedulerState:    string(executor.StatusQueued),
		SubmissionAttempt: 1,
		SubmittedAt:       nullTime(now),
		LastObservedAt:    nullTime(now),
	}
	err = s.store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.Jobs().Insert(ctx, job); err != nil {
			return err
		}
		return tx.Runs().MarkDispatched(ctx, run.RunID, now)
	})
	if err != nil {
		return nil, err
	}
	return s.store.Runs().Get(ctx, run.RunID)
}

// Poll observes the active job for a run and advances run state per the
// observation. Returns the (possibly updated) run.
//
// Spec §5.6: completion of a scheduler job alone does NOT mark the run
// complete; Poll only transitions running→finalizing on a SUCCEEDED
// observation. Finalize handles the actual completion gate.
func (s *Service) Poll(ctx context.Context, runID string) (*store.RunRecord, error) {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	jobs, err := s.store.Jobs().ListByRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return run, nil
	}
	job := jobs[len(jobs)-1]
	if job.SchedulerID == "" {
		return run, nil
	}
	obs, err := s.exec.Poll(ctx, job.SchedulerID)
	if err != nil {
		return nil, fmt.Errorf("executor poll: %w", err)
	}
	now := s.clock().UTC()
	if err := s.store.Jobs().UpdateState(ctx, job.JobID, string(obs.Status), now, obs.Status.IsTerminal()); err != nil {
		return nil, err
	}

	switch obs.Status {
	case executor.StatusRunning:
		if run.State == "dispatched" {
			if err := s.store.Runs().MarkRunning(ctx, runID, now); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
				return nil, err
			}
		}
	case executor.StatusSucceeded:
		// Move to finalizing; Finalize() will gate completion on artifacts.
		if err := s.store.Runs().MarkReadyForFinalization(ctx, runID); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return nil, err
		}
	case executor.StatusFailed:
		reason := obs.FailureMsg
		if reason == "" {
			reason = "executor reported failure"
		}
		if err := s.store.Runs().MarkFailed(ctx, runID, reason, now); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return nil, err
		}
	case executor.StatusCancelled:
		if err := s.store.Runs().MarkFailed(ctx, runID, "cancelled", now); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return nil, err
		}
	}
	return s.store.Runs().Get(ctx, runID)
}

// GetRun returns a run by id.
func (s *Service) GetRun(ctx context.Context, runID string) (*store.RunRecord, error) {
	r, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return r, nil
}

// ListRuns paginates runs.
func (s *Service) ListRuns(ctx context.Context, f store.RunListFilter) (*store.RunListPage, error) {
	return s.store.Runs().List(ctx, f)
}

// ListJobsForRun returns all jobs of a run.
func (s *Service) ListJobsForRun(ctx context.Context, runID string) ([]*store.JobRecord, error) {
	return s.store.Jobs().ListByRun(ctx, runID)
}

// GetJob returns a job by id.
func (s *Service) GetJob(ctx context.Context, jobID string) (*store.JobRecord, error) {
	j, err := s.store.Jobs().Get(ctx, jobID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrJobNotFound
		}
		return nil, err
	}
	return j, nil
}

// ListArtifacts returns artifacts produced by a run.
func (s *Service) ListArtifacts(ctx context.Context, runID, logicalType string) ([]*store.ArtifactRecord, error) {
	return s.store.Artifacts().ListByRun(ctx, runID, logicalType)
}

// GetArtifact returns a single artifact.
func (s *Service) GetArtifact(ctx context.Context, artifactID string) (*store.ArtifactRecord, error) {
	a, err := s.store.Artifacts().Get(ctx, artifactID)
	if err != nil {
		return nil, mapStoreErr(err)
	}
	return a, nil
}

// ListPublicationsByArtifact returns all rolling-archive publications for an
// artifact (Spec §5.5.6). Returns nil on missing artifact rather than an
// error so the caller can simply omit the publication summary.
func (s *Service) ListPublicationsByArtifact(ctx context.Context, artifactID string) ([]*store.PublicationRecord, error) {
	return s.store.Publications().ListByArtifact(ctx, artifactID)
}

// ----- helpers --------------------------------------------------------------

func mapStoreErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return ErrRunNotFound
	}
	if errors.Is(err, store.ErrInvalidTransition) {
		return ErrInvalidStateTransition
	}
	return err
}

// buildStubManifest produces a single-entry manifest used by the stub-executor
// profile. Real executors will populate this from station declarations.
func buildStubManifest(runID string) *store.ManifestRecord {
	return &store.ManifestRecord{
		ManifestID: "mf-" + sha12(runID),
		RunID:      runID,
		Entries: []store.ManifestEntry{
			{
				EntryID:  "ent-" + sha12(runID+":primary"),
				FileType: "PRIMARY_INPUT",
				Category: "input",
				Optional: false,
				Present:  true,
			},
		},
	}
}

// computeFingerprint hashes the canonical JSON of {station_revision_id,
// manifest summary, force}. Spec §3.10 requires the fingerprint to be a
// deterministic function of the inputs that govern processing equivalence.
func computeFingerprint(rev *store.StationRevisionRecord, m *store.ManifestRecord, force bool) string {
	entries := make([]any, 0, len(m.Entries))
	for _, e := range m.Entries {
		entries = append(entries, map[string]any{
			"file_type": e.FileType,
			"category":  e.Category,
			"checksum":  e.Checksum,
		})
	}
	payload := map[string]any{
		"station_revision_id":          rev.RevisionID,
		"station_revision_content_hash": rev.ContentHash,
		"manifest":                     entries,
		"force":                        force,
	}
	raw, err := canonjson.Marshal(payload)
	if err != nil {
		// canonjson on simple maps cannot fail; fall back to a sentinel hash.
		raw = []byte(fmt.Sprintf(`{"err":%q}`, err.Error()))
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func sha12(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func nullTime(t time.Time) sql.NullTime {
	if t.IsZero() {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: t.UTC(), Valid: true}
}
