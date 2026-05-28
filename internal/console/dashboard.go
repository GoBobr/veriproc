package console

import (
	"context"
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
// Ordering rule (Spec §8.4.2):
//   1. running, oldest first
//   2. completed (within visibility timeout), newest first
//   3. queued, oldest first
//   4. failed/cancelled (not hidden), newest first
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
	}

	var running, completed, queued, failed []DashboardSlot
	for _, s := range summary.Slots {
		// Filter out hidden failed runs (console-local acknowledgment).
		key := HiddenRunKey{InstanceID: instanceID, StationID: summary.StationID, TaskID: s.TaskID, RetryIndex: s.RetryIndex}
		if _, isHidden := hidden[key]; isHidden && (s.Kind == "failed" || s.Kind == "cancelled") {
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

	// Sort each pool deterministically. Without a created_at field we order
	// by retry index (running/queued: oldest first → lower index first) and
	// by terminal time (completed/failed: newest first).
	sort.SliceStable(running, func(i, j int) bool { return running[i].RetryIndex < running[j].RetryIndex })
	sort.SliceStable(queued, func(i, j int) bool { return queued[i].RetryIndex < queued[j].RetryIndex })
	sort.SliceStable(completed, func(i, j int) bool {
		return slotTime(completed[i]).After(slotTime(completed[j]))
	})
	sort.SliceStable(failed, func(i, j int) bool {
		return slotTime(failed[i]).After(slotTime(failed[j]))
	})

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

func slotTime(s DashboardSlot) time.Time {
	if s.TerminalAt != nil {
		return *s.TerminalAt
	}
	return time.Time{}
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
		row := ExpandStationRow(s, hidden, instanceID, g.cfg.UI.VisibleSlotCount, g.cfg.UI.CompletedVisibility, now)
		rows = append(rows, row)
	}
	view.Stations = rows
	if !summary.Since.IsZero() {
		view.Since = summary.Since
	}
	return view
}
