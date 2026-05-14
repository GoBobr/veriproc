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

	logArtifact, err := s.collectRunLog(run)
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

	now := s.clock().UTC()
	publicationRecords, err := s.buildPublicationRecords(ctx, run, outputArtifacts)
	if err != nil {
		_ = s.MarkFailedAndLog(ctx, runID, err.Error())
		return nil, err
	}
	downstreamTasks, err := s.buildDownstreamTasks(ctx, run, task, canonicality)
	if err != nil {
		_ = s.MarkFailedAndLog(ctx, runID, err.Error())
		return nil, err
	}

	err = s.store.InTx(ctx, func(tx *store.Tx) error {
		if err := tx.Artifacts().Insert(ctx, logArtifact); err != nil {
			return err
		}
		for _, oa := range outputArtifacts {
			if err := tx.Artifacts().Insert(ctx, oa); err != nil {
				return err
			}
		}
		for _, pub := range publicationRecords {
			if err := tx.Publications().Insert(ctx, pub); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
		}
		for _, child := range downstreamTasks {
			if err := tx.Tasks().Insert(ctx, child); err != nil && !errors.Is(err, store.ErrConflict) {
				return err
			}
			link := &store.ProvenanceLink{LinkID: "prov-" + sha12(run.RunID+":"+child.TaskID), SourceType: "run", SourceID: run.RunID, TargetType: "task", TargetID: child.TaskID, RelationshipType: "produced_downstream", Role: "parent", Reason: "station_default_downstream", CreatedAt: now}
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
		}
		return nil
	})
	if err != nil {
		return nil, err
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

	// Build the base joborder document.
	doc := jobOrderDocument(run, task, rev, manifest, outputs, filepath.ToSlash(manifestPath), s.generators, s.jobOrderPaths)

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
	body, err := renderJobOrder(doc, joCfg.Format)
	if err != nil {
		return "", nil, fmt.Errorf("render joborder (%s): %w", joCfg.Format, err)
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

func (s *Service) collectRunLog(run *store.RunRecord) (*store.ArtifactRecord, error) {
	dir := filepath.Join(run.WorkingRoot, "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "run.log")
	body, err := os.ReadFile(path)
	if err != nil {
		body = []byte(fmt.Sprintf("run=%s task=%s state=finalizing executor=%s\n",
			run.RunID, run.TaskID, s.exec.Type()))
		if err := os.WriteFile(path, body, 0o644); err != nil {
			return nil, err
		}
	}
	return &store.ArtifactRecord{
		ArtifactID:       "art-" + sha12(run.RunID+":log"),
		ProducingRunID:   run.RunID,
		LogicalType:      "log",
		ObjectKind:       store.ObjectKindRegularFile,
		FileType:         "RUN_LOG",
		Path:             path,
		Size:             int64(len(body)),
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
	if logArt, werr := s.collectRunLog(run); werr == nil {
		logArt.ValidationStatus = "failed"
		_ = s.store.Artifacts().Insert(ctx, logArt)
	}
	return s.store.Tasks().SetState(ctx, run.TaskID, "failed", reason)
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
		EntryID                  string         `yaml:"entry_id"`
		FileType                 string         `yaml:"file_type"`
		Category                 string         `yaml:"category,omitempty"`
		Optional                 bool           `yaml:"optional"`
		Present                  bool           `yaml:"present"`
		Path                     string         `yaml:"path,omitempty"`
		ObjectKind               string         `yaml:"object_kind,omitempty"`
		Size                     int64          `yaml:"size,omitempty"`
		SourceArchiveID          string         `yaml:"source_archive_id,omitempty"`
		FolderPriority           int            `yaml:"folder_priority,omitempty"`
		EffectiveFilenamePattern string         `yaml:"effective_filename_pattern,omitempty"`
		FilenameComponents       string         `yaml:"filename_components,omitempty"`
		WindowMatch              string         `yaml:"window_match,omitempty"`
		IntervalGroupKey         string         `yaml:"interval_group_key,omitempty"`
		Discriminator            string         `yaml:"discriminator,omitempty"`
		WinnerMetadata           string         `yaml:"winner_metadata,omitempty"`
		SelectionReason          string         `yaml:"selection_reason,omitempty"`
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
		"schema_version": "veriproc.joborder/v1",
		"joborder_id":    "joborder-" + run.RunID,
		"task_id":        run.TaskID,
		"retry_index":    run.RetryIndex,
		"run_ref":        fmt.Sprintf("%s/r%d", run.TaskID, run.RetryIndex),
		"station":        map[string]any{"station_id": rev.StationID, "station_name": rev.StationName, "revision": rev.RevisionID},
		"generator":      generator,
		"order":          map[string]any{"start": task.WindowStart.UTC().Format(time.RFC3339Nano), "end": task.WindowEnd.UTC().Format(time.RFC3339Nano)},
		"manifest":       map[string]any{"path": manifestPath},
		"inputs":         inputs,
		"outputs":        outDocs,
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
	"order":          true,
	"manifest":       true,
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
	if err != nil || rev.PublicationPolicy == "" {
		return nil, err
	}
	var policy stations.PublicationPolicy
	if err := json.Unmarshal([]byte(rev.PublicationPolicy), &policy); err != nil {
		return nil, fmt.Errorf("parse publication policy: %w", err)
	}
	if !policy.Enabled {
		return nil, nil
	}
	if s.rollingArchives[policy.ArchiveID] == "" {
		return nil, fmt.Errorf("publication archive %q is not configured", policy.ArchiveID)
	}
	subpath, err := cleanPublicationSubpath(policy.TargetSubpath)
	if err != nil {
		return nil, err
	}
	selected := map[string]bool{}
	for _, name := range policy.Outputs {
		selected[name] = true
	}
	records := make([]*store.PublicationRecord, 0, len(artifacts))
	for _, art := range artifacts {
		name := filepath.Base(art.Path)
		if len(selected) > 0 && !selected[name] && !selected[art.FileType] && !matchesAnyGlob(name, policy.Outputs) {
			continue
		}
		targetPath := name
		if subpath != "" {
			targetPath = filepath.ToSlash(filepath.Join(subpath, name))
		}
		mode := policy.Mode
		if mode == "" {
			mode = "copy"
		}
		if err := fileops.PublishObject(art.Path, filepath.Join(s.rollingArchives[policy.ArchiveID], targetPath), mode); err != nil {
			return nil, err
		}
		now := s.clock().UTC()
		records = append(records, &store.PublicationRecord{PublicationID: "pub-" + sha12(run.RunID+":"+policy.ArchiveID+":"+name), ArtifactID: art.ArtifactID, ProducingRunID: run.RunID, ArchiveID: policy.ArchiveID, TargetPath: targetPath, ObjectKind: art.ObjectKind, PublicationMode: mode, PublicationState: store.PublicationStatePublished, Size: art.Size, Checksum: art.Checksum, ChecksumAlgo: art.ChecksumAlgo, ChecksumSource: art.ChecksumSource, CreatedAt: now, PublishedAt: nullTime(now)})
	}
	return records, nil
}

// matchesAnyGlob reports whether name matches any of the glob patterns.
// Patterns that are not valid globs are treated as literal strings.
func matchesAnyGlob(name string, patterns []string) bool {
	for _, p := range patterns {
		if ok, err := filepath.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
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

func (s *Service) buildDownstreamTasks(ctx context.Context, run *store.RunRecord, parent *store.TaskRecord, canonicality string) ([]*store.TaskRecord, error) {
	if canonicality != "canonical" {
		return nil, nil
	}
	rev, err := s.store.Stations().Get(ctx, run.StationRevisionID)
	if err != nil || rev.DeclaredDownstream == "" {
		return nil, err
	}
	var routes []stations.DownstreamTarget
	if err := json.Unmarshal([]byte(rev.DeclaredDownstream), &routes); err != nil {
		return nil, fmt.Errorf("parse downstream: %w", err)
	}
	children := make([]*store.TaskRecord, 0, len(routes))
	baseCreated := s.clock().UTC()
	for idx, route := range routes {
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
			"parent":         map[string]any{"task_id": parent.TaskID, "retry_index": run.RetryIndex},
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
	return children, nil
}
