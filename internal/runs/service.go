// Package runs implements the run lifecycle: it prepares runs for tasks,
// freezes input manifests, computes processing fingerprints, dispatches work
// through an executor, observes job progress, and finalizes runs by recording
// log artifacts and electing canonical runs.
//
// Spec references:
//   - §3.7 Run identity, retry, working root
//   - §3.8 Resolved input manifest (frozen at preparation)
//   - §3.15 Processing fingerprint
//   - §5.6 Run completion gate
//   - §7.5 Lifecycle scenarios
package runs

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/canonjson"
	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/policy"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
)

// Sentinel errors. Package-level so HTTP handlers can map them to API codes.
var (
	ErrRunNotFound            = errors.New("runs: run not found")
	ErrInvalidStateTransition = errors.New("runs: invalid state transition")
	ErrTaskNotFound           = errors.New("runs: task not found")
	ErrUnknownStation         = errors.New("runs: unknown station")
	ErrJobNotFound            = errors.New("runs: job not found")
	// ErrReconciliationInProgress is returned by Cancel/PromoteCanonical when
	// the reconciler currently holds soft ownership of the run (Spec §3.13 /
	// §7.8). The HTTP layer maps this to 409 reconciliation_in_progress.
	ErrReconciliationInProgress = errors.New("runs: reconciliation in progress")
	// ErrFatalPrepare is returned by PrepareRun when the failure is permanent
	// and the task must not be retried automatically. The dispatcher uses this
	// to transition the task to "failed" instead of looping indefinitely.
	// Examples: mandatory input not found, working root cannot be created.
	ErrFatalPrepare = errors.New("runs: fatal preparation error")
)

// Service drives the run lifecycle. It is safe for concurrent use; per-run
// state transitions are guarded by conditional UPDATE statements at the store
// layer (Spec §5.6 lag tolerance).
type Service struct {
	store             *store.Store
	exec              executor.Executor
	resolver          stations.Resolver
	workingRootBase   string
	clock             func() time.Time
	idFactory         func() string
	registerGroup        GroupRegistrar
	notifyGroupComplete  GroupCompleteNotifier
	instanceID           string
	definitions       map[string]any
	facility          map[string]string
	rollingArchives   map[string]string
	productCategories map[string][]string
	generators        map[string]string
	jobOrderPaths     string // "relative" or "absolute"
	naming            policy.Naming
	integrity         policy.Integrity
	logger            zerolog.Logger
}

// GroupRegistrar is the optional callback invoked after PrepareRun when a
// task carries a non-empty SplitGroupID (Spec §3.11). The runs package does
// not import the groups package directly to keep the dependency one-way.
type GroupRegistrar func(ctx context.Context, splitGroupID, runID, taskID string) error

// GroupCompleteNotifier is the optional callback invoked after a run is
// finalised as canonical and belongs to a split group. The caller is
// responsible for aggregating the group and triggering any downstream action
// (e.g. auto-submitting the fan-in station). Called asynchronously in a
// goroutine so it does not block finalization.
type GroupCompleteNotifier func(ctx context.Context, splitGroupID string)

// Config configures a Service.
type Config struct {
	Store             *store.Store
	Executor          executor.Executor
	Resolver          stations.Resolver
	WorkingRootBase   string
	Clock             func() time.Time
	IDFactory         func() string
	RegisterGroup        GroupRegistrar
	NotifyGroupComplete  GroupCompleteNotifier
	InstanceID           string
	Definitions       map[string]any
	Facility          map[string]string
	RollingArchives   map[string]string
	ProductCategories map[string][]string
	Generators        map[string]string
	JobOrderPaths     string // "relative" (default) or "absolute"
	Naming            policy.Naming
	Integrity         policy.Integrity
	Logger            zerolog.Logger
}

// NewService constructs a Service. WorkingRootBase defaults to
// $XDG_CACHE_HOME/veriproc/runs (via os.UserCacheDir) when empty;
// tests typically supply a t.TempDir().
func NewService(cfg Config) *Service {
	if cfg.Clock == nil {
		cfg.Clock = func() time.Time { return time.Now().UTC() }
	}
	if cfg.IDFactory == nil {
		cfg.IDFactory = defaultRunID
	}
	if cfg.WorkingRootBase == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			base = os.TempDir()
		}
		cfg.WorkingRootBase = filepath.Join(base, "veriproc", "runs")
	}
	if abs, err := filepath.Abs(cfg.WorkingRootBase); err == nil {
		cfg.WorkingRootBase = abs
	}
	cfg.RollingArchives = absPathMap(cfg.RollingArchives)
	cfg.Naming = cfg.Naming.WithDefaults()
	cfg.Integrity = cfg.Integrity.WithDefaults()
	return &Service{
		store:             cfg.Store,
		exec:              cfg.Executor,
		resolver:          cfg.Resolver,
		workingRootBase:   cfg.WorkingRootBase,
		clock:             cfg.Clock,
		idFactory:         cfg.IDFactory,
		registerGroup:       cfg.RegisterGroup,
		notifyGroupComplete: cfg.NotifyGroupComplete,
		instanceID:          cfg.InstanceID,
		definitions:       cloneAnyMap(cfg.Definitions),
		facility:          cloneStringMap(cfg.Facility),
		rollingArchives:   cloneStringMap(cfg.RollingArchives),
		productCategories: cloneStringSliceMap(cfg.ProductCategories),
		generators:        cloneStringMap(cfg.Generators),
		jobOrderPaths:     cfg.JobOrderPaths,
		naming:            cfg.Naming,
		integrity:         cfg.Integrity,
		logger:            cfg.Logger,
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
	rev, err := s.resolver.Resolve(ctx, task.DestinationStationID)
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
	workingRoot := s.workingRootPath(rev, task, runID, retryIndex, now)

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

	if err := materializeWorkingRoot(workingRoot); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrFatalPrepare, err)
	}
	manifest, err := s.resolveManifest(ctx, runID, task, rev, workingRoot)
	if err != nil {
		_ = os.RemoveAll(workingRoot)
		return nil, fmt.Errorf("%w: %w", ErrFatalPrepare, err)
	}
	fingerprintValue := computeFingerprint(rev, manifest, task.Force, task.WindowStart, task.WindowEnd)

	// Spec §3.15: if the fingerprint is already canonically owned and this
	// run is not forced, fail before creating any DB records or dispatching.
	// Forced runs bypass this check so operators can always trigger a fresh
	// execution; if execution actually happens the run will go downstream
	// regardless of canonicality (§3.8, §3.12).
	if !task.Force {
		if fp, gerr := s.store.Fingerprints().GetByValue(ctx, fingerprintValue); gerr == nil && fp.CanonicalRunID != "" {
			_ = os.RemoveAll(workingRoot)
			return nil, fmt.Errorf("%w: duplicate fingerprint — already canonically processed as run %s", ErrFatalPrepare, fp.CanonicalRunID)
		}
	}

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
		return tx.Tasks().SetLatestRun(ctx, taskID, retryIndex)
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

	jobOrderPath, jobOrderArtifact, err := s.writeJobOrder(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("write job-order: %w", err)
	}

	taskHistoryArtifact, err := s.writeTaskHistory(ctx, run)
	if err != nil {
		return nil, fmt.Errorf("write task history: %w", err)
	}

	// Resolve the station execution from the station revision's declared execution.
	var executable string
	var resolvedArgs []string
	rev, rerr := s.store.Stations().Get(ctx, run.StationRevisionID)
	if rerr != nil {
		return nil, rerr
	}

	task, err := s.store.Tasks().Get(ctx, run.TaskID)
	if err != nil {
		return nil, fmt.Errorf("dispatch: fetch task: %w", err)
	}

	if rev.DeclaredExecution != "" {
		var execCfg stations.Execution
		if jerr := json.Unmarshal([]byte(rev.DeclaredExecution), &execCfg); jerr == nil {
			executable = execCfg.Executable
			// Build runtime context and resolve args.
			runCtx := s.buildRunContext(run, task, rev, jobOrderPath)
			resolvedArgs, err = stations.ResolveArgs(execCfg.Args, runCtx)
			if err != nil {
				return nil, fmt.Errorf("dispatch: resolve execution args: %w", err)
			}
		}
	}

	runRef := fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex)
	desc := executor.JobDescription{
		RunID:        run.RunID,
		WorkingRoot:  run.WorkingRoot,
		JobOrderPath: jobOrderPath,
		StationID:    rev.StationID,
		TaskID:       run.TaskID,
		RetryIndex:   run.RetryIndex,
		RunRef:       runRef,
		Executable:   executable,
		Args:         resolvedArgs,
		WindowStart:  task.WindowStart,
		WindowEnd:    task.WindowEnd,
		SplitGroupID: task.SplitGroupID,
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
		if jobOrderArtifact != nil {
			if err := tx.Artifacts().Insert(ctx, jobOrderArtifact); err != nil {
				return err
			}
		}
		if taskHistoryArtifact != nil {
			if err := tx.Artifacts().Insert(ctx, taskHistoryArtifact); err != nil {
				return err
			}
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
		_ = s.store.Tasks().SetState(ctx, run.TaskID, "failed", reason)
		if logArts, werr := s.collectRunLogs(run); werr == nil {
			for _, logArt := range logArts {
				logArt.ValidationStatus = "failed"
				_ = s.store.Artifacts().Insert(ctx, logArt)
			}
		}
	case executor.StatusCancelled:
		if err := s.store.Runs().MarkFailed(ctx, runID, "cancelled", now); err != nil && !errors.Is(err, store.ErrInvalidTransition) {
			return nil, err
		}
		_ = s.store.Tasks().SetState(ctx, run.TaskID, "failed", "cancelled")
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

func (s *Service) GetTask(ctx context.Context, taskID string) (*store.TaskRecord, error) {
	t, err := s.store.Tasks().Get(ctx, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrTaskNotFound
	}
	return t, err
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

func materializeWorkingRoot(root string) error {
	for _, rel := range []string{"input", "output", "logs", "temp", "manifest"} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) resolveManifest(ctx context.Context, runID string, task *store.TaskRecord, rev *store.StationRevisionRecord, workingRoot string) (*store.ManifestRecord, error) {
	manifest := &store.ManifestRecord{ManifestID: "mf-" + sha12(runID), RunID: runID}
	if rev.DeclaredInputs == "" {
		return manifest, nil
	}
	var inputs []stations.InputDefinition
	if err := json.Unmarshal([]byte(rev.DeclaredInputs), &inputs); err != nil {
		return nil, fmt.Errorf("parse declared inputs: %w", err)
	}
	var rollingFolders map[string][]string
	if rev.RollingFolders != "" {
		if err := json.Unmarshal([]byte(rev.RollingFolders), &rollingFolders); err != nil {
			return nil, fmt.Errorf("parse rolling folders: %w", err)
		}
	}
	seqCounter := 0
	for inputIdx, input := range inputs {
		effectivePattern := s.effectiveInputFilenamePattern(input)
		windowMatch := defaultWindowMatch(effectivePattern, s.naming, input)
		folders := s.productCategories[input.Category]
		if override := rollingFolders[input.Category]; len(override) > 0 {
			folders = override
		}
		winners, missingReason, err := s.classicalSelectCandidates(runID, input, folders, task)
		if err != nil {
			return nil, err
		}
		if len(winners) == 0 {
			entry := store.ManifestEntry{
				EntryID:                  fmt.Sprintf("ent-%s-%02d-miss", sha12(runID+":"+input.FileType), inputIdx),
				ObjectSequence:           seqCounter,
				FileType:                 input.FileType,
				Category:                 input.Category,
				Optional:                 input.Optional,
				Present:                  false,
				EffectiveFilenamePattern: effectivePattern,
				WindowMatch:              windowMatch,
				SelectionReason:          missingReason,
			}
			seqCounter++
			manifest.Entries = append(manifest.Entries, entry)
			if !input.Optional {
				return nil, fmt.Errorf("mandatory input %s not found in category %s: %s", input.FileType, input.Category, missingReason)
			}
			s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
				Str("input_key", input.FileType).Bool("optional", input.Optional).
				Str("reason", missingReason).Msg("matcher: missing optional input")
			continue
		}
		for winnerIdx, winner := range winners {
			entry := store.ManifestEntry{
				EntryID:                  fmt.Sprintf("ent-%s-%02d-%02d", sha12(runID+":"+input.FileType), inputIdx, winnerIdx),
				ObjectSequence:           seqCounter,
				FileType:                 input.FileType,
				Category:                 input.Category,
				Optional:                 input.Optional,
				EffectiveFilenamePattern: winner.EffectivePattern,
				FilenameComponents:       winner.FilenameComponents,
				WindowMatch:              winner.WindowMatch,
				ObjectKind:               winner.ObjectKind,
				SourceArchiveID:          winner.ArchiveID,
				SourcePrecedence:         winner.FolderPriority + 1,
				SelectionReason:          winner.Reason,
				IntervalGroupKey:         winner.IntervalGroupKey,
				FolderPriority:           winner.FolderPriority,
				Discriminator:            winner.Discriminator,
				WinnerMetadata:           winner.WinnerMetadata,
			}
			seqCounter++
			info, err := os.Stat(winner.Path)
			if err != nil {
				return nil, err
			}
			linkName := safeInputLinkName(seqCounter-1, input.FileType, filepath.Base(winner.Path))
			linkPath := filepath.Join(workingRoot, "input", linkName)
			_ = os.Remove(linkPath)
			if err := os.Symlink(winner.Path, linkPath); err != nil {
				return nil, fmt.Errorf("symlink input %s: %w", input.FileType, err)
			}
			entry.Path = filepath.ToSlash(filepath.Join("input", linkName))
			entry.Present = true
			if winner.ObjectKind == store.ObjectKindRegularFile {
				entry.Size = info.Size()
			}
			entry.MTime = nullTime(info.ModTime().UTC())
			if winner.ObjectKind == store.ObjectKindRegularFile {
				entry.Checksum, entry.ChecksumAlgo, entry.ChecksumSource = availableChecksum(winner.Path, s.integrity)
			}
			entry.VersionMetadata = versionMetadata(winner.Path)
			manifest.Entries = append(manifest.Entries, entry)
		}
	}
	_ = ctx
	return manifest, nil
}

// selectedInputCandidate is one winner produced by the classical matcher.
type selectedInputCandidate struct {
	Path               string
	ObjectKind         string
	ArchiveID          string
	FolderPriority     int
	Reason             string
	EffectivePattern   string
	FilenameComponents string
	WindowMatch        string
	IntervalGroupKey   string
	Discriminator      string
	WinnerMetadata     string
}

// classicalCandidate is an internal struct holding a parsed candidate during
// classical matching before winner selection.
type classicalCandidate struct {
	Path             string
	ObjectKind       string
	ArchiveID        string
	FolderPriority   int // 0-indexed; 0 = first (highest-priority) configured folder
	EffectivePattern string
	WindowMatch      string
	ParsedComponents map[string]string
	GenTimeStr       string
	GenTime          time.Time
	IsSentinel       bool   // true when GenTimeStr cannot be parsed as a real timestamp
	Discriminator    string // captured suffix from post-generation wildcard
	IntervalGroupKey string // file_type|start_time|end_time, or just file_type when unstructured
}

// classicalSelectCandidates implements the classical input matching algorithm.
// It scans all configured folders, groups valid candidates by logical interval,
// and selects one winner per group using the classical comparison order:
//  1. Newest real parsed generation time (sentinels rank last).
//  2. Lexicographically greatest discriminator/suffix when generation time ties.
//  3. Lowest folder-priority index (first configured folder) when discriminator ties.
//  4. Lexicographically smallest full source path as a stable fallback.
//
// It returns one selectedInputCandidate per logical interval group. The second
// return value is the reason string for a missing-input entry when the slice is empty.
func (s *Service) classicalSelectCandidates(
	runID string,
	input stations.InputDefinition,
	folders []string,
	task *store.TaskRecord,
) ([]selectedInputCandidate, string, error) {
	effectivePattern := s.effectiveInputFilenamePattern(input)
	windowMatch := defaultWindowMatch(effectivePattern, s.naming, input)
	patternHasFileType := s.inputPatternHasFileType(input)

	s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
		Str("input_key", input.FileType).
		Str("file_type", input.FileType).
		Str("category", input.Category).
		Str("object_kind", input.ObjectKind).
		Bool("optional", input.Optional).
		Str("effective_pattern", effectivePattern).
		Str("window_match", windowMatch).
		Msg("matcher: resolving input")

	if len(folders) == 0 {
		s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
			Str("input_key", input.FileType).Msg("matcher: no configured folders")
		return nil, "no configured folders", nil
	}

	// Collect all valid candidates from all folders.
	var allCandidates []classicalCandidate
	for priority, folderRef := range folders {
		folder, archiveID, err := s.resolveFolderRef(folderRef)
		if err != nil {
			return nil, "", err
		}
		s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
			Str("input_key", input.FileType).
			Int("folder_priority", priority).
			Str("folder_ref", folderRef).
			Str("folder", folder).
			Str("archive_id", archiveID).
			Msg("matcher: scanning folder")
		rawCandidates, err := s.inputCandidatesInFolder(folder, input, effectivePattern)
		if err != nil {
			return nil, "", err
		}
		for _, raw := range rawCandidates {
			candidate := classicalCandidate{
				Path:             raw.Path,
				ObjectKind:       raw.ObjectKind,
				ArchiveID:        archiveID,
				FolderPriority:   priority,
				EffectivePattern: effectivePattern,
				WindowMatch:      windowMatch,
			}
			if effectivePattern != "" {
				components, err := policy.ParseFilename(filepath.Base(raw.Path), effectivePattern, s.naming.Filenames.Components)
				if err != nil {
					s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
						Str("input_key", input.FileType).
						Str("path", raw.Path).
						Str("parse_error", err.Error()).
						Msg("matcher: candidate parse failure")
					continue
				}
				if patternHasFileType {
					components["file_type"] = input.FileType
				}
				if !candidateMatchesWindow(components, windowMatch, input, task) {
					s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
						Str("input_key", input.FileType).
						Str("path", raw.Path).
						Str("window_policy", windowMatch).
						Msg("matcher: candidate rejected by window policy")
					continue
				}
				genTimeStr := components["generation_time"]
				genTime, gtErr := policy.ParseFilenameTime(genTimeStr)
				candidate.GenTimeStr = genTimeStr
				candidate.GenTime = genTime
				candidate.IsSentinel = gtErr != nil
				candidate.Discriminator = components["suffix"]
				candidate.ParsedComponents = components
				candidate.IntervalGroupKey = input.FileType + "|" + components["start_time"] + "|" + components["end_time"]
				s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
					Str("input_key", input.FileType).
					Str("path", raw.Path).
					Str("interval_group_key", candidate.IntervalGroupKey).
					Str("generation_time", genTimeStr).
					Bool("is_sentinel", candidate.IsSentinel).
					Str("discriminator", candidate.Discriminator).
					Int("folder_priority", priority).
					Msg("matcher: candidate accepted")
			} else {
				// No structured pattern: single group per declared input.
				candidate.IntervalGroupKey = input.FileType
			}
			allCandidates = append(allCandidates, candidate)
		}
	}

	if len(allCandidates) == 0 {
		s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
			Str("input_key", input.FileType).Msg("matcher: no matching candidates")
		return nil, "no matching candidate", nil
	}

	// Group by interval key preserving first-seen order.
	groupOrder := []string{}
	groups := map[string][]classicalCandidate{}
	for _, c := range allCandidates {
		if _, ok := groups[c.IntervalGroupKey]; !ok {
			groupOrder = append(groupOrder, c.IntervalGroupKey)
		}
		groups[c.IntervalGroupKey] = append(groups[c.IntervalGroupKey], c)
	}

	winners := make([]selectedInputCandidate, 0, len(groups))
	for _, groupKey := range groupOrder {
		groupCandidates := groups[groupKey]
		// Sort best-first: winner is groupCandidates[0].
		sort.Slice(groupCandidates, func(i, j int) bool {
			ci, cj := groupCandidates[i], groupCandidates[j]
			// Non-sentinel beats sentinel.
			if ci.IsSentinel != cj.IsSentinel {
				return !ci.IsSentinel
			}
			// Both real: newest generation time first.
			if !ci.IsSentinel && !cj.IsSentinel && !ci.GenTime.Equal(cj.GenTime) {
				return ci.GenTime.After(cj.GenTime)
			}
			// Both sentinels: greatest gen-time string first (lexicographic).
			if ci.IsSentinel && cj.IsSentinel && ci.GenTimeStr != cj.GenTimeStr {
				return ci.GenTimeStr > cj.GenTimeStr
			}
			// Greatest discriminator first.
			if ci.Discriminator != cj.Discriminator {
				return ci.Discriminator > cj.Discriminator
			}
			// Lowest folder-priority index first (highest user priority).
			if ci.FolderPriority != cj.FolderPriority {
				return ci.FolderPriority < cj.FolderPriority
			}
			// Stable fallback: smallest path first.
			return ci.Path < cj.Path
		})
		winner := groupCandidates[0]

		winnerMeta := map[string]any{
			"generation_time": winner.GenTimeStr,
			"is_sentinel":     winner.IsSentinel,
			"discriminator":   winner.Discriminator,
			"folder_priority": winner.FolderPriority,
			"source_path":     winner.Path,
			"candidate_count": len(groupCandidates),
		}
		winnerMetaJSON, _ := canonjson.Marshal(winnerMeta)

		var filenameComponentsJSON string
		if winner.ParsedComponents != nil {
			b, err := canonjson.Marshal(winner.ParsedComponents)
			if err != nil {
				return nil, "", err
			}
			filenameComponentsJSON = string(b)
		}

		s.logger.Debug().Str("run_id", runID).Str("component", "matcher").
			Str("input_key", input.FileType).
			Str("interval_group_key", groupKey).
			Str("winner_path", winner.Path).
			Str("generation_time", winner.GenTimeStr).
			Bool("is_sentinel", winner.IsSentinel).
			Str("discriminator", winner.Discriminator).
			Int("folder_priority", winner.FolderPriority).
			Int("candidates_in_group", len(groupCandidates)).
			Msg("matcher: selected winner")

		winners = append(winners, selectedInputCandidate{
			Path:               winner.Path,
			ObjectKind:         winner.ObjectKind,
			ArchiveID:          winner.ArchiveID,
			FolderPriority:     winner.FolderPriority,
			EffectivePattern:   winner.EffectivePattern,
			WindowMatch:        winner.WindowMatch,
			FilenameComponents: filenameComponentsJSON,
			IntervalGroupKey:   winner.IntervalGroupKey,
			Discriminator:      winner.Discriminator,
			WinnerMetadata:     string(winnerMetaJSON),
			Reason:             "classical-matcher: selected by generation_time, discriminator, folder_priority, path",
		})
	}
	return winners, "", nil
}

func (s *Service) effectiveInputFilenamePattern(input stations.InputDefinition) string {
	return policy.EffectiveFilenamePattern(s.naming.Filenames.FilenamePattern, input.FilenamePattern, input.FileType)
}

func (s *Service) inputPatternHasFileType(input stations.InputDefinition) bool {
	pattern := strings.TrimSpace(s.naming.Filenames.FilenamePattern)
	if strings.TrimSpace(input.FilenamePattern) != "" {
		pattern = strings.TrimSpace(input.FilenamePattern)
	}
	return strings.Contains(pattern, "<FILE_TYPE>")
}

type inputCandidate struct {
	Path       string
	ObjectKind string
}

func (s *Service) inputCandidatesInFolder(folder string, input stations.InputDefinition, effectivePattern string) ([]inputCandidate, error) {
	if effectivePattern == "" {
		pattern := input.Pattern
		if pattern == "" {
			pattern = "*" + input.FileType + "*"
		}
		matches, err := filepath.Glob(filepath.Join(folder, pattern))
		if err != nil {
			return nil, err
		}
		candidates := make([]inputCandidate, 0, len(matches))
		for _, match := range matches {
			if strings.HasSuffix(match, ".sha256") {
				continue
			}
			if info, err := os.Stat(match); err == nil && filenameMatchesInput(filepath.Base(match), input) {
				kind := objectKindFromInfo(info)
				if input.ObjectKind != "" && input.ObjectKind != kind {
					continue
				}
				candidates = append(candidates, inputCandidate{Path: match, ObjectKind: kind})
			}
		}
		return candidates, nil
	}
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, err
	}
	candidates := make([]inputCandidate, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, ".sha256") {
			continue
		}
		path := filepath.Join(folder, name)
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		kind := objectKindFromInfo(info)
		if input.ObjectKind != "" && input.ObjectKind != kind {
			continue
		}
		candidates = append(candidates, inputCandidate{Path: path, ObjectKind: kind})
	}
	return candidates, nil
}

func (s *Service) workingRootPath(rev *store.StationRevisionRecord, task *store.TaskRecord, runID string, retryIndex int, created time.Time) string {
	// taskValues uses the task's own CreatedAt so that all retries of the same
	// task share the same task-level segment. runValues uses the run's
	// creation time for the run-level segment.
	taskValues := map[string]string{
		"station_id":   rev.StationID,
		"task_id":      task.TaskID,
		"start":        policy.CompactTaskWindow(task.WindowStart),
		"end":          policy.CompactTaskWindow(task.WindowEnd),
		"created":      policy.CompactRuntimeEvent(task.CreatedAt),
		"short_run_id": policy.ShortRunID(runID),
	}
	runValues := map[string]string{
		"station_id":   rev.StationID,
		"task_id":      task.TaskID,
		"retry_index":  fmt.Sprintf("%d", retryIndex),
		"start":        policy.CompactTaskWindow(task.WindowStart),
		"end":          policy.CompactTaskWindow(task.WindowEnd),
		"created":      policy.CompactRuntimeEvent(created),
		"short_run_id": policy.ShortRunID(runID),
	}
	station := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.StationSegment, taskValues, s.naming)
	taskSegment := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.TaskSegment, taskValues, s.naming)
	runSegment := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.RunSegment, runValues, s.naming)

	// Expand path template with the normalized segment values.
	tmpl := s.naming.WorkingRoot.PathTemplate
	relative := policy.ExpandWorkingRootTemplate(tmpl, station, taskSegment, runSegment)
	relative = filepath.Clean(relative)

	// Containment safety: if the cleaned relative path escapes the base
	// (e.g. due to a misconfigured template), fall back to a safe nested layout.
	if relative == ".." || strings.HasPrefix(relative, "../") || filepath.IsAbs(relative) {
		relative = filepath.Join(station, taskSegment, runSegment)
	}

	full := filepath.Join(s.workingRootBase, relative)
	if _, err := os.Stat(full); errors.Is(err, os.ErrNotExist) {
		return full
	}
	// Collision disambiguation: append suffix to the final expanded path
	// component only, preserving all readable tokens in their original order.
	suffix := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.CollisionSuffix, runValues, s.naming)
	dir := filepath.Dir(full)
	base := filepath.Base(full)
	return filepath.Join(dir, base+suffix)
}

func (s *Service) resolveFolderRef(ref string) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "rolling:") {
		archiveID := strings.TrimPrefix(ref, "rolling:")
		path := s.rollingArchives[archiveID]
		if path == "" {
			return "", "", fmt.Errorf("unknown rolling archive %q", archiveID)
		}
		return path, archiveID, nil
	}
	if abs, err := filepath.Abs(ref); err == nil {
		return abs, "", nil
	}
	return ref, "", nil
}

func safeInputLinkName(_ int, _ string, base string) string {
	return base
}

func filenameMatchesInput(name string, input stations.InputDefinition) bool {
	if input.Pattern != "" {
		return true
	}
	return strings.Contains(name, input.FileType)
}

func defaultWindowMatch(effectivePattern string, naming policy.Naming, input stations.InputDefinition) string {
	if match := canonicalWindowMatch(input.WindowMatch); match != "" {
		return match
	}
	if effectivePattern != "" && strings.Contains(effectivePattern, "<START_TIME>") && strings.Contains(effectivePattern, "<END_TIME>") {
		return "overlaps"
	}
	_ = naming
	return ""
}

func canonicalWindowMatch(match string) string {
	switch strings.ToLower(strings.TrimSpace(match)) {
	case "", "none":
		return ""
	case "overlaps", "cross":
		return "overlaps"
	case "within_window", "fully_within":
		return "within_window"
	case "covers_window", "surrender":
		return "covers_window"
	default:
		return strings.ToLower(strings.TrimSpace(match))
	}
}

func candidateMatchesWindow(components map[string]string, match string, input stations.InputDefinition, task *store.TaskRecord) bool {
	match = canonicalWindowMatch(match)
	if match == "" {
		return true
	}
	if task == nil {
		return false
	}
	candidateStart, err := policy.ParseFilenameTime(components["start_time"])
	if err != nil {
		return false
	}
	candidateEnd, err := policy.ParseFilenameTime(components["end_time"])
	if err != nil || candidateEnd.Before(candidateStart) {
		return false
	}
	windowStart, windowEnd := effectiveInputWindow(task.WindowStart, task.WindowEnd, input.Margins)
	switch match {
	case "overlaps":
		return !candidateEnd.Before(windowStart) && !candidateStart.After(windowEnd)
	case "within_window":
		return !candidateStart.Before(windowStart) && !candidateEnd.After(windowEnd)
	case "covers_window":
		return !candidateStart.After(windowStart) && !candidateEnd.Before(windowEnd)
	default:
		return false
	}
}

func effectiveInputWindow(start, end time.Time, margins []int) (time.Time, time.Time) {
	before, after := 0, 0
	if len(margins) == 1 {
		before, after = margins[0], margins[0]
	} else if len(margins) >= 2 {
		before, after = margins[0], margins[1]
	}
	return start.UTC().Add(-time.Duration(before) * time.Second), end.UTC().Add(time.Duration(after) * time.Second)
}

func availableChecksum(path string, integrity policy.Integrity) (string, string, string) {
	if integrity.ChecksumPolicy == policy.ChecksumNone {
		return "", "", ""
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", "", ""
	}
	for _, sidecar := range []string{path + ".sha256", strings.TrimSuffix(path, filepath.Ext(path)) + ".sha256"} {
		body, err := os.ReadFile(sidecar)
		if err != nil {
			continue
		}
		fields := strings.Fields(string(body))
		if len(fields) > 0 && len(fields[0]) == 64 {
			return fields[0], "sha256", "sidecar"
		}
	}
	return "", "", ""
}

func objectKindFromInfo(info os.FileInfo) string {
	if info != nil && info.IsDir() {
		return store.ObjectKindDirectory
	}
	return store.ObjectKindRegularFile
}

func versionMetadata(path string) string {
	base := filepath.Base(path)
	parts := strings.Split(base, "_")
	for _, part := range parts {
		if strings.HasPrefix(strings.ToLower(part), "v") && len(part) > 1 {
			return part
		}
	}
	return ""
}

// buildRunContext assembles the station context namespace for a given run.
// It merges instance definitions (lowest priority) with reserved runtime keys
// (highest priority). The resulting map is used by the context resolver to
// expand <name> and <name.path> references in execution args and joborder include.
func (s *Service) buildRunContext(run *store.RunRecord, task *store.TaskRecord, rev *store.StationRevisionRecord, jobOrderPath string) map[string]any {
	ctx := make(map[string]any, len(s.definitions)+12)
	for k, v := range s.definitions {
		ctx[k] = v
	}
	// Reserved runtime keys always override definitions.
	runRef := fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex)
	ctx["station_id"] = rev.StationID
	ctx["station_name"] = rev.StationName
	ctx["task_id"] = run.TaskID
	ctx["retry_index"] = run.RetryIndex
	ctx["run_ref"] = runRef
	ctx["start"] = task.WindowStart.UTC().Format("20060102T150405")
	ctx["end"] = task.WindowEnd.UTC().Format("20060102T150405")
	ctx["working_root"] = run.WorkingRoot
	if jobOrderPath != "" {
		ctx["joborder"] = map[string]any{
			"path": jobOrderPath,
		}
	}
	return ctx
}

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func absPathMap(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		if abs, err := filepath.Abs(v); err == nil {
			out[k] = abs
		} else {
			out[k] = v
		}
	}
	return out
}

func cloneStringSliceMap(in map[string][]string) map[string][]string {
	out := map[string][]string{}
	for k, v := range in {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// computeFingerprint hashes the canonical JSON of {station_revision_id,
// manifest summary, task window, force, resolved_execution_args, declared_joborder}.
// Spec §3.15 requires the fingerprint to be a deterministic function of the
// inputs that govern processing equivalence. The task window is included
// because the same input file processed for different time windows represents
// distinct computations (e.g. fan-out workers each covering a sub-window of
// a shared parent product). Resolved execution args and joborder configuration
// are included because they capture resolved context values (e.g., instance
// definitions) that affect processing.
func computeFingerprint(rev *store.StationRevisionRecord, m *store.ManifestRecord, force bool, windowStart, windowEnd time.Time) string {
	entries := make([]any, 0, len(m.Entries))
	for _, e := range m.Entries {
		entries = append(entries, map[string]any{
			"file_type":        e.FileType,
			"category":         e.Category,
			"path":             e.Path,
			"version_metadata": e.VersionMetadata,
		})
	}
	payload := map[string]any{
		"station_revision_id":           rev.RevisionID,
		"station_revision_content_hash": rev.ContentHash,
		"manifest":                      entries,
		"window_start":                  windowStart.UTC().Format(time.RFC3339Nano),
		"window_end":                    windowEnd.UTC().Format(time.RFC3339Nano),
		"force":                         force,
		"declared_execution":            rev.DeclaredExecution,
		"declared_joborder":             rev.DeclaredJobOrder,
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
