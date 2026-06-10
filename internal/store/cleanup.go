package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DeletionCounts records how many rows were removed from each table during a
// purge. It is surfaced to operators (CLI / API) so a destructive cleanup can
// be audited.
type DeletionCounts struct {
	Tasks                int `json:"tasks"`
	Runs                 int `json:"runs"`
	Jobs                 int `json:"jobs"`
	Artifacts            int `json:"artifacts"`
	Publications         int `json:"publications"`
	Manifests            int `json:"manifests"`
	ManifestEntries      int `json:"manifest_entries"`
	DeduplicationRecords int `json:"deduplication_records"`
	CanonicalityAudits   int `json:"canonicality_audits"`
	SplitGroupMembers    int `json:"split_group_members"`
	TaskHistoryEntries   int `json:"task_history_entries"`
	ProvenanceLinks      int `json:"provenance_links"`
	IdempotencyRecords   int `json:"idempotency_records"`
}

// Add accumulates counts from another DeletionCounts into c.
func (c *DeletionCounts) Add(o DeletionCounts) {
	c.Tasks += o.Tasks
	c.Runs += o.Runs
	c.Jobs += o.Jobs
	c.Artifacts += o.Artifacts
	c.Publications += o.Publications
	c.Manifests += o.Manifests
	c.ManifestEntries += o.ManifestEntries
	c.DeduplicationRecords += o.DeduplicationRecords
	c.CanonicalityAudits += o.CanonicalityAudits
	c.SplitGroupMembers += o.SplitGroupMembers
	c.TaskHistoryEntries += o.TaskHistoryEntries
	c.ProvenanceLinks += o.ProvenanceLinks
	c.IdempotencyRecords += o.IdempotencyRecords
}

// PurgeResult is returned by the cascade-deletion helpers. WorkingRoots lists
// the on-disk run working roots that the caller is responsible for removing
// (the store never touches the filesystem).
type PurgeResult struct {
	Counts       DeletionCounts `json:"counts"`
	TaskIDs      []string       `json:"task_ids"`
	RunIDs       []string       `json:"run_ids"`
	WorkingRoots []string       `json:"working_roots"`
}

// PurgeRun deletes a single run and every dependent control-plane row.
// When cascade is true it removes all child rows (jobs, artifacts, etc.) and
// returns the working roots for filesystem cleanup. When cascade is false it
// nullifies nullable foreign keys, deletes rows with NOT NULL constraints so
// the run row can be removed, and skips working-root reporting (the caller
// should not touch the filesystem). The owning task and sibling runs are left
// untouched. Provenance links are always left intact.
// Returns ErrNotFound if the run does not exist.
func (s *Store) PurgeRun(ctx context.Context, runID string, cascade bool) (*PurgeResult, error) {
	run, err := s.Runs().Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	res := &PurgeResult{RunIDs: []string{run.RunID}}
	if run.WorkingRoot != "" {
		res.WorkingRoots = append(res.WorkingRoots, run.WorkingRoot)
	}
	err = s.InTx(ctx, func(tx *Tx) error {
		if err := tx.deferForeignKeys(ctx); err != nil {
			return err
		}
		if cascade {
			return deleteRunRows(ctx, tx.tx, []string{run.RunID}, &res.Counts)
		}
		return nullifyRunRows(ctx, tx.tx, []string{run.RunID}, &res.Counts)
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// PurgeTasks deletes the given tasks together with their runs and dependent
// rows. When cascade is true it also expands to all descendant tasks (via
// parent_task_id). Working roots of the deleted runs are always returned
// regardless of cascade (the caller is responsible for removing them from disk).
// Task-scoped row handling differs:
//   - cascade=true: full delete of all rows per deleteTaskRows.
//   - cascade=false: only the explicitly listed tasks are deleted; descendants
//     are orphaned (parent_task_id NULL); nullable FK references are set to NULL;
//     NOT NULL constrained rows are deleted.
// Idempotency records are kept with task_id left as the original value (task no
// longer references them). Unknown task IDs are silently ignored.
func (s *Store) PurgeTasks(ctx context.Context, taskIDs []string, cascade bool) (*PurgeResult, error) {
	res := &PurgeResult{}
	if len(taskIDs) == 0 {
		return res, nil
	}

	// Validate which task IDs actually exist (unknown IDs are dropped).
	var targetIDs []string
	for _, id := range taskIDs {
		var exists string
		err := s.db.QueryRowContext(ctx, `SELECT task_id FROM tasks WHERE task_id = ?`, id).Scan(&exists)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		targetIDs = append(targetIDs, id)
	}
	if len(targetIDs) == 0 {
		return res, nil
	}

	if cascade {
		// Expand to the full closure of descendant tasks. collectTaskClosureInternal
		// skips seed re-validation because PurgeTasks already verified existence above.
		closure, err := s.collectTaskClosureInternal(ctx, targetIDs, true)
		if err != nil {
			return nil, err
		}
		return s.purgeTasksCascade(ctx, closure, res)
	}
	return s.purgeTasksNullify(ctx, targetIDs, res)
}

func (s *Store) purgeTasksCascade(ctx context.Context, closure []string, res *PurgeResult) (*PurgeResult, error) {
	if len(closure) == 0 {
		return res, nil
	}
	res.TaskIDs = closure

	runIDs, workingRoots, err := s.RunIDsAndRootsForTasks(ctx, closure)
	if err != nil {
		return nil, err
	}
	res.RunIDs = runIDs
	res.WorkingRoots = workingRoots

	err = s.InTx(ctx, func(tx *Tx) error {
		if err := tx.deferForeignKeys(ctx); err != nil {
			return err
		}
		if len(runIDs) > 0 {
			if err := deleteRunRows(ctx, tx.tx, runIDs, &res.Counts); err != nil {
				return err
			}
		}
		return deleteTaskRows(ctx, tx.tx, closure, &res.Counts)
	})
	return res, err
}

func (s *Store) purgeTasksNullify(ctx context.Context, taskIDs []string, res *PurgeResult) (*PurgeResult, error) {
	if len(taskIDs) == 0 {
		return res, nil
	}
	res.TaskIDs = taskIDs

	runIDs, workingRoots, err := s.RunIDsAndRootsForTasks(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	res.RunIDs = runIDs
	res.WorkingRoots = workingRoots

	err = s.InTx(ctx, func(tx *Tx) error {
		if err := tx.deferForeignKeys(ctx); err != nil {
			return err
		}
		// 1. Orphan child tasks.
		ph, args := placeholders(taskIDs)
		if _, err := tx.tx.ExecContext(ctx,
			`UPDATE tasks SET parent_task_id = NULL WHERE parent_task_id IN (`+ph+`)`, args...); err != nil {
			return err
		}

		// 2. For each run: delete NOT NULL constrained rows, nullify nullable FKs, delete run.
		runIDsLocal := runIDs
		if len(runIDsLocal) == 0 {
			runIDsLocal = nil
		}
		if len(runIDsLocal) > 0 {
			if err := deleteNotNullForRun(ctx, tx.tx, runIDsLocal, &res.Counts); err != nil {
				return err
			}
			if err := nullifyRunFKs(ctx, tx.tx, runIDsLocal, &res.Counts); err != nil {
				return err
			}
			// Delete the runs themselves (FK on task_id can't be nullified — NOT NULL).
			rph, rargs := placeholders(runIDsLocal)
			if n, err := execCount(ctx, tx.tx,
				`DELETE FROM runs WHERE run_id IN (`+rph+`)`, rargs...); err != nil {
				return err
			} else {
				res.Counts.Runs += n
			}
		}

		// 3. Nullify task-scoped FKs.
		if n, err := execCount(ctx, tx.tx,
			`UPDATE task_history_entries SET task_id = NULL WHERE task_id IN (`+ph+`)`, args...); err != nil {
			return err
		} else {
			res.Counts.TaskHistoryEntries += n
		}
		if n, err := execCount(ctx, tx.tx,
			`DELETE FROM split_group_members WHERE task_id IN (`+ph+`)`, args...); err != nil {
			return err
		} else {
			res.Counts.SplitGroupMembers += n
		}

		// 4. Nullify task's idempotency_record_id, then delete the task.
		// The idempotency_records rows themselves are NOT deleted. They are
		// kept deliberately so that the original submission keys remain
		// recorded and cannot be re-used for a different (re-submitted) task —
		// effectively replay-protection. The cascade path deletes them because
		// the operator has explicitly asked for a complete teardown.
		if _, err := tx.tx.ExecContext(ctx,
			`UPDATE tasks SET idempotency_record_id = NULL WHERE task_id IN (`+ph+`)`, args...); err != nil {
			return err
		}
		if n, err := execCount(ctx, tx.tx,
			`DELETE FROM tasks WHERE task_id IN (`+ph+`)`, args...); err != nil {
			return err
		} else {
			res.Counts.Tasks += n
		}
		return nil
	})
	return res, err
}

// CollectTaskClosure returns the supplied task IDs plus all transitive
// descendants reachable through parent_task_id. Unknown IDs are dropped.
func (s *Store) CollectTaskClosure(ctx context.Context, seeds []string) ([]string, error) {
	return s.collectTaskClosureInternal(ctx, seeds, false)
}

// collectTaskClosureInternal behaves like CollectTaskClosure but when
// skipValidateSeed is true it trusts the caller to have already verified that
// each seed exists (avoids a second SELECT pass inside PurgeTasks).
func (s *Store) collectTaskClosureInternal(ctx context.Context, seeds []string, skipValidateSeed bool) ([]string, error) {
	seen := make(map[string]bool)
	var ordered []string
	queue := make([]string, 0, len(seeds))

	for _, id := range seeds {
		if id == "" || seen[id] {
			continue
		}
		if !skipValidateSeed {
			var exists string
			err := s.db.QueryRowContext(ctx, `SELECT task_id FROM tasks WHERE task_id = ?`, id).Scan(&exists)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return nil, err
			}
		}
		seen[id] = true
		ordered = append(ordered, id)
		queue = append(queue, id)
	}

	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		rows, err := s.db.QueryContext(ctx, `SELECT task_id FROM tasks WHERE parent_task_id = ?`, parent)
		if err != nil {
			return nil, err
		}
		var children []string
		for rows.Next() {
			var child string
			if err := rows.Scan(&child); err != nil {
				rows.Close()
				return nil, err
			}
			children = append(children, child)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		for _, child := range children {
			if seen[child] {
				continue
			}
			seen[child] = true
			ordered = append(ordered, child)
			queue = append(queue, child)
		}
	}
	return ordered, nil
}

// RunIDsAndRootsForTasks returns the run IDs and working roots for every run
// belonging to the supplied tasks.
func (s *Store) RunIDsAndRootsForTasks(ctx context.Context, taskIDs []string) (runIDs, workingRoots []string, err error) {
	if len(taskIDs) == 0 {
		return nil, nil, nil
	}
	ph, args := placeholders(taskIDs)
	rows, err := s.db.QueryContext(ctx,
		`SELECT run_id, working_root FROM runs WHERE task_id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, root string
		if err := rows.Scan(&id, &root); err != nil {
			return nil, nil, err
		}
		runIDs = append(runIDs, id)
		if root != "" {
			workingRoots = append(workingRoots, root)
		}
	}
	return runIDs, workingRoots, rows.Err()
}

// CleanupBasis selects which task timestamps the cleanup cutoffs are compared
// against.
type CleanupBasis int

const (
	// BasisProcessingTime compares against the task's created_at — the
	// wall-clock time the task was submitted and processed.
	BasisProcessingTime CleanupBasis = iota
	// BasisProcessingWindow compares against the task's data sensing window
	// (window_start / window_end).
	BasisProcessingWindow
)

// IDsForCleanup returns the task IDs whose timestamps fall within the requested
// cutoff(s), according to the selected basis.
//
// For BasisProcessingTime (default), cutoffs are compared against created_at:
//
//   - before (non-zero): created_at <= before
//   - after  (non-zero): created_at >= after
//
// For BasisProcessingWindow, cutoffs are compared against the sensing window:
//
//   - before (non-zero): window_end <= before  (window lies entirely before the cutoff)
//   - after  (non-zero): window_start >= after (window lies entirely after the cutoff)
//
// When both are supplied they are combined with AND. At least one cutoff must
// be non-zero.
func (r *TaskRepo) IDsForCleanup(ctx context.Context, before, after time.Time, basis CleanupBasis) ([]string, error) {
	beforeCol, afterCol, orderCol := "created_at", "created_at", "created_at"
	if basis == BasisProcessingWindow {
		beforeCol, afterCol, orderCol = "window_end", "window_start", "window_start"
	}
	var conds []string
	var args []any
	if !before.IsZero() {
		conds = append(conds, beforeCol+" <= ?")
		args = append(args, before.UTC())
	}
	if !after.IsZero() {
		conds = append(conds, afterCol+" >= ?")
		args = append(args, after.UTC())
	}
	if len(conds) == 0 {
		return nil, fmt.Errorf("store: IDsForCleanup requires a before or after cutoff")
	}
	rows, err := r.q.QueryContext(ctx,
		`SELECT task_id FROM tasks WHERE `+strings.Join(conds, " AND ")+` ORDER BY `+orderCol, args...)
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

// deleteRunRows removes the supplied runs and every row that references them.
// It relies on deferred foreign keys being enabled on the transaction so the
// statement order does not need to satisfy referential integrity until commit.
func deleteRunRows(ctx context.Context, q querier, runIDs []string, c *DeletionCounts) error {
	if len(runIDs) == 0 {
		return nil
	}
	ph, args := placeholders(runIDs)

	// Rolling-archive publications (reference both artifacts and runs).
	if n, err := execCount(ctx, q,
		`DELETE FROM rolling_archive_publications WHERE producing_run_id IN (`+ph+`)
		 OR artifact_id IN (SELECT artifact_id FROM artifacts WHERE producing_run_id IN (`+ph+`))`,
		append(args, args...)...); err != nil {
		return err
	} else {
		c.Publications += n
	}

	// Artifacts produced by the runs.
	if n, err := execCount(ctx, q,
		`DELETE FROM artifacts WHERE producing_run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Artifacts += n
	}

	// Jobs.
	if n, err := execCount(ctx, q,
		`DELETE FROM jobs WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Jobs += n
	}

	// Resolved input manifest entries, then the manifests.
	if n, err := execCount(ctx, q,
		`DELETE FROM resolved_input_entries WHERE manifest_id IN
		 (SELECT manifest_id FROM resolved_input_manifests WHERE run_id IN (`+ph+`))`, args...); err != nil {
		return err
	} else {
		c.ManifestEntries += n
	}
	if n, err := execCount(ctx, q,
		`DELETE FROM resolved_input_manifests WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Manifests += n
	}

	// Deduplication records keyed by canonical run.
	if n, err := execCount(ctx, q,
		`DELETE FROM deduplication_records WHERE canonical_run_id IN (`+ph+`)
		 OR superseded_by_run_id IN (`+ph+`)`, append(args, args...)...); err != nil {
		return err
	} else {
		c.DeduplicationRecords += n
	}

	// Canonicality audit rows referencing the runs.
	if n, err := execCount(ctx, q,
		`DELETE FROM canonicality_audit WHERE new_run_id IN (`+ph+`)
		 OR previous_run_id IN (`+ph+`)`, append(args, args...)...); err != nil {
		return err
	} else {
		c.CanonicalityAudits += n
	}

	// Split-group memberships referencing the runs.
	if n, err := execCount(ctx, q,
		`DELETE FROM split_group_members WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.SplitGroupMembers += n
	}

	// Provenance links (no FK; best-effort cleanup of dangling references).
	if n, err := execCount(ctx, q,
		`DELETE FROM provenance_links WHERE (source_type = 'run' AND source_id IN (`+ph+`))
		 OR (target_type = 'run' AND target_id IN (`+ph+`))`, append(args, args...)...); err != nil {
		return err
	} else {
		c.ProvenanceLinks += n
	}

	// processing_fingerprints.canonical_run_id has no FK; clear the pointer so
	// the fingerprint no longer claims a deleted run as canonical (allows the
	// same input to be reprocessed after cleanup).
	if _, err := q.ExecContext(ctx,
		`UPDATE processing_fingerprints SET canonical_run_id = NULL WHERE canonical_run_id IN (`+ph+`)`, args...); err != nil {
		return err
	}

	// Finally the runs themselves.
	if n, err := execCount(ctx, q,
		`DELETE FROM runs WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Runs += n
	}
	return nil
}

// deleteTaskRows removes the supplied tasks plus their task-scoped rows
// (history entries, split-group memberships, provenance links and the owning
// idempotency records). Run-scoped rows must already have been deleted by
// deleteRunRows. Deferred foreign keys must be enabled on the transaction.
func deleteTaskRows(ctx context.Context, q querier, taskIDs []string, c *DeletionCounts) error {
	if len(taskIDs) == 0 {
		return nil
	}
	ph, args := placeholders(taskIDs)

	if n, err := execCount(ctx, q,
		`DELETE FROM task_history_entries WHERE task_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.TaskHistoryEntries += n
	}

	if n, err := execCount(ctx, q,
		`DELETE FROM split_group_members WHERE task_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.SplitGroupMembers += n
	}

	if n, err := execCount(ctx, q,
		`DELETE FROM provenance_links WHERE (source_type = 'task' AND source_id IN (`+ph+`))
		 OR (target_type = 'task' AND target_id IN (`+ph+`))`, append(args, args...)...); err != nil {
		return err
	} else {
		c.ProvenanceLinks += n
	}

	// Idempotency records owned by the deleted tasks. The tasks reference these
	// via idempotency_record_id; with deferred FKs the order is irrelevant.
	if n, err := execCount(ctx, q,
		`DELETE FROM idempotency_records WHERE task_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.IdempotencyRecords += n
	}

	if n, err := execCount(ctx, q,
		`DELETE FROM tasks WHERE task_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Tasks += n
	}
	return nil
}

// nullifyRunRows is the non-cascade path for PurgeRun. It deletes NOT NULL
// constrained rows, nullifies nullable FK references, and removes the run
// rows. Provenance links are left intact.
func nullifyRunRows(ctx context.Context, q querier, runIDs []string, c *DeletionCounts) error {
	if len(runIDs) == 0 {
		return nil
	}
	if err := deleteNotNullForRun(ctx, q, runIDs, c); err != nil {
		return err
	}
	if err := nullifyRunFKs(ctx, q, runIDs, c); err != nil {
		return err
	}
	ph, args := placeholders(runIDs)
	if n, err := execCount(ctx, q,
		`DELETE FROM runs WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Runs += n
	}
	return nil
}

// deleteNotNullForRun deletes rows whose FK to runs is NOT NULL, so the run
// can be removed cleanly. This is used by the non-cascade nullify path.
func deleteNotNullForRun(ctx context.Context, q querier, runIDs []string, c *DeletionCounts) error {
	ph, args := placeholders(runIDs)

	// jobs (run_id NOT NULL).
	if n, err := execCount(ctx, q,
		`DELETE FROM jobs WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Jobs += n
	}

	// resolved_input_entries then manifests (run_id NOT NULL UNIQUE).
	if n, err := execCount(ctx, q,
		`DELETE FROM resolved_input_entries WHERE manifest_id IN
		 (SELECT manifest_id FROM resolved_input_manifests WHERE run_id IN (`+ph+`))`, args...); err != nil {
		return err
	} else {
		c.ManifestEntries += n
	}
	if n, err := execCount(ctx, q,
		`DELETE FROM resolved_input_manifests WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.Manifests += n
	}

	// split_group_members (run_id and task_id are NOT NULL primary key columns).
	if n, err := execCount(ctx, q,
		`DELETE FROM split_group_members WHERE run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.SplitGroupMembers += n
	}

	// deduplication_records (canonical_run_id NOT NULL). Delete rows where the
	// run is the canonical run or the superseding run.
	if n, err := execCount(ctx, q,
		`DELETE FROM deduplication_records WHERE canonical_run_id IN (`+ph+`)
		 OR superseded_by_run_id IN (`+ph+`)`, append(args, args...)...); err != nil {
		return err
	} else {
		c.DeduplicationRecords += n
	}

	// canonicality_audit (new_run_id NOT NULL). Delete rows where the run is
	// the new canonical run; nullify previous_run_id separately.
	if n, err := execCount(ctx, q,
		`DELETE FROM canonicality_audit WHERE new_run_id IN (`+ph+`)`, args...); err != nil {
		return err
	} else {
		c.CanonicalityAudits += n
	}

	return nil
}

// nullifyRunFKs sets nullable FK columns to NULL for the given runs. It does
// NOT touch provenance_links (per user request). Used by the non-cascade path.
func nullifyRunFKs(ctx context.Context, q querier, runIDs []string, c *DeletionCounts) error {
	ph, args := placeholders(runIDs)

	// artifacts (producing_run_id is nullable).
	if _, err := q.ExecContext(ctx,
		`UPDATE artifacts SET producing_run_id = NULL WHERE producing_run_id IN (`+ph+`)`, args...); err != nil {
		return err
	}

	// rolling_archive_publications (producing_run_id is nullable; artifact FK is NOT NULL
	// so we can't remove the publication — just nullify the run reference).
	if _, err := q.ExecContext(ctx,
		`UPDATE rolling_archive_publications SET producing_run_id = NULL WHERE producing_run_id IN (`+ph+`)`, args...); err != nil {
		return err
	}

	// Nullify previous_run_id in canonicality_audit (nullable).
	if _, err := q.ExecContext(ctx,
		`UPDATE canonicality_audit SET previous_run_id = NULL WHERE previous_run_id IN (`+ph+`)`, args...); err != nil {
		return err
	}

	// processing_fingerprints (canonical_run_id is nullable).
	if _, err := q.ExecContext(ctx,
		`UPDATE processing_fingerprints SET canonical_run_id = NULL WHERE canonical_run_id IN (`+ph+`)`, args...); err != nil {
		return err
	}

	return nil
}

// deferForeignKeys enables PRAGMA defer_foreign_keys for the active
// transaction so cascade deletes need not satisfy referential integrity until
// commit. The pragma resets automatically when the transaction completes.
func (t *Tx) deferForeignKeys(ctx context.Context) error {
	_, err := t.tx.ExecContext(ctx, `PRAGMA defer_foreign_keys = ON`)
	return err
}

// execCount runs a DELETE and returns the number of affected rows.
func execCount(ctx context.Context, q querier, query string, args ...any) (int, error) {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// placeholders returns a comma-separated list of "?" placeholders and the
// corresponding []any args for the supplied string values.
func placeholders(values []string) (string, []any) {
	if len(values) == 0 {
		return "", nil
	}
	marks := make([]string, len(values))
	args := make([]any, len(values))
	for i, v := range values {
		marks[i] = "?"
		args[i] = v
	}
	return strings.Join(marks, ", "), args
}
