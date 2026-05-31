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

	toml "github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"github.com/eum/veriproc/internal/canonjson"
	"github.com/eum/veriproc/internal/fileops"
	"github.com/eum/veriproc/internal/policy"
	"github.com/eum/veriproc/internal/stations"
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

	logArtifacts, err := s.collectRunLogs(run)
	if err != nil {
		return nil, fmt.Errorf("write log: %w", err)
	}

	outputArtifacts, err := s.validateOutputs(ctx, run)
	if err != nil {
		_ = s.MarkFailedAndLog(ctx, runID, err.Error())
		return nil, fmt.Errorf("validate outputs: %w", err)
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

	// Emit a structured log explaining the canonicality decision so operators
	// can diagnose why a run is or is not triggering downstream tasks.
	canonRunRef := fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex)
	switch canonicality {
	case "canonical":
		s.logger.Debug().
			Str("run_ref", canonRunRef).
			Str("fingerprint", run.ProcessingFingerprint).
			Msg("run elected canonical")
	case "duplicate":
		s.logger.Warn().
			Str("run_ref", canonRunRef).
			Str("fingerprint", run.ProcessingFingerprint).
			Msg("run is duplicate — fingerprint already owned; downstream tasks will still be triggered")
	case "forced":
		s.logger.Info().
			Str("run_ref", canonRunRef).
			Msg("run is forced — does not compete for fingerprint ownership")
	}

	now := s.clock().UTC()
	publicationRecords, err := s.buildPublicationRecords(ctx, run, outputArtifacts)
	if err != nil {
		_ = s.MarkFailedAndLog(ctx, runID, err.Error())
		return nil, err
	}
	downstreamPlan, err := s.buildDownstreamPlan(ctx, run, task, canonicality)
	if err != nil {
		_ = s.MarkFailedAndLog(ctx, runID, err.Error())
		return nil, err
	}
	downstreamTasks := downstreamPlan.Tasks
	parentRunRef := fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex)
	for _, dt := range downstreamTasks {
		s.logger.Info().
			Str("task_id", dt.TaskID).
			Str("station", dt.DestinationStationID).
			Time("window_start", dt.WindowStart).
			Time("window_end", dt.WindowEnd).
			Str("trigger", parentRunRef).
			Msg("downstream task generated")
	}

	err = s.store.InTx(ctx, func(tx *store.Tx) error {
		for _, logArtifact := range logArtifacts {
			if err := tx.Artifacts().Insert(ctx, logArtifact); err != nil {
				return err
			}
		}
		for _, oa := range outputArtifacts {
			if err := tx.Artifacts().Insert(ctx, oa); err != nil {
				return err
			}
		}
		if downstreamPlan.TaskOutArtifact != nil {
			if err := tx.Artifacts().Insert(ctx, downstreamPlan.TaskOutArtifact); err != nil {
				return err
			}
		}
		for _, pub := range publicationRecords {
			if err := tx.Publications().Insert(ctx, pub); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
		}
		for _, group := range downstreamPlan.SplitGroups {
			if _, err := tx.SplitGroups().EnsureGroup(ctx, group.GroupID, group.Label, group.Description, group.ExpectedMembers, now); err != nil {
				return err
			}
			if group.Close {
				if err := tx.SplitGroups().SetState(ctx, group.GroupID, store.SplitGroupStateAggregating, now); err != nil {
					return err
				}
			}
		}
		for _, child := range downstreamTasks {
			if err := tx.Tasks().Insert(ctx, child); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
			link := &store.ProvenanceLink{LinkID: "prov-" + sha12(run.RunID+":"+child.TaskID), SourceType: "run", SourceID: run.RunID, TargetType: "task", TargetID: child.TaskID, RelationshipType: "produced_downstream", Role: "parent", Reason: downstreamPlan.ProvenanceReason, CreatedAt: now}
			if err := tx.Provenance().Insert(ctx, link); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
		}
		if err := tx.Runs().MarkComplete(ctx, runID, canonicality, now); err != nil {
			return err
		}
		if err := tx.Tasks().SetLatestRun(ctx, run.TaskID, run.RetryIndex); err != nil {
			return err
		}
		if canonicality == "canonical" {
			if err := tx.Tasks().SetCanonicalRun(ctx, run.TaskID, run.RetryIndex, now); err != nil {
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
		} else if canonicality == "duplicate" {
			// Duplicate runs still complete the task — the canonical run from
			// an earlier or concurrent submission already owns the fingerprint
			// and has triggered downstream. The task must not stay in
			// "accepted" indefinitely.
			if err := tx.Tasks().SetState(ctx, run.TaskID, "completed", ""); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Log task finalization at INFO so operators can track end-to-end progress.
	finalRunRef := fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex)
	s.logger.Info().
		Str("run_ref", finalRunRef).
		Str("station", task.DestinationStationID).
		Time("window_start", task.WindowStart).
		Time("window_end", task.WindowEnd).
		Str("canonicality", canonicality).
		Time("finalized_at", now).
		Msg("task finalized")
	// After the transaction commits, fire the group-complete notifier if this
	// run became canonical and belongs to a split group. Run in a goroutine so
	// finalization is not delayed by downstream submission latency.
	if (canonicality == "canonical" || canonicality == "forced") &&
		task.SplitGroupID != "" && s.notifyGroupComplete != nil {
		groupID := task.SplitGroupID
		notifier := s.notifyGroupComplete
		go notifier(context.Background(), groupID)
	}
	return s.store.Runs().Get(ctx, runID)
}

// writeJobOrder writes the joborder file to the working root before the
// executor is invoked. Returns the resolved path and artifact record, or
// ("", nil, nil) when joborder.format is "none". The file captures the run
// identity and dispatch context so that a human or real executor can inspect
// what was submitted.
func (s *Service) writeJobOrder(ctx context.Context, run *store.RunRecord) (string, *store.ArtifactRecord, error) {
	task, err := s.store.Tasks().Get(ctx, run.TaskID)
	if err != nil {
		return "", nil, err
	}
	rev, err := s.store.Stations().Get(ctx, run.StationRevisionID)
	if err != nil {
		return "", nil, err
	}

	// Parse the station's joborder configuration.
	var joCfg stations.JobOrderConfig
	if rev.DeclaredJobOrder != "" {
		if jerr := json.Unmarshal([]byte(rev.DeclaredJobOrder), &joCfg); jerr != nil {
			return "", nil, fmt.Errorf("parse declared_joborder: %w", jerr)
		}
	}
	// Apply defaults (normalizeDefinition already does this but guard again).
	if joCfg.Renderer == "" {
		joCfg.Renderer = "default"
	}
	if joCfg.Format == "" {
		joCfg.Format = "yaml"
	}
	if joCfg.Format == "none" {
		// No joborder file, no artifact.
		return "", nil, nil
	}
	if joCfg.Name == "" {
		switch joCfg.Format {
		case "toml":
			joCfg.Name = "joborder.toml"
		case "json":
			joCfg.Name = "joborder.json"
		default:
			joCfg.Name = "joborder.yaml"
		}
	}

	path := filepath.Join(run.WorkingRoot, joCfg.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", nil, err
	}

	manifest, err := s.store.Manifests().GetByRun(ctx, run.RunID)
	if err != nil {
		return "", nil, err
	}
	manifestPath, err := s.writeManifestExport(run, manifest)
	if err != nil {
		return "", nil, err
	}
	outputs, err := declaredOutputs(rev)
	if err != nil {
		return "", nil, err
	}

	// Resolve effective path mode: station-level joborder.paths overrides the
	// instance-level generators.job_order.paths when explicitly set.
	effectivePathMode := s.jobOrderPaths
	if joCfg.Paths != "" {
		effectivePathMode = joCfg.Paths
	}

	var body []byte
	if joCfg.Renderer == "template" {
		runCtx := s.buildRunContext(run, task, rev, path)
		resolvedParams, rerr := stations.ResolveMap(joCfg.Params, runCtx)
		if rerr != nil {
			return "", nil, fmt.Errorf("resolve joborder.params: %w", rerr)
		}
		joCfg.Params = resolvedParams
		body, err = renderTemplateJobOrder(run, task, rev, manifest, outputs, filepath.ToSlash(manifestPath), joCfg, effectivePathMode)
		if err != nil {
			return "", nil, fmt.Errorf("render template joborder: %w", err)
		}
	} else {
		// Build the base joborder document.
		doc := jobOrderDocument(run, task, rev, manifest, outputs, filepath.ToSlash(manifestPath), s.generators, effectivePathMode)

		// Drop veriproc_meta when the station explicitly sets meta: false.
		if joCfg.Meta != nil && !*joCfg.Meta {
			delete(doc, "veriproc_meta")
		}

		// Resolve and merge joborder.include into doc.
		if len(joCfg.Include) > 0 {
			runCtx := s.buildRunContext(run, task, rev, path)
			resolvedInclude, rerr := stations.ResolveMap(joCfg.Include, runCtx)
			if rerr != nil {
				return "", nil, fmt.Errorf("resolve joborder.include: %w", rerr)
			}
			if merr := mergeJobOrderInclude(doc, resolvedInclude); merr != nil {
				return "", nil, fmt.Errorf("merge joborder.include: %w", merr)
			}
		}

		// Render to the configured format.
		body, err = renderJobOrder(doc, joCfg.Format)
		if err != nil {
			return "", nil, fmt.Errorf("render joborder (%s): %w", joCfg.Format, err)
		}
	}

	if err := os.WriteFile(path, body, 0o644); err != nil {
		return "", nil, err
	}
	return path, &store.ArtifactRecord{
		ArtifactID:       "art-" + sha12(run.RunID+":joborder"),
		ProducingRunID:   run.RunID,
		LogicalType:      "joborder",
		ObjectKind:       store.ObjectKindRegularFile,
		FileType:         "JOB_ORDER",
		Path:             path,
		Size:             int64(len(body)),
		ValidationStatus: "validated",
		Availability:     "available",
		CreatedAt:        s.clock().UTC(),
	}, nil
}

func (s *Service) validateOutputs(ctx context.Context, run *store.RunRecord) ([]*store.ArtifactRecord, error) {
	rev, err := s.store.Stations().Get(ctx, run.StationRevisionID)
	if err != nil {
		return nil, err
	}
	outputs, err := declaredOutputs(rev)
	if err != nil {
		return nil, err
	}
	if len(outputs) == 0 {
		return nil, nil
	}
	dir := filepath.Join(run.WorkingRoot, "output")
	arts := make([]*store.ArtifactRecord, 0, len(outputs))
	for _, out := range outputs {
		if out.Multiple {
			// Fan-out outputs: collect all files matching the pattern.
			paths, merr := s.resolveAllOutputPaths(dir, out)
			if merr != nil {
				return nil, merr
			}
			for _, path := range paths {
				name := filepath.Base(path)
				info, serr := os.Stat(path)
				if serr != nil {
					return nil, fmt.Errorf("output %s missing at %s", out.FileType, path)
				}
				objectKind := objectKindFromInfo(info)
				var size int64
				if objectKind == store.ObjectKindRegularFile {
					size = info.Size()
				}
				checksum, algo, source := "", "", ""
				if objectKind == store.ObjectKindRegularFile {
					checksum, algo, source = availableChecksum(path, s.integrity)
				}
				arts = append(arts, &store.ArtifactRecord{
					ArtifactID:       "art-" + sha12(run.RunID+":output:"+name),
					ProducingRunID:   run.RunID,
					LogicalType:      "output",
					ObjectKind:       objectKind,
					FileType:         out.FileType,
					Path:             path,
					Size:             size,
					Checksum:         checksum,
					ChecksumAlgo:     algo,
					ChecksumSource:   source,
					ValidationStatus: "validated",
					Availability:     "available",
					CreatedAt:        s.clock().UTC(),
				})
			}
			continue
		}
		path, name, err := s.resolveOutputPath(dir, out)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			entries, _ := os.ReadDir(dir)
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return nil, fmt.Errorf("mandatory output %s missing at %s (output dir contains: %v)", out.FileType, path, names)
		}
		objectKind := objectKindFromInfo(info)
		if out.ObjectKind != "" && out.ObjectKind != objectKind {
			return nil, fmt.Errorf("output %s at %s has object_kind %s, want %s", out.FileType, path, objectKind, out.ObjectKind)
		}
		var size int64
		if objectKind == store.ObjectKindRegularFile {
			size = info.Size()
		}
		checksum, algo, source := "", "", ""
		if objectKind == store.ObjectKindRegularFile {
			checksum, algo, source = availableChecksum(path, s.integrity)
		}
		arts = append(arts, &store.ArtifactRecord{
			ArtifactID:       "art-" + sha12(run.RunID+":output:"+name),
			ProducingRunID:   run.RunID,
			LogicalType:      "output",
			ObjectKind:       objectKind,
			FileType:         out.FileType,
			Path:             path,
			Size:             size,
			Checksum:         checksum,
			ChecksumAlgo:     algo,
			ChecksumSource:   source,
			ValidationStatus: "validated",
			Availability:     "available",
			CreatedAt:        s.clock().UTC(),
		})
	}
	return arts, nil
}

// resolveAllOutputPaths returns the paths of all files in dir that match the
// output definition's filename pattern (or Pattern glob). It is used when
// Multiple is true — i.e. a station produces a variable number of output files
// of the same file type per run.
func (s *Service) resolveAllOutputPaths(dir string, out stations.OutputDefinition) ([]string, error) {
	effective := policy.EffectiveFilenamePattern(s.naming.Filenames.FilenamePattern, out.FilenamePattern, out.FileType)
	if effective != "" {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		var matches []string
		for _, entry := range entries {
			if out.ObjectKind != "" {
				if info, ierr := entry.Info(); ierr == nil {
					if objectKindFromInfo(info) != out.ObjectKind {
						continue
					}
				}
			}
			if _, perr := policy.ParseFilename(entry.Name(), effective, s.naming.Filenames.Components); perr == nil {
				matches = append(matches, filepath.Join(dir, entry.Name()))
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("mandatory output %s (multiple): no files matched pattern %q in %s", out.FileType, effective, dir)
		}
		sort.Strings(matches)
		return matches, nil
	}
	if out.Pattern != "" {
		matches, err := filepath.Glob(filepath.Join(dir, out.Pattern))
		if err != nil {
			return nil, err
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("mandatory output %s (multiple): no files matched glob %q in %s", out.FileType, out.Pattern, dir)
		}
		sort.Strings(matches)
		return matches, nil
	}
	// No pattern — fall back to single-file resolve for compatibility.
	path, _, err := s.resolveOutputPath(dir, out)
	if err != nil {
		return nil, err
	}
	return []string{path}, nil
}

func (s *Service) resolveOutputPath(dir string, out stations.OutputDefinition) (string, string, error) {
	name := out.Name
	if name != "" {
		path := filepath.Join(dir, name)
		if out.FilenamePattern != "" {
			effective := policy.EffectiveFilenamePattern(s.naming.Filenames.FilenamePattern, out.FilenamePattern, out.FileType)
			if _, err := policy.ParseFilename(filepath.Base(path), effective, s.naming.Filenames.Components); err != nil {
				return "", "", fmt.Errorf("output %s filename_pattern validation: %w", out.FileType, err)
			}
		}
		return path, name, nil
	}
	effective := policy.EffectiveFilenamePattern(s.naming.Filenames.FilenamePattern, out.FilenamePattern, out.FileType)
	if effective != "" {
		matches := []string{}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return "", "", err
		}
		var parseErrs []string
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if info, ierr := entry.Info(); ierr == nil {
				kind := objectKindFromInfo(info)
				if out.ObjectKind != "" && out.ObjectKind != kind {
					continue
				}
			}
			if _, perr := policy.ParseFilename(entry.Name(), effective, s.naming.Filenames.Components); perr == nil {
				matches = append(matches, path)
			} else {
				parseErrs = append(parseErrs, entry.Name()+":"+perr.Error())
			}
		}
		if len(matches) == 0 {
			return "", "", fmt.Errorf("mandatory output %s: no file matched pattern %q (scan errors: %v)", out.FileType, effective, parseErrs)
		}
		sort.Slice(matches, func(i, j int) bool {
			im, _ := os.Stat(matches[i])
			jm, _ := os.Stat(matches[j])
			if im != nil && jm != nil && !im.ModTime().Equal(jm.ModTime()) {
				return im.ModTime().Before(jm.ModTime())
			}
			return matches[i] < matches[j]
		})
		path := matches[len(matches)-1]
		return path, filepath.Base(path), nil
	}
	if out.Pattern != "" {
		matches, err := filepath.Glob(filepath.Join(dir, out.Pattern))
		if err != nil {
			return "", "", err
		}
		if len(matches) == 0 {
			return "", "", fmt.Errorf("mandatory output %s missing matching pattern %s", out.FileType, out.Pattern)
		}
		if out.ObjectKind != "" {
			filtered := matches[:0]
			for _, match := range matches {
				info, err := os.Stat(match)
				if err != nil {
					continue
				}
				if objectKindFromInfo(info) == out.ObjectKind {
					filtered = append(filtered, match)
				}
			}
			matches = filtered
			if len(matches) == 0 {
				return "", "", fmt.Errorf("mandatory output %s missing matching pattern %s with object_kind %s", out.FileType, out.Pattern, out.ObjectKind)
			}
		}
		sort.Strings(matches)
		path := matches[len(matches)-1]
		return path, filepath.Base(path), nil
	}
	if out.FileType == "" {
		return "", "", fmt.Errorf("mandatory output has neither name nor file_type")
	}
	return filepath.Join(dir, out.FileType), out.FileType, nil
}

func (s *Service) collectRunLogs(run *store.RunRecord) ([]*store.ArtifactRecord, error) {
	dir := filepath.Join(run.WorkingRoot, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	now := s.clock().UTC()
	logSpecs := []struct {
		name     string
		fileType string
	}{
		{name: "run_out.log", fileType: "RUN_STDOUT"},
		{name: "run_err.log", fileType: "RUN_STDERR"},
	}
	arts := make([]*store.ArtifactRecord, 0, len(logSpecs))
	for _, spec := range logSpecs {
		path := filepath.Join(dir, spec.name)
		body, err := os.ReadFile(path)
		if err != nil {
			if spec.name == "run_err.log" {
				body = []byte(fmt.Sprintf("run=%s task=%s state=finalizing\n",
					run.RunID, run.TaskID))
			} else {
				body = nil
			}
			if err := os.WriteFile(path, body, 0o644); err != nil {
				return nil, err
			}
		}
		arts = append(arts, &store.ArtifactRecord{
			ArtifactID:       "art-" + sha12(run.RunID+":"+spec.fileType),
			ProducingRunID:   run.RunID,
			LogicalType:      "log",
			ObjectKind:       store.ObjectKindRegularFile,
			FileType:         spec.fileType,
			Path:             path,
			Size:             int64(len(body)),
			ValidationStatus: "validated",
			Availability:     "available",
			CreatedAt:        now,
		})
	}
	return arts, nil
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
	if logArts, werr := s.collectRunLogs(run); werr == nil {
		for _, logArt := range logArts {
			logArt.ValidationStatus = "failed"
			_ = s.store.Artifacts().Insert(ctx, logArt)
		}
	}
	if err := s.store.Tasks().SetState(ctx, run.TaskID, "failed", reason); err != nil {
		return err
	}
	// Fire the group-complete notifier even on failure so partial-aggregation
	// logic in the notifier can advance the group state (e.g. statE runs when
	// at least one split-group member succeeded, even if others failed).
	if task, terr := s.store.Tasks().Get(ctx, run.TaskID); terr == nil &&
		task.SplitGroupID != "" && s.notifyGroupComplete != nil {
		groupID := task.SplitGroupID
		notifier := s.notifyGroupComplete
		go notifier(context.Background(), groupID)
	}
	return nil
}

func declaredOutputs(rev *store.StationRevisionRecord) ([]stations.OutputDefinition, error) {
	if rev.DeclaredOutputs == "" {
		return nil, nil
	}
	var outputs []stations.OutputDefinition
	if err := json.Unmarshal([]byte(rev.DeclaredOutputs), &outputs); err != nil {
		return nil, fmt.Errorf("parse declared_outputs: %w", err)
	}
	return outputs, nil
}

func (s *Service) writeManifestExport(run *store.RunRecord, manifest *store.ManifestRecord) (string, error) {
	path := filepath.Join(run.WorkingRoot, "manifest", "resolved-inputs.yaml")
	type exportEntry struct {
		EntryID                  string `yaml:"entry_id"`
		FileType                 string `yaml:"file_type"`
		Category                 string `yaml:"category,omitempty"`
		Optional                 bool   `yaml:"optional"`
		Present                  bool   `yaml:"present"`
		Path                     string `yaml:"path,omitempty"`
		ObjectKind               string `yaml:"object_kind,omitempty"`
		Size                     int64  `yaml:"size,omitempty"`
		SourceArchiveID          string `yaml:"source_archive_id,omitempty"`
		FolderPriority           int    `yaml:"folder_priority,omitempty"`
		EffectiveFilenamePattern string `yaml:"effective_filename_pattern,omitempty"`
		FilenameComponents       string `yaml:"filename_components,omitempty"`
		WindowMatch              string `yaml:"window_match,omitempty"`
		IntervalGroupKey         string `yaml:"interval_group_key,omitempty"`
		Discriminator            string `yaml:"discriminator,omitempty"`
		WinnerMetadata           string `yaml:"winner_metadata,omitempty"`
		SelectionReason          string `yaml:"selection_reason,omitempty"`
	}
	entries := make([]exportEntry, 0, len(manifest.Entries))
	for _, e := range manifest.Entries {
		entries = append(entries, exportEntry{
			EntryID:                  e.EntryID,
			FileType:                 e.FileType,
			Category:                 e.Category,
			Optional:                 e.Optional,
			Present:                  e.Present,
			Path:                     e.Path,
			ObjectKind:               e.ObjectKind,
			Size:                     e.Size,
			SourceArchiveID:          e.SourceArchiveID,
			FolderPriority:           e.FolderPriority,
			EffectiveFilenamePattern: e.EffectiveFilenamePattern,
			FilenameComponents:       e.FilenameComponents,
			WindowMatch:              e.WindowMatch,
			IntervalGroupKey:         e.IntervalGroupKey,
			Discriminator:            e.Discriminator,
			WinnerMetadata:           e.WinnerMetadata,
			SelectionReason:          e.SelectionReason,
		})
	}
	body, err := yaml.Marshal(map[string]any{
		"schema_version": "veriproc.manifest/v1",
		"run_id":         run.RunID,
		"entries":        entries,
	})
	if err != nil {
		return "", err
	}
	return "./manifest/resolved-inputs.yaml", os.WriteFile(path, body, 0o644)
}

func jobOrderDocument(run *store.RunRecord, task *store.TaskRecord, rev *store.StationRevisionRecord, manifest *store.ManifestRecord, outputs []stations.OutputDefinition, manifestPath string, generators map[string]string, pathMode string) map[string]any {
	absolutePaths := pathMode == "absolute"
	// Group manifest entries by (file_type, category) to support multiple
	// selected objects (distinct interval groups) per declared input.
	type inputKey struct{ fileType, category string }
	inputMap := map[inputKey][]string{}
	inputOrder := []inputKey{}
	for _, entry := range manifest.Entries {
		key := inputKey{entry.FileType, entry.Category}
		if _, exists := inputMap[key]; !exists {
			inputOrder = append(inputOrder, key)
			inputMap[key] = []string{}
		}
		if entry.Present && entry.Path != "" {
			if absolutePaths {
				inputMap[key] = append(inputMap[key], filepath.Join(run.WorkingRoot, filepath.FromSlash(entry.Path)))
			} else {
				inputMap[key] = append(inputMap[key], "./"+entry.Path)
			}
		}
	}
	inputs := make([]map[string]any, 0, len(inputOrder))
	for _, key := range inputOrder {
		inputs = append(inputs, map[string]any{
			"file_type": key.fileType,
			"files":     inputMap[key],
		})
	}
	outputDir := filepath.Join(run.WorkingRoot, "output")
	if !absolutePaths {
		outputDir = "./output"
	}
	outDocs := make([]map[string]any, 0, len(outputs))
	for _, out := range outputs {
		outDocs = append(outDocs, map[string]any{"file_type": out.FileType, "directory": outputDir})
	}
	generator := map[string]any{"type": "system-default", "version": "local-mvp"}
	if v := generators["job_order"]; v != "" {
		generator["type"] = "configured"
		generator["version"] = v
	}
	return map[string]any{
		"veriproc_meta": map[string]any{
			"schema_version":    "veriproc.joborder/v1",
			"task_id":           run.TaskID,
			"retry_index":       run.RetryIndex,
			"run_ref":           fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex),
			"station_id":        rev.StationID,
			"station_revision":  rev.RevisionID,
			"generator_type":    generator["type"],
			"generator_version": generator["version"],
			"manifest_path":     manifestPath,
		},
		"order":   map[string]any{"start": task.WindowStart.UTC().Format(time.RFC3339Nano), "end": task.WindowEnd.UTC().Format(time.RFC3339Nano)},
		"inputs":  inputs,
		"outputs": outDocs,
	}
}

// jobOrderReservedFields lists top-level fields in the joborder document that
// the system generates and that must not be overridden via joborder.include.
var jobOrderReservedFields = map[string]bool{
	"schema_version": true,
	"joborder_id":    true,
	"task_id":        true,
	"retry_index":    true,
	"run_ref":        true,
	"station":        true,
	"generator":      true,
	"manifest":       true,
	"veriproc_meta":  true,
	"order":          true,
	"inputs":         true,
	"outputs":        true,
}

// mergeJobOrderInclude deep-merges the resolved include subtree into doc.
// Top-level reserved fields must not appear in include.
func mergeJobOrderInclude(doc map[string]any, include map[string]any) error {
	for k, v := range include {
		if jobOrderReservedFields[k] {
			return fmt.Errorf("joborder.include attempts to override reserved field %q", k)
		}
		existing, ok := doc[k]
		if !ok {
			doc[k] = v
			continue
		}
		// Deep-merge mappings; replace scalars/slices directly.
		existingMap, em := existing.(map[string]any)
		incomingMap, im := v.(map[string]any)
		if em && im {
			if err := mergeJobOrderInclude(existingMap, incomingMap); err != nil {
				return err
			}
		} else {
			doc[k] = v
		}
	}
	return nil
}

// renderJobOrder encodes doc to the specified format.
func renderJobOrder(doc map[string]any, format string) ([]byte, error) {
	switch format {
	case "yaml", "":
		return yaml.Marshal(doc)
	case "json":
		return json.MarshalIndent(doc, "", "  ")
	case "toml":
		var buf strings.Builder
		enc := toml.NewEncoder(&buf)
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
		return []byte(buf.String()), nil
	default:
		return nil, fmt.Errorf("unsupported joborder format %q", format)
	}
}

func (s *Service) buildPublicationRecords(ctx context.Context, run *store.RunRecord, artifacts []*store.ArtifactRecord) ([]*store.PublicationRecord, error) {
	rev, err := s.store.Stations().Get(ctx, run.StationRevisionID)
	if err != nil {
		return nil, err
	}
	if rev.DeclaredOutputs == "" {
		return nil, nil
	}
	var outDefs []stations.OutputDefinition
	if err := json.Unmarshal([]byte(rev.DeclaredOutputs), &outDefs); err != nil {
		return nil, fmt.Errorf("parse declared outputs: %w", err)
	}
	// Index publish config by file_type for O(1) lookup per artifact.
	publishByType := map[string]*stations.OutputPublish{}
	for _, od := range outDefs {
		if od.Publish != nil {
			publishByType[od.FileType] = od.Publish
		}
	}
	if len(publishByType) == 0 {
		return nil, nil
	}
	records := make([]*store.PublicationRecord, 0, len(artifacts))
	for _, art := range artifacts {
		pub := publishByType[art.FileType]
		if pub == nil {
			continue
		}
		if s.rollingArchives[pub.RollingArchive] == "" {
			return nil, fmt.Errorf("publication archive %q is not configured", pub.RollingArchive)
		}
		subpath, err := cleanPublicationSubpath(pub.TargetSubpath)
		if err != nil {
			return nil, err
		}
		name := filepath.Base(art.Path)
		targetPath := name
		if subpath != "" {
			targetPath = filepath.ToSlash(filepath.Join(subpath, name))
		}
		mode := pub.Mode
		if mode == "" {
			mode = "copy"
		}
		if err := fileops.PublishObject(art.Path, filepath.Join(s.rollingArchives[pub.RollingArchive], targetPath), mode); err != nil {
			return nil, err
		}
		now := s.clock().UTC()
		records = append(records, &store.PublicationRecord{PublicationID: "pub-" + sha12(run.RunID+":"+pub.RollingArchive+":"+name), ArtifactID: art.ArtifactID, ProducingRunID: run.RunID, ArchiveID: pub.RollingArchive, TargetPath: targetPath, ObjectKind: art.ObjectKind, PublicationMode: mode, PublicationState: store.PublicationStatePublished, Size: art.Size, Checksum: art.Checksum, ChecksumAlgo: art.ChecksumAlgo, ChecksumSource: art.ChecksumSource, CreatedAt: now, PublishedAt: nullTime(now)})
	}
	return records, nil
}

func cleanPublicationSubpath(subpath string) (string, error) {
	subpath = filepath.ToSlash(strings.TrimSpace(subpath))
	if subpath == "" || subpath == "." {
		return "", nil
	}
	if filepath.IsAbs(subpath) || strings.HasPrefix(subpath, "/") {
		return "", fmt.Errorf("publication target_subpath %q must be relative", subpath)
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(subpath)))
	if clean == "." {
		return "", nil
	}
	if clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return "", fmt.Errorf("publication target_subpath %q escapes archive root", subpath)
	}
	return clean, nil
}

type downstreamPlan struct {
	Tasks            []*store.TaskRecord
	SplitGroups      []taskOutSplitGroupInit
	TaskOutArtifact  *store.ArtifactRecord
	ProvenanceReason string
}

type taskOutSplitGroupInit struct {
	GroupID         string
	Label           string
	Description     string
	ExpectedMembers int
	Close           bool
}

type taskOutDescriptor struct {
	SchemaVersion string                  `yaml:"schema_version"`
	SplitGroups   []taskOutSplitGroupYAML `yaml:"split_groups"`
	Downstream    []taskOutDownstreamYAML `yaml:"downstream"`
}

type taskOutSplitGroupYAML struct {
	GroupID              string `yaml:"group_id"`
	Mode                 string `yaml:"mode"`
	AggregationStationID string `yaml:"aggregation_station_id"`
	ExpectedMembers      int    `yaml:"expected_members"`
	Closure              string `yaml:"closure"`
	Label                string `yaml:"label"`
	Description          string `yaml:"description"`
}

type taskOutDownstreamYAML struct {
	Key          string        `yaml:"key"`
	StationID    string        `yaml:"station_id"`
	Window       taskOutWindow `yaml:"window"`
	SplitGroupID string        `yaml:"split_group_id"`
	Role         string        `yaml:"role"`
}

type taskOutWindow struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
}

func (s *Service) buildDownstreamPlan(ctx context.Context, run *store.RunRecord, parent *store.TaskRecord, canonicality string) (*downstreamPlan, error) {
	rev, err := s.store.Stations().Get(ctx, run.StationRevisionID)
	if err != nil {
		return nil, err
	}
	var routes []stations.DownstreamTarget
	if rev.DeclaredDownstream != "" {
		if err := json.Unmarshal([]byte(rev.DeclaredDownstream), &routes); err != nil {
			return nil, fmt.Errorf("parse downstream: %w", err)
		}
	}

	taskOut, taskOutArtifact, hasTaskOut, err := s.readTaskOutDescriptor(run)
	if err != nil {
		return nil, err
	}

	// Build the history entry for the current (parent) run.
	parentRunRef := fmt.Sprintf("%s/r%d", parent.TaskID, run.RetryIndex)
	parentEntry := map[string]any{
		"station_id":   rev.StationID,
		"run_ref":      parentRunRef,
		"completed_at": s.clock().UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"summary":      "station " + rev.StationName + " completed",
	}

	// Extract history already recorded in the parent task's routing content,
	// then append the current parent run so the child carries the full chain.
	var ancestorHistory []any
	if len(parent.RoutingContent) > 0 {
		var rc map[string]any
		if jerr := json.Unmarshal(parent.RoutingContent, &rc); jerr == nil {
			if h, ok := rc["history"].([]any); ok {
				ancestorHistory = h
			}
		}
	}
	history := append(ancestorHistory, parentEntry) //nolint:gocritic

	if hasTaskOut {
		children, groups, err := s.buildTaskOutDownstreamTasks(ctx, run, parent, taskOut, routes, parentRunRef, history)
		if err != nil {
			return nil, err
		}
		return &downstreamPlan{Tasks: children, SplitGroups: groups, TaskOutArtifact: taskOutArtifact, ProvenanceReason: "task_out_descriptor"}, nil
	}

	children := make([]*store.TaskRecord, 0, len(routes))
	baseCreated := s.clock().UTC()
	for idx, route := range routes {
		// "fan_in" targets are triggered by the group-complete notifier, and
		// "task_out" targets require an algorithm-produced task-out.yaml.
		if route.Mode == "fan_in" || route.Mode == "task_out" {
			continue
		}
		resolved, err := s.resolver.Resolve(ctx, route.StationID)
		if err != nil {
			return nil, fmt.Errorf("resolve downstream: %w", err)
		}
		created := baseCreated.Add(time.Duration(idx) * time.Microsecond)
		hashSeed := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", run.RunID, resolved.StationID, idx)))
		hex6 := hex.EncodeToString(hashSeed[:3])
		taskIDTimestamp := created
		if s.naming.TaskIDTimestamp == policy.TaskIDTimestampStart {
			taskIDTimestamp = parent.WindowStart.UTC()
		}
		taskID := policy.GenerateTaskID(resolved.StationID, taskIDTimestamp, hex6)
		routing := map[string]any{
			"schema_version": parent.SchemaVersion,
			"destination":    map[string]any{"station_id": resolved.StationID},
			"window":         map[string]any{"start": parent.WindowStart.UTC().Format(time.RFC3339Nano), "end": parent.WindowEnd.UTC().Format(time.RFC3339Nano)},
			"force":          false,
			"parent":         map[string]any{"run_ref": parentRunRef},
			"history":        history,
		}
		raw, err := canonjson.Marshal(routing)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		children = append(children, &store.TaskRecord{
			TaskID:               taskID,
			SchemaVersion:        parent.SchemaVersion,
			DestinationStationID: resolved.StationID,
			WindowStart:          parent.WindowStart,
			WindowEnd:            parent.WindowEnd,
			ParentTaskID:         parent.TaskID,
			ParentRunRetryIndex:  sql.NullInt64{Int64: int64(run.RetryIndex), Valid: true},
			RoutingContent:       raw,
			RoutingContentHash:   hex.EncodeToString(sum[:]),
			SubmissionOrigin:     "backend",
			State:                "accepted",
			CreatedAt:            created,
		})
	}
	return &downstreamPlan{Tasks: children, ProvenanceReason: "station_default_downstream"}, nil
}

func (s *Service) readTaskOutDescriptor(run *store.RunRecord) (*taskOutDescriptor, *store.ArtifactRecord, bool, error) {
	paths := []string{
		filepath.Join(run.WorkingRoot, "task-out.yaml"),
		filepath.Join(run.WorkingRoot, "output", "task-out.yaml"),
	}
	var path string
	for _, candidate := range paths {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			path = candidate
			break
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, nil, false, fmt.Errorf("stat task-out.yaml: %w", err)
		}
	}
	if path == "" {
		return nil, nil, false, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read task-out.yaml: %w", err)
	}
	var desc taskOutDescriptor
	if err := yaml.Unmarshal(b, &desc); err != nil {
		return nil, nil, false, fmt.Errorf("parse task-out.yaml: %w", err)
	}
	if desc.SchemaVersion != "" && desc.SchemaVersion != "veriproc.task-out/v1" {
		return nil, nil, false, fmt.Errorf("task-out.yaml schema_version %q unsupported", desc.SchemaVersion)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("stat task-out.yaml: %w", err)
	}
	checksum, algo, source := availableChecksum(path, s.integrity)
	artifact := &store.ArtifactRecord{
		ArtifactID:       "art-" + sha12(run.RunID+":task-out"),
		ProducingRunID:   run.RunID,
		LogicalType:      "task_out",
		ObjectKind:       store.ObjectKindRegularFile,
		FileType:         "task-out.yaml",
		Path:             path,
		Size:             info.Size(),
		Checksum:         checksum,
		ChecksumAlgo:     algo,
		ChecksumSource:   source,
		ValidationStatus: "validated",
		Availability:     "available",
		CreatedAt:        s.clock().UTC(),
	}
	return &desc, artifact, true, nil
}

func (s *Service) buildTaskOutDownstreamTasks(ctx context.Context, run *store.RunRecord, parent *store.TaskRecord, desc *taskOutDescriptor, routes []stations.DownstreamTarget, parentRunRef string, history []any) ([]*store.TaskRecord, []taskOutSplitGroupInit, error) {
	if len(desc.Downstream) == 0 {
		return nil, nil, fmt.Errorf("task-out.yaml must declare at least one downstream task")
	}
	allowedTaskOut := map[string]bool{}
	allowedFanIn := map[string]bool{}
	for _, route := range routes {
		switch route.Mode {
		case "task_out":
			allowedTaskOut[route.StationID] = true
		case "fan_in":
			allowedFanIn[route.StationID] = true
		case "", "normal":
			allowedTaskOut[route.StationID] = true
		}
	}

	groupsByID := map[string]taskOutSplitGroupYAML{}
	for _, group := range desc.SplitGroups {
		group.GroupID = strings.TrimSpace(group.GroupID)
		group.AggregationStationID = strings.TrimSpace(group.AggregationStationID)
		group.Mode = strings.TrimSpace(group.Mode)
		group.Closure = strings.TrimSpace(group.Closure)
		if group.GroupID == "" {
			return nil, nil, fmt.Errorf("task-out.yaml split group missing group_id")
		}
		if group.Mode != "aggregation" {
			return nil, nil, fmt.Errorf("task-out.yaml split group %s mode %q unsupported", group.GroupID, group.Mode)
		}
		if group.ExpectedMembers <= 0 {
			return nil, nil, fmt.Errorf("task-out.yaml split group %s expected_members must be positive", group.GroupID)
		}
		if group.AggregationStationID == "" || !allowedFanIn[group.AggregationStationID] {
			return nil, nil, fmt.Errorf("task-out.yaml split group %s aggregation target %q is not declared as fan_in downstream", group.GroupID, group.AggregationStationID)
		}
		if _, err := s.resolver.Resolve(ctx, group.AggregationStationID); err != nil {
			return nil, nil, fmt.Errorf("resolve aggregation target %s: %w", group.AggregationStationID, err)
		}
		groupsByID[group.GroupID] = group
	}

	seen := map[string]bool{}
	groupMemberCounts := map[string]int{}
	children := make([]*store.TaskRecord, 0, len(desc.Downstream))
	baseCreated := s.clock().UTC()
	for idx, entry := range desc.Downstream {
		entry.Key = strings.TrimSpace(entry.Key)
		entry.StationID = strings.TrimSpace(entry.StationID)
		entry.SplitGroupID = strings.TrimSpace(entry.SplitGroupID)
		entry.Role = strings.TrimSpace(entry.Role)
		if entry.StationID == "" {
			return nil, nil, fmt.Errorf("task-out.yaml downstream[%d] missing station_id", idx)
		}
		if !allowedTaskOut[entry.StationID] {
			return nil, nil, fmt.Errorf("task-out.yaml downstream[%d] target %q is not declared for task_out routing", idx, entry.StationID)
		}
		if entry.Key == "" {
			entry.Key = fmt.Sprintf("%03d", idx)
		}
		start, err := parseTaskOutTime(entry.Window.Start)
		if err != nil {
			return nil, nil, fmt.Errorf("task-out.yaml downstream[%d] window.start: %w", idx, err)
		}
		end, err := parseTaskOutTime(entry.Window.End)
		if err != nil {
			return nil, nil, fmt.Errorf("task-out.yaml downstream[%d] window.end: %w", idx, err)
		}
		if !end.After(start) {
			return nil, nil, fmt.Errorf("task-out.yaml downstream[%d] end must be after start", idx)
		}
		if entry.SplitGroupID != "" {
			if _, ok := groupsByID[entry.SplitGroupID]; !ok {
				return nil, nil, fmt.Errorf("task-out.yaml downstream[%d] references undeclared split_group_id %q", idx, entry.SplitGroupID)
			}
			groupMemberCounts[entry.SplitGroupID]++
		}
		dupKey := entry.StationID + "|" + start.Format(time.RFC3339Nano) + "|" + end.Format(time.RFC3339Nano) + "|" + entry.SplitGroupID + "|" + entry.Key
		if seen[dupKey] {
			return nil, nil, fmt.Errorf("task-out.yaml duplicate downstream entry %q", dupKey)
		}
		seen[dupKey] = true

		resolved, err := s.resolver.Resolve(ctx, entry.StationID)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve task-out downstream %s: %w", entry.StationID, err)
		}
		created := baseCreated.Add(time.Duration(idx) * time.Microsecond)
		hashSeed := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s|%s", parent.TaskID, entry.Key, resolved.StationID, start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))))
		hex6 := hex.EncodeToString(hashSeed[:3])
		taskIDTimestamp := created
		if s.naming.TaskIDTimestamp == policy.TaskIDTimestampStart {
			taskIDTimestamp = start.UTC()
		}
		taskID := policy.GenerateTaskID(resolved.StationID, taskIDTimestamp, hex6)
		routing := map[string]any{
			"schema_version": parent.SchemaVersion,
			"destination":    map[string]any{"station_id": resolved.StationID},
			"window":         map[string]any{"start": start.UTC().Format(time.RFC3339Nano), "end": end.UTC().Format(time.RFC3339Nano)},
			"force":          false,
			"parent":         map[string]any{"run_ref": parentRunRef},
			"history":        history,
			"routing_source": "task-out.yaml",
		}
		if entry.SplitGroupID != "" {
			routing["split_group_id"] = entry.SplitGroupID
		}
		raw, err := canonjson.Marshal(routing)
		if err != nil {
			return nil, nil, err
		}
		sum := sha256.Sum256(raw)
		children = append(children, &store.TaskRecord{
			TaskID:               taskID,
			SchemaVersion:        parent.SchemaVersion,
			DestinationStationID: resolved.StationID,
			WindowStart:          start.UTC(),
			WindowEnd:            end.UTC(),
			ParentTaskID:         parent.TaskID,
			ParentRunRetryIndex:  sql.NullInt64{Int64: int64(run.RetryIndex), Valid: true},
			SplitGroupID:         entry.SplitGroupID,
			RoutingContent:       raw,
			RoutingContentHash:   hex.EncodeToString(sum[:]),
			SubmissionOrigin:     "backend",
			State:                "accepted",
			CreatedAt:            created,
		})
	}

	groups := make([]taskOutSplitGroupInit, 0, len(groupsByID))
	for groupID, group := range groupsByID {
		if groupMemberCounts[groupID] != group.ExpectedMembers {
			return nil, nil, fmt.Errorf("task-out.yaml split group %s expected %d members, got %d downstream declarations", groupID, group.ExpectedMembers, groupMemberCounts[groupID])
		}
		groups = append(groups, taskOutSplitGroupInit{
			GroupID:         groupID,
			Label:           group.Label,
			Description:     group.Description,
			ExpectedMembers: group.ExpectedMembers,
			Close:           group.Closure == "closed",
		})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].GroupID < groups[j].GroupID })
	return children, groups, nil
}

func parseTaskOutTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("timestamp is empty")
	}
	for _, layout := range []string{time.RFC3339Nano, "20060102T150405", "20060102T150405000"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}
