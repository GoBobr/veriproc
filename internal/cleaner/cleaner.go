// Package cleaner implements destructive maintenance operations: deleting
// tasks, runs, and time-bounded ranges of work together with their
// control-plane records and on-disk run working roots.
//
// The cleaner is the single authority that removes both database state and
// filesystem artifacts so operators cannot leave one half orphaned. Database
// rows are removed transactionally via the store's cascade helpers; the
// working-root directories recorded on each run are then removed from disk.
//
// Spec references:
//   - §3.7 (run working roots)
//   - §4.3 (control-plane tables)
//   - §6 (CLI maintenance commands)
package cleaner

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/store"
)

// Sentinel errors mapped by the HTTP layer to API error codes.
var (
	// ErrTaskNotFound is returned by DeleteTask when the task does not exist.
	ErrTaskNotFound = errors.New("cleaner: task not found")
	// ErrRunNotFound is returned by DeleteRun when the run does not exist.
	ErrRunNotFound = errors.New("cleaner: run not found")
	// ErrNoCutoff is returned by Clean when neither Before nor After is set.
	ErrNoCutoff = errors.New("cleaner: clean requires a before or after cutoff")
)

// Service performs cascade deletions across the store and the filesystem.
type Service struct {
	store  *store.Store
	logger zerolog.Logger
}

// New constructs a cleaner Service.
func New(st *store.Store, logger zerolog.Logger) *Service {
	return &Service{store: st, logger: logger}
}

// Report summarizes a completed (or, for a dry run, a projected) cleanup.
type Report struct {
	// DryRun is true when no deletion was performed; the counts then describe
	// what *would* be deleted.
	DryRun bool `json:"dry_run"`
	// Counts holds the per-table deleted-row counts.
	Counts store.DeletionCounts `json:"counts"`
	// TaskIDs / RunIDs are the identifiers that were (or would be) deleted.
	TaskIDs []string `json:"task_ids,omitempty"`
	RunIDs  []string `json:"run_ids,omitempty"`
	// WorkingRoots lists the on-disk run working roots that were (or would be)
	// removed.
	WorkingRoots []string `json:"working_roots,omitempty"`
	// WorkingRootsRemoved counts the working-root directories actually deleted
	// from disk (0 for a dry run).
	WorkingRootsRemoved int `json:"working_roots_removed"`
	// FilesystemErrors lists working roots that could not be removed. Database
	// deletion still succeeded; these are reported so the operator can follow
	// up manually.
	FilesystemErrors []string `json:"filesystem_errors,omitempty"`
}

// CleanFilter selects the work to delete in a Clean operation. At least one of
// Before / After must be non-zero. Basis selects which task timestamps the
// cutoffs are compared against:
//
//   - BasisProcessingTime (default): created_at (wall-clock processing time)
//     - Before: tasks whose created_at <= Before
//     - After:  tasks whose created_at >= After
//   - BasisProcessingWindow: the data sensing window
//     - Before: tasks whose window_end <= Before (entirely before the cutoff)
//     - After:  tasks whose window_start >= After (entirely after the cutoff)
//
// When StationID is non-empty, only tasks whose destination_station_id matches
// are selected.
type CleanFilter struct {
	Before    time.Time
	After     time.Time
	Basis     store.CleanupBasis
	StationID string
}

// DeleteRun removes a single run, its dependent control-plane rows, and its
// on-disk working root. The working root is always removed from disk regardless
// of the cascade flag. When cascade is true, all child rows (jobs, artifacts,
// publications, manifests, etc.) are also deleted. When cascade is false
// (default), NOT NULL constrained rows are deleted, nullable FK references are
// set to NULL, and provenance links are left intact.
// When dryRun is true nothing is deleted and the report describes what would
// be removed.
func (s *Service) DeleteRun(ctx context.Context, runID string, dryRun, cascade bool) (*Report, error) {
	run, err := s.store.Runs().Get(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	if dryRun {
		rep := &Report{DryRun: true, RunIDs: []string{run.RunID}}
		if run.WorkingRoot != "" {
			rep.WorkingRoots = []string{run.WorkingRoot}
		}
		return rep, nil
	}
	res, err := s.store.PurgeRun(ctx, runID, cascade)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrRunNotFound
		}
		return nil, err
	}
	rep := s.reportFromPurge(res, false)
	s.removeWorkingRoots(res.WorkingRoots, rep)
	s.logger.Info().
		Str("run_id", runID).
		Bool("cascade", cascade).
		Int("working_roots_removed", rep.WorkingRootsRemoved).
		Msg("cleaner: run deleted")
	return rep, nil
}

// DeleteTask removes a task and its runs. When cascade is true it also removes
// all descendant tasks (via parent_task_id), every dependent control-plane row,
// and all associated on-disk working roots. When cascade is false (default)
// it only removes the explicitly listed task (not descendants), nullifies
// nullable FK references, deletes NOT NULL constrained rows, and removes the
// working roots of the runs that were deleted. Provenance links are always left intact.
// When dryRun is true nothing is deleted and the report describes what would
// be removed.
func (s *Service) DeleteTask(ctx context.Context, taskID string, dryRun, cascade bool) (*Report, error) {
	if _, err := s.store.Tasks().Get(ctx, taskID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	return s.purgeTasks(ctx, []string{taskID}, dryRun, cascade, func(rep *Report) {
		s.logger.Info().
			Str("task_id", taskID).
			Bool("cascade", cascade).
			Int("tasks", rep.Counts.Tasks).
			Int("runs", rep.Counts.Runs).
			Int("working_roots_removed", rep.WorkingRootsRemoved).
			Msg("cleaner: task deleted")
	})
}

// Clean removes every task whose timestamps match the filter, along with
// their runs and dependent rows. When cascade is true it also removes
// descendant tasks (via parent_task_id). When cascade is false (default)
// it only removes the explicitly matching tasks (not descendants), nullifies
// nullable FK references, and deletes NOT NULL constrained rows. Working
// roots of the deleted runs are always removed regardless of cascade.
// Selection is by processing time (created_at) by default, or by the data
// sensing window when the filter requests it.
// When dryRun is true nothing is deleted and the report describes what would
// be removed.
func (s *Service) Clean(ctx context.Context, f CleanFilter, dryRun, cascade bool) (*Report, error) {
	if f.Before.IsZero() && f.After.IsZero() {
		return nil, ErrNoCutoff
	}
	taskIDs, err := s.store.Tasks().IDsForCleanup(ctx, f.Before, f.After, f.Basis, f.StationID)
	if err != nil {
		return nil, err
	}
	if len(taskIDs) == 0 {
		return &Report{DryRun: dryRun}, nil
	}
	return s.purgeTasks(ctx, taskIDs, dryRun, cascade, func(rep *Report) {
		s.logger.Info().
			Time("before", f.Before).
			Time("after", f.After).
			Bool("cascade", cascade).
			Int("tasks", rep.Counts.Tasks).
			Int("runs", rep.Counts.Runs).
			Int("working_roots_removed", rep.WorkingRootsRemoved).
			Msg("cleaner: clean completed")
	})
}

// purgeTasks is the shared deletion path for DeleteTask and Clean. When
// cascade is true it uses the full PurgeTasks (BFS descendants + full purge).
// When cascade is false it nullifies FK references and deletes NOT NULL
// constrained rows. Working roots of deleted runs are always removed
// regardless of cascade because the run rows no longer exist and the disk
// space is orphaned.
func (s *Service) purgeTasks(ctx context.Context, taskIDs []string, dryRun, cascade bool, onDone func(*Report)) (*Report, error) {
	if dryRun {
		return s.projectTasks(ctx, taskIDs, cascade)
	}
	res, err := s.store.PurgeTasks(ctx, taskIDs, cascade)
	if err != nil {
		return nil, err
	}
	rep := s.reportFromPurge(res, false)
	s.removeWorkingRoots(res.WorkingRoots, rep)
	if onDone != nil {
		onDone(rep)
	}
	return rep, nil
}

// projectTasks computes a dry-run report describing what purgeTasks would
// delete without mutating any state. When cascade is true it includes
// descendant tasks (BFS); when cascade is false it only includes the
// explicitly listed tasks.
func (s *Service) projectTasks(ctx context.Context, taskIDs []string, cascade bool) (*Report, error) {
	var closure []string
	if cascade {
		var err error
		closure, err = s.store.CollectTaskClosure(ctx, taskIDs)
		if err != nil {
			return nil, err
		}
	} else {
		// Only keep IDs that actually exist (without expanding descendants).
		for _, id := range taskIDs {
			_, err := s.store.Tasks().Get(ctx, id)
			if err != nil {
				continue
			}
			closure = append(closure, id)
		}
	}
	runIDs, workingRoots, err := s.store.RunIDsAndRootsForTasks(ctx, closure)
	if err != nil {
		return nil, err
	}
	rep := &Report{
		DryRun:       true,
		TaskIDs:      closure,
		RunIDs:       runIDs,
		WorkingRoots: workingRoots,
	}
	rep.Counts.Tasks = len(closure)
	rep.Counts.Runs = len(runIDs)
	return rep, nil
}

func (s *Service) reportFromPurge(res *store.PurgeResult, dryRun bool) *Report {
	return &Report{
		DryRun:       dryRun,
		Counts:       res.Counts,
		TaskIDs:      res.TaskIDs,
		RunIDs:       res.RunIDs,
		WorkingRoots: res.WorkingRoots,
	}
}

// removeWorkingRoots deletes each working-root directory from disk, recording
// successes and failures on the report. A missing directory counts as removed
// (the desired end state is already met).
func (s *Service) removeWorkingRoots(roots []string, rep *Report) {
	for _, root := range roots {
		if root == "" {
			continue
		}
		if err := os.RemoveAll(root); err != nil {
			s.logger.Warn().Err(err).Str("working_root", root).Msg("cleaner: failed to remove working root")
			rep.FilesystemErrors = append(rep.FilesystemErrors, root)
			continue
		}
		rep.WorkingRootsRemoved++
	}
}
