package runs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/eum/veriproc/internal/store"
)

// taskHistoryEntry is one hop in the routing chain embedded in task.yaml.
type taskHistoryEntry struct {
	StationID   string `yaml:"station_id"`
	RunRef      string `yaml:"run_ref"`
	CompletedAt string `yaml:"completed_at,omitempty"`
	Summary     string `yaml:"summary,omitempty"`
}

// taskHistoryDocument is the YAML structure written to task.yaml in the
// working root (Spec §2.6.1). It gives the executing algorithm full context
// about the task it is processing and the processing chain that led to it.
type taskHistoryDocument struct {
	SchemaVersion string `yaml:"schema_version"`
	RunRef        string `yaml:"run_ref"`
	IdempotencyKey string `yaml:"idempotency_key,omitempty"`
	Destination   struct {
		StationID string `yaml:"station_id"`
	} `yaml:"destination"`
	Window struct {
		Start string `yaml:"start"`
		End   string `yaml:"end"`
	} `yaml:"window"`
	CreatedAt string `yaml:"created_at"`
	Priority  string `yaml:"priority,omitempty"`
	Force     bool   `yaml:"force"`
	Parent    *struct {
		RunRef string `yaml:"run_ref"`
	} `yaml:"parent,omitempty"`
	History []taskHistoryEntry `yaml:"history,omitempty"`
}

// writeTaskHistory writes task.yaml to the run working root and returns an
// artifact record for it. The file captures the full routing context (Spec
// §2.6.1) so that the executing algorithm or a human auditor can see exactly
// what task is being processed and how it arrived there.
func (s *Service) writeTaskHistory(ctx context.Context, run *store.RunRecord) (*store.ArtifactRecord, error) {
	task, err := s.store.Tasks().Get(ctx, run.TaskID)
	if err != nil {
		return nil, fmt.Errorf("task history: fetch task: %w", err)
	}

	runRef := fmt.Sprintf("%s/r%d", task.TaskID, run.RetryIndex)

	doc := taskHistoryDocument{
		SchemaVersion: "veriproc.task/v1",
		RunRef:        runRef,
		Force:         task.Force,
		CreatedAt:     task.CreatedAt.UTC().Format(time.RFC3339),
	}
	doc.Destination.StationID = task.DestinationStationID
	doc.Window.Start = task.WindowStart.UTC().Format(time.RFC3339Nano)
	doc.Window.End = task.WindowEnd.UTC().Format(time.RFC3339Nano)
	if task.Priority != "" {
		doc.Priority = task.Priority
	}

	// Parse routing content to extract parent run_ref and history chain.
	if len(task.RoutingContent) > 0 {
		var rc map[string]any
		if jerr := json.Unmarshal(task.RoutingContent, &rc); jerr == nil {
			// Parent run_ref.
			if p, ok := rc["parent"].(map[string]any); ok {
				if pRunRef, ok := p["run_ref"].(string); ok && pRunRef != "" {
					doc.Parent = &struct {
						RunRef string `yaml:"run_ref"`
					}{RunRef: pRunRef}
				}
			}
			// History chain.
			if h, ok := rc["history"].([]any); ok {
				for _, raw := range h {
					m, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					entry := taskHistoryEntry{}
					if v, ok := m["station_id"].(string); ok {
						entry.StationID = v
					}
					if v, ok := m["run_ref"].(string); ok {
						entry.RunRef = v
					}
					if v, ok := m["completed_at"].(string); ok {
						entry.CompletedAt = v
					}
					if v, ok := m["summary"].(string); ok {
						entry.Summary = v
					}
					doc.History = append(doc.History, entry)
				}
			}
		}
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, fmt.Errorf("task history: marshal yaml: %w", err)
	}

	destPath := filepath.Join(run.WorkingRoot, "task.yaml")
	if err := os.WriteFile(destPath, out, 0o644); err != nil {
		return nil, fmt.Errorf("task history: write task.yaml: %w", err)
	}

	now := s.clock().UTC()
	return &store.ArtifactRecord{
		ArtifactID:       "art-" + sha12(run.RunID+":task-history"),
		ProducingRunID:   run.RunID,
		LogicalType:      "task_history",
		ObjectKind:       store.ObjectKindRegularFile,
		FileType:         "TASK_HISTORY",
		Path:             destPath,
		Size:             int64(len(out)),
		ValidationStatus: "validated",
		Availability:     "available",
		CreatedAt:        now,
	}, nil
}
