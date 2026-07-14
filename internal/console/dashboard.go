package console

import (
	"context"
	"net/url"
	"sort"
	"time"
)

// SlotKind enumerates the dashboard slot kinds rendered by the frontend.
type SlotKind string

const (
	SlotEmpty     SlotKind = "empty"
	SlotRunning   SlotKind = "running"
	SlotQueued    SlotKind = "queued"
	SlotCompleted SlotKind = "completed"
	SlotFailed    SlotKind = "failed"
)

// DashboardSlot is the UI-oriented expansion of one execution box on the
// dashboard. Empty slots carry only Kind=empty.
type DashboardSlot struct {
	Kind       SlotKind   `json:"kind"`
	RunID      string     `json:"run_id,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	RetryIndex int        `json:"retry_index"`
	State      string     `json:"state,omitempty"`
	TerminalAt *time.Time `json:"terminal_at,omitempty"`
}

// DashboardStationRow is the expanded UI model for one station row.
type DashboardStationRow struct {
	StationID    string          `json:"station_id"`
	StationName  string          `json:"station_name,omitempty"`
	Paused       bool            `json:"paused"`
	RunningCount int             `json:"running_count"`
	QueuedCount  int             `json:"queued_count"`
	Counts       SummaryCounts   `json:"counts"`
	Slots        []DashboardSlot `json:"slots"`
	Overflow     int             `json:"overflow"`
	LastRefresh  *time.Time      `json:"last_refresh,omitempty"`
	// Downstream lists the station IDs declared as downstream targets in the
	// station definition. Used by the UI to build "Push downstream" actions.
	Downstream   []string        `json:"downstream,omitempty"`
}

// DashboardInstance is the per-instance dashboard view returned to the
// frontend.
type DashboardInstance struct {
	InstanceID  string                `json:"instance_id"`
	Title       string                `json:"title"`
	Status      string                `json:"status"` // ok | degraded
	Error       string                `json:"error,omitempty"`
	Since       time.Time             `json:"since"`
	Stations    []DashboardStationRow `json:"stations"`
	LastRefresh time.Time             `json:"last_refresh"`
}

// ExpandStationRow turns an upstream StationSummary plus the console-local
// hidden-run set into the UI-oriented row representation expected by the
// frontend. The expansion is pure: same inputs → same output.
//
// Ordering rule:
//   1. running, alphabetical by task ID
//   2. completed (within visibility timeout), alphabetical by task ID
//   3. queued, alphabetical by task ID
//   4. failed/cancelled (not hidden), alphabetical by task ID
//   5. empty slots padding to visibleSlotCount
//
// completedVisibility prunes completed slots whose terminal time is older
// than now-completedVisibility.
func ExpandStationRow(
	summary StationSummary,
	hidden map[HiddenRunKey]struct{},
	instanceID string,
	visibleSlotCount int,
	completedVisibility time.Duration,
	now time.Time,
) DashboardStationRow {
	row := DashboardStationRow{
		StationID:    summary.StationID,
		StationName:  summary.StationName,
		Paused:       summary.Paused,
		RunningCount: summary.RunningCount,
		QueuedCount:  summary.QueuedCount,
		Counts:       summary.Counts,
		LastRefresh:  summary.LastRefresh,
		Downstream:   summary.Downstream,
	}

	var running, completed, queued, failed []DashboardSlot
	for _, s := range summary.Slots {
		// Filter out hidden runs (console-local acknowledgment): failed, cancelled, or completed.
		key := HiddenRunKey{InstanceID: instanceID, StationID: summary.StationID, TaskID: s.TaskID, RetryIndex: s.RetryIndex}
		if _, isHidden := hidden[key]; isHidden && (s.Kind == "failed" || s.Kind == "cancelled" || s.Kind == "completed") {
			continue
		}
		slot := DashboardSlot{
			Kind:       SlotKind(s.Kind),
			RunID:      s.RunID,
			TaskID:     s.TaskID,
			RetryIndex: s.RetryIndex,
			State:      s.State,
			TerminalAt: s.TerminalAt,
		}
		switch s.Kind {
		case "running":
			running = append(running, slot)
		case "completed":
			if completedVisibility > 0 && s.TerminalAt != nil && now.Sub(*s.TerminalAt) > completedVisibility {
				// Aged-out; skip.
				continue
			}
			slot.Kind = SlotCompleted
			completed = append(completed, slot)
		case "queued":
			slot.Kind = SlotQueued
			queued = append(queued, slot)
		case "failed", "cancelled":
			slot.Kind = SlotFailed
			failed = append(failed, slot)
		default:
			// Unknown upstream kinds (e.g. paused-down) – treat as empty.
		}
	}

	// Sort each pool alphabetically by TaskID for a stable, predictable
	// display order.
	sort.SliceStable(running, func(i, j int) bool { return running[i].TaskID < running[j].TaskID })
	sort.SliceStable(queued, func(i, j int) bool { return queued[i].TaskID < queued[j].TaskID })
	sort.SliceStable(completed, func(i, j int) bool { return completed[i].TaskID < completed[j].TaskID })
	sort.SliceStable(failed, func(i, j int) bool { return failed[i].TaskID < failed[j].TaskID })

	ordered := make([]DashboardSlot, 0, visibleSlotCount)
	ordered = append(ordered, running...)
	ordered = append(ordered, completed...)
	ordered = append(ordered, queued...)
	ordered = append(ordered, failed...)

	total := len(ordered)
	if total > visibleSlotCount {
		row.Overflow = total - visibleSlotCount
		ordered = ordered[:visibleSlotCount]
	}
	// Pad with empty slots.
	for len(ordered) < visibleSlotCount {
		ordered = append(ordered, DashboardSlot{Kind: SlotEmpty})
	}
	row.Slots = ordered
	return row
}

// BuildInstanceDashboard fetches the upstream station summary and produces
// the UI model. A non-nil error means the gateway could not reach upstream;
// callers may surface a degraded view.
func (g *Gateway) BuildInstanceDashboard(ctx context.Context, instanceID string, since time.Time) DashboardInstance {
	inst, ok := g.instance(instanceID)
	now := g.now()
	view := DashboardInstance{
		InstanceID:  instanceID,
		Title:       "",
		Status:      "ok",
		Since:       since,
		LastRefresh: now,
	}
	if !ok {
		view.Status = "degraded"
		view.Error = "unknown instance"
		return view
	}
	view.Title = inst.cfg.Title

	summary, err := inst.client.StationsSummary(ctx, since)
	if err != nil {
		view.Status = "degraded"
		view.Error = err.Error()
		return view
	}
	rows := make([]DashboardStationRow, 0, len(summary.Items))
	for _, s := range summary.Items {
		hidden, _ := g.db.HiddenForStation(ctx, instanceID, s.StationID)
		// Augment the summary with tasks that failed immediately (no run was
		// ever created for them) so the operator has visibility.
		augmented := g.injectRunlessFailed(ctx, inst, s, since)
		row := ExpandStationRow(augmented, hidden, instanceID, g.cfg.UI.VisibleSlotCount, g.cfg.UI.CompletedVisibility.D, now)
		rows = append(rows, row)
	}
	view.Stations = rows
	if !summary.Since.IsZero() {
		view.Since = summary.Since
	}
	return view
}

// injectRunlessFailed augments a StationSummary with synthetic failed slots
// for tasks that failed before any run was created (latest_retry_index == nil
// in the upstream task record). These tasks never appear in the station
// summary slot list because the summary is run-derived.
//
// The call is best-effort: errors from ListTasks are silently ignored to
// avoid degrading the whole dashboard.
func (g *Gateway) injectRunlessFailed(ctx context.Context, inst *upstreamInstance, summary StationSummary, since time.Time) StationSummary {
	// Build the set of task IDs already represented in the slot list so we
	// don't double-count tasks that have runs.
	knownTasks := make(map[string]struct{}, len(summary.Slots))
	for _, sl := range summary.Slots {
		if sl.TaskID != "" {
			knownTasks[sl.TaskID] = struct{}{}
		}
	}

	q := url.Values{}
	q.Set("station_id", summary.StationID)
	q.Set("state", "failed")
	q.Set("limit", "50")
	tasks, err := inst.client.ListTasks(ctx, q)
	if err != nil {
		return summary // best-effort; upstream list may not be available
	}

	for _, t := range tasks {
		taskID, _ := t["task_id"].(string)
		_, alreadyShown := knownTasks[taskID]
		if taskID == "" || alreadyShown {
			continue // already shown via a run slot
		}
		// Skip if the task has any runs (latest_retry_index != null).
		// JSON null unmarshals as nil in map[string]any; a real value is float64.
		if t["latest_retry_index"] != nil {
			continue
		}
		// Respect the since window using the task's created_at.
		if createdStr, ok := t["created_at"].(string); ok {
			if created, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
				if created.Before(since) {
					continue
				}
			}
		}
		// Check if hidden.
		summary.Slots = append(summary.Slots, SummarySlot{
			Kind:       "failed",
			TaskID:     taskID,
			RetryIndex: 0,
			State:      "failed",
		})
	}
	return summary
}
