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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

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
	registerGroup     GroupRegistrar
	instanceID        string
	facility          map[string]string
	rollingArchives   map[string]string
	productCategories map[string][]string
	generators        map[string]string
	naming            policy.Naming
	integrity         policy.Integrity
}

// GroupRegistrar is the optional callback invoked after PrepareRun when a
// task carries a non-empty SplitGroupID (Spec §3.11). The runs package does
// not import the groups package directly to keep the dependency one-way.
type GroupRegistrar func(ctx context.Context, splitGroupID, runID, taskID string) error

// Config configures a Service.
type Config struct {
	Store             *store.Store
	Executor          executor.Executor
	Resolver          stations.Resolver
	WorkingRootBase   string
	Clock             func() time.Time
	IDFactory         func() string
	RegisterGroup     GroupRegistrar
	InstanceID        string
	Facility          map[string]string
	RollingArchives   map[string]string
	ProductCategories map[string][]string
	Generators        map[string]string
	Naming            policy.Naming
	Integrity         policy.Integrity
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
		registerGroup:     cfg.RegisterGroup,
		instanceID:        cfg.InstanceID,
		facility:          cloneStringMap(cfg.Facility),
		rollingArchives:   cloneStringMap(cfg.RollingArchives),
		productCategories: cloneStringSliceMap(cfg.ProductCategories),
		generators:        cloneStringMap(cfg.Generators),
		naming:            cfg.Naming,
		integrity:         cfg.Integrity,
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
	workingRoot := s.workingRootPath(rev, task, runID, now)

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
		return nil, err
	}
	manifest, err := s.resolveManifest(ctx, runID, task, rev, workingRoot)
	if err != nil {
		return nil, err
	}
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

	jobOrderPath := filepath.Join(run.WorkingRoot, "joborder.yaml")
	jobOrderArtifact, err := s.writeJobOrder(ctx, run, jobOrderPath)
	if err != nil {
		return nil, fmt.Errorf("write job-order: %w", err)
	}

	// Resolve the run script path from the station revision's declared scripts.
	var scriptPath string
	rev, rerr := s.store.Stations().Get(ctx, run.StationRevisionID)
	if rerr != nil {
		return nil, rerr
	}
	if rev.DeclaredScripts != "" {
		var scripts map[string]string
		if jerr := json.Unmarshal([]byte(rev.DeclaredScripts), &scripts); jerr == nil {
			scriptPath = scripts["run"]
		}
	}

	desc := executor.JobDescription{
		RunID:        run.RunID,
		WorkingRoot:  run.WorkingRoot,
		JobOrderPath: jobOrderPath,
		StationID:    rev.StationID,
		TaskID:       run.TaskID,
		Command:      "run",
		ScriptPath:   scriptPath,
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
		if err := tx.Artifacts().Insert(ctx, jobOrderArtifact); err != nil {
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
	_ = task
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
	for idx, input := range inputs {
		entry := store.ManifestEntry{
			EntryID:  fmt.Sprintf("ent-%s-%02d", sha12(runID+":"+input.FileType), idx),
			FileType: input.FileType,
			Category: input.Category,
			Optional: input.Optional,
		}
		folders := s.productCategories[input.Category]
		if override := rollingFolders[input.Category]; len(override) > 0 {
			folders = override
		}
		candidate, archiveID, precedence, reason, err := s.selectInputCandidate(input, folders)
		if err != nil {
			return nil, err
		}
		if candidate == "" {
			entry.SelectionReason = reason
			if !input.Optional {
				return nil, fmt.Errorf("mandatory input %s not found in category %s", input.FileType, input.Category)
			}
			manifest.Entries = append(manifest.Entries, entry)
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil {
			return nil, err
		}
		linkName := safeInputLinkName(idx, input.FileType, filepath.Base(candidate))
		linkPath := filepath.Join(workingRoot, "input", linkName)
		_ = os.Remove(linkPath)
		if err := os.Symlink(candidate, linkPath); err != nil {
			return nil, fmt.Errorf("symlink input %s: %w", input.FileType, err)
		}
		entry.Path = filepath.ToSlash(filepath.Join("input", linkName))
		entry.Present = true
		entry.Size = info.Size()
		entry.MTime = nullTime(info.ModTime().UTC())
		entry.Checksum, entry.ChecksumAlgo, entry.ChecksumSource = availableChecksum(candidate, s.integrity)
		entry.SourceArchiveID = archiveID
		entry.SourcePrecedence = precedence
		entry.VersionMetadata = versionMetadata(candidate)
		entry.SelectionReason = reason
		manifest.Entries = append(manifest.Entries, entry)
	}
	_ = ctx
	return manifest, nil
}

func (s *Service) selectInputCandidate(input stations.InputDefinition, folders []string) (string, string, int, string, error) {
	if len(folders) == 0 {
		return "", "", 0, "no configured folders", nil
	}
	pattern := input.Pattern
	if pattern == "" {
		pattern = "*" + input.FileType + "*"
	}
	for precedence, folderRef := range folders {
		folder, archiveID, err := s.resolveFolderRef(folderRef)
		if err != nil {
			return "", "", 0, "", err
		}
		matches, err := filepath.Glob(filepath.Join(folder, pattern))
		if err != nil {
			return "", "", 0, "", err
		}
		files := matches[:0]
		for _, match := range matches {
			if info, err := os.Stat(match); err == nil && !info.IsDir() && filenameMatchesInput(filepath.Base(match), input) {
				files = append(files, match)
			}
		}
		if len(files) == 0 {
			continue
		}
		sort.Slice(files, func(i, j int) bool {
			im, _ := os.Stat(files[i])
			jm, _ := os.Stat(files[j])
			if im != nil && jm != nil && !im.ModTime().Equal(jm.ModTime()) {
				return im.ModTime().Before(jm.ModTime())
			}
			return files[i] < files[j]
		})
		return files[len(files)-1], archiveID, precedence + 1, "selected latest by mtime then path within precedence", nil
	}
	return "", "", 0, "no matching candidate", nil
}

func (s *Service) workingRootPath(rev *store.StationRevisionRecord, task *store.TaskRecord, runID string, created time.Time) string {
	values := map[string]string{
		"station_id":   rev.StationID,
		"start":        policy.CompactTaskWindow(task.WindowStart),
		"end":          policy.CompactTaskWindow(task.WindowEnd),
		"created":      policy.CompactRuntimeEvent(created),
		"short_run_id": policy.ShortRunID(runID),
	}
	station := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.StationSegment, values, s.naming)
	taskSegment := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.TaskSegment, values, s.naming)
	runSegment := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.RunSegment, values, s.naming)
	path := filepath.Join(s.workingRootBase, station, taskSegment, runSegment)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return path
	}
	suffix := policy.ExpandWorkingRootSegment(s.naming.WorkingRoot.CollisionSuffix, values, s.naming)
	return filepath.Join(s.workingRootBase, station, taskSegment, runSegment+suffix)
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

func safeInputLinkName(idx int, fileType, base string) string {
	cleanType := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, fileType)
	return fmt.Sprintf("%02d_%s_%s", idx, cleanType, base)
}

func filenameMatchesInput(name string, input stations.InputDefinition) bool {
	if input.Pattern != "" {
		return true
	}
	return strings.Contains(name, input.FileType)
}

func availableChecksum(path string, integrity policy.Integrity) (string, string, string) {
	if integrity.ChecksumPolicy == policy.ChecksumNone {
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

func cloneStringMap(in map[string]string) map[string]string {
	out := map[string]string{}
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
// manifest summary, force}. Spec §3.10 requires the fingerprint to be a
// deterministic function of the inputs that govern processing equivalence.
func computeFingerprint(rev *store.StationRevisionRecord, m *store.ManifestRecord, force bool) string {
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
		"force":                         force,
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
