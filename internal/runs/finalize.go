package runs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eum/veriproc/internal/canonjson"
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

// writeJobOrder writes joborder.yaml to the working root before the executor
// is invoked. The file captures the run identity and dispatch context so that
// a human or real executor can inspect what was submitted.
func (s *Service) writeJobOrder(ctx context.Context, run *store.RunRecord, path string) (*store.ArtifactRecord, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	task, err := s.store.Tasks().Get(ctx, run.TaskID)
	if err != nil {
		return nil, err
	}
	rev, err := s.store.Stations().Get(ctx, run.StationRevisionID)
	if err != nil {
		return nil, err
	}
	manifest, err := s.store.Manifests().GetByRun(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	manifestPath, err := s.writeManifestExport(run, manifest)
	if err != nil {
		return nil, err
	}
	outputs, err := declaredOutputs(rev)
	if err != nil {
		return nil, err
	}
	body, err := yaml.Marshal(jobOrderDocument(run, task, rev, manifest, outputs, filepath.ToSlash(manifestPath), s))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return nil, err
	}
	return &store.ArtifactRecord{
		ArtifactID:       "art-" + sha12(run.RunID+":joborder"),
		ProducingRunID:   run.RunID,
		LogicalType:      "joborder",
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
		name := out.Name
		if name == "" {
			name = out.FileType
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("mandatory output %s missing at %s", out.FileType, path)
		}
		checksum, algo, source := availableChecksum(path, s.integrity)
		arts = append(arts, &store.ArtifactRecord{
			ArtifactID:       "art-" + sha12(run.RunID+":output:"+name),
			ProducingRunID:   run.RunID,
			LogicalType:      "output",
			FileType:         out.FileType,
			Path:             path,
			Size:             info.Size(),
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
	body, err := yaml.Marshal(map[string]any{"schema_version": "veriproc.manifest/v1", "run_id": run.RunID, "entries": manifest.Entries})
	if err != nil {
		return "", err
	}
	return "./manifest/resolved-inputs.yaml", os.WriteFile(path, body, 0o644)
}

func jobOrderDocument(run *store.RunRecord, task *store.TaskRecord, rev *store.StationRevisionRecord, manifest *store.ManifestRecord, outputs []stations.OutputDefinition, manifestPath string, s *Service) map[string]any {
	inputs := make([]map[string]any, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		item := map[string]any{"file_type": entry.FileType, "category": entry.Category, "files": []string{}}
		if entry.Present && entry.Path != "" {
			item["files"] = []string{"./" + filepath.ToSlash(entry.Path)}
		}
		inputs = append(inputs, item)
	}
	outDocs := make([]map[string]any, 0, len(outputs))
	for _, out := range outputs {
		outDocs = append(outDocs, map[string]any{"file_type": out.FileType, "name": out.Name, "required": out.Required})
	}
	generator := map[string]any{"type": "system-default", "version": "local-mvp"}
	if v := s.generators["job_order"]; v != "" {
		generator["type"] = "configured"
		generator["version"] = v
	}
	return map[string]any{
		"schema_version": "veriproc.joborder/v1",
		"joborder_id":    "joborder-" + run.RunID,
		"task_id":        run.TaskID,
		"run_id":         run.RunID,
		"station":        map[string]any{"station_id": rev.StationID, "revision": rev.RevisionID},
		"generator":      generator,
		"order":          map[string]any{"start": task.WindowStart.UTC().Format(time.RFC3339Nano), "end": task.WindowEnd.UTC().Format(time.RFC3339Nano)},
		"facility":       s.facility,
		"log_level":      "INFO",
		"dyn_params":     map[string]any{"instance_id": s.instanceID},
		"manifest":       map[string]any{"path": manifestPath},
		"inputs":         inputs,
		"outputs":        outDocs,
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
	selected := map[string]bool{}
	for _, name := range policy.Outputs {
		selected[name] = true
	}
	records := make([]*store.PublicationRecord, 0, len(artifacts))
	for _, art := range artifacts {
		name := filepath.Base(art.Path)
		if len(selected) > 0 && !selected[name] && !selected[art.FileType] {
			continue
		}
		targetPath := filepath.Join(rev.StationID, name)
		if err := copyArtifactToArchive(art.Path, filepath.Join(s.rollingArchives[policy.ArchiveID], targetPath)); err != nil {
			return nil, err
		}
		now := s.clock().UTC()
		records = append(records, &store.PublicationRecord{PublicationID: "pub-" + sha12(run.RunID+":"+policy.ArchiveID+":"+name), ArtifactID: art.ArtifactID, ProducingRunID: run.RunID, ArchiveID: policy.ArchiveID, TargetPath: targetPath, PublicationMode: policy.Mode, PublicationState: store.PublicationStatePublished, Size: art.Size, Checksum: art.Checksum, ChecksumAlgo: art.ChecksumAlgo, ChecksumSource: art.ChecksumSource, CreatedAt: now, PublishedAt: nullTime(now)})
	}
	return records, nil
}

func copyArtifactToArchive(src, dst string) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read publication source: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("mkdir publication target: %w", err)
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return fmt.Errorf("write publication target: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("publish rename: %w", err)
	}
	return nil
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
		resolved, err := s.resolver.Resolve(ctx, route.StationID, route.ProcType)
		if err != nil {
			return nil, fmt.Errorf("resolve downstream: %w", err)
		}
		created := baseCreated.Add(time.Duration(idx) * time.Microsecond)
		taskID := policy.GenerateTaskID(s.naming, parent.WindowStart, created)
		routing := map[string]any{"schema_version": parent.SchemaVersion, "destination": map[string]any{"station_id": resolved.StationID}, "window": map[string]any{"start": parent.WindowStart.UTC().Format(time.RFC3339Nano), "end": parent.WindowEnd.UTC().Format(time.RFC3339Nano)}, "force": false, "parent": map[string]any{"task_id": parent.TaskID, "run_id": run.RunID}}
		raw, err := canonjson.Marshal(routing)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		children = append(children, &store.TaskRecord{TaskID: taskID, SchemaVersion: parent.SchemaVersion, DestinationStationID: resolved.StationID, WindowStart: parent.WindowStart, WindowEnd: parent.WindowEnd, ParentTaskID: parent.TaskID, ParentRunID: run.RunID, RoutingContent: raw, RoutingContentHash: hex.EncodeToString(sum[:]), SubmissionOrigin: "backend", State: "accepted", CreatedAt: created})
	}
	return children, nil
}
