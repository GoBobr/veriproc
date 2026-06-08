package stations

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/eum/veriproc/internal/store"
)

// defaultCompletedVisibilityTimeout is 0, meaning no cutoff: the upstream
// returns all completed slots and lets the console gateway decide what to
// display based on its own completed_visibility_timeout setting.
const defaultCompletedVisibilityTimeout = time.Duration(0)

// Service provides operator-facing station status and control operations.
type Service struct {
	store                      *store.Store
	clock                      func() time.Time
	completedVisibilityTimeout time.Duration
	// stationOrder is the display ordering applied by List/Summary. When nil
	// the DB default (station_id alphabetical) is used.
	stationOrder               []string
}

type SummaryCounts struct {
	Success int `json:"success"`
	Failure int `json:"failure"`
}

type Slot struct {
	Kind       string     `json:"kind"`
	RunID      string     `json:"run_id,omitempty"`
	TaskID     string     `json:"task_id,omitempty"`
	RetryIndex int        `json:"retry_index,omitempty"`
	State      string     `json:"state,omitempty"`
	TerminalAt *time.Time `json:"terminal_at,omitempty"`
}

type StationView struct {
	StationID    string        `json:"station_id"`
	StationName  string        `json:"station_name,omitempty"`
	Paused       bool          `json:"paused"`
	RunningCount int           `json:"running_count,omitempty"`
	QueuedCount  int           `json:"queued_count,omitempty"`
	Counts       SummaryCounts `json:"counts,omitempty"`
	Slots        []Slot        `json:"slots,omitempty"`
	LastRefresh  time.Time     `json:"last_refresh,omitempty"`
	// Downstream lists the station IDs that this station triggers automatically.
	Downstream   []string      `json:"downstream,omitempty"`
	// DeclaredInputs are the input file_types declared by the station.
	DeclaredInputs []string `json:"declared_inputs,omitempty"`
	// DeclaredOutputs are the output file_types declared by the station.
	DeclaredOutputs []string `json:"declared_outputs,omitempty"`
}

func NewService(st *store.Store, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: st, clock: now, completedVisibilityTimeout: defaultCompletedVisibilityTimeout}
}

// SetCompletedVisibilityTimeout overrides the completed-slot age cutoff
// applied when building the station summary. A value of 0 (the default)
// means no cutoff: all completed runs within the slot list limit are returned.
// Negative values are treated as 0 (no cutoff).
func (s *Service) SetCompletedVisibilityTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	s.completedVisibilityTimeout = d
}

// SetStationOrder configures the display order for List and Summary.
// Stations not present in order are appended after the listed ones, sorted
// alphabetically by station_id. An empty slice restores the default
// (station_id alphabetical from the store).
func (s *Service) SetStationOrder(order []string) {
	s.stationOrder = append([]string(nil), order...)
}

// applyOrder reorders views in-place according to s.stationOrder.
func (s *Service) applyOrder(views []StationView) {
	if len(s.stationOrder) == 0 {
		return
	}
	rank := make(map[string]int, len(s.stationOrder))
	for i, id := range s.stationOrder {
		rank[id] = i
	}
	maxRank := len(s.stationOrder)
	sort.SliceStable(views, func(i, j int) bool {
		ri, oki := rank[views[i].StationID]
		rj, okj := rank[views[j].StationID]
		if !oki {
			ri = maxRank
		}
		if !okj {
			rj = maxRank
		}
		if ri != rj {
			return ri < rj
		}
		return views[i].StationID < views[j].StationID
	})
}

func (s *Service) List(ctx context.Context) ([]StationView, error) {
	current, err := s.store.Stations().ListCurrent(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]StationView, 0, len(current))
	for _, rec := range current {
		paused, err := s.store.StationControls().IsPaused(ctx, rec.StationID)
		if err != nil {
			return nil, err
		}
		view := StationView{StationID: rec.StationID, StationName: rec.StationName, Paused: paused}
		if rec.DeclaredDownstream != "" {
			view.Downstream = parseDownstreamIDs(rec.DeclaredDownstream)
		}
		if rec.DeclaredInputs != "" {
			view.DeclaredInputs = parseInputOutputTypes(rec.DeclaredInputs, "file_type")
		}
		if rec.DeclaredOutputs != "" {
			view.DeclaredOutputs = parseInputOutputTypes(rec.DeclaredOutputs, "file_type")
		}
		out = append(out, view)
	}
	s.applyOrder(out)
	return out, nil
}

func parseInputOutputTypes(raw string, key string) []string {
	entries := make([]map[string]any, 0)
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil
	}
	types := make([]string, 0, len(entries))
	for _, e := range entries {
		if ft, ok := e[key].(string); ok && ft != "" {
			types = append(types, ft)
		}
	}
	return types
}

// parseDownstreamIDs decodes the declared_downstream JSON and returns the
// station_id of each target. Invalid JSON is silently ignored.
func parseDownstreamIDs(raw string) []string {
	var targets []struct {
		StationID string `json:"station_id"`
	}
	if err := json.Unmarshal([]byte(raw), &targets); err != nil {
		return nil
	}
	ids := make([]string, 0, len(targets))
	for _, t := range targets {
		if t.StationID != "" {
			ids = append(ids, t.StationID)
		}
	}
	return ids
}

func (s *Service) Pause(ctx context.Context, stationID string) (*StationView, error) {
	return s.setPaused(ctx, stationID, true)
}

func (s *Service) Unpause(ctx context.Context, stationID string) (*StationView, error) {
	return s.setPaused(ctx, stationID, false)
}

func (s *Service) setPaused(ctx context.Context, stationID string, paused bool) (*StationView, error) {
	exists, err := s.store.Stations().ExistsStationID(ctx, stationID)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("%w: station_id=%s", ErrUnknownStation, stationID)
	}
	if err := s.store.StationControls().SetPaused(ctx, stationID, paused, s.clock()); err != nil {
		return nil, err
	}
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, st := range list {
		if st.StationID == stationID {
			return &st, nil
		}
	}
	return &StationView{StationID: stationID, Paused: paused}, nil
}

func (s *Service) Summary(ctx context.Context, since time.Time, slotCap int) ([]StationView, error) {
	list, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	now := s.clock()
	for i := range list {
		summary, err := s.summaryForStation(ctx, list[i], since, now, slotCap)
		if err != nil {
			return nil, err
		}
		list[i] = summary
	}
	return list, nil
}

func (s *Service) SummaryStation(ctx context.Context, stationID string, since time.Time, slotCap int) (*StationView, error) {
	list, err := s.Summary(ctx, since, slotCap)
	if err != nil {
		return nil, err
	}
	for _, st := range list {
		if st.StationID == stationID {
			return &st, nil
		}
	}
	return nil, fmt.Errorf("%w: station_id=%s", ErrUnknownStation, stationID)
}

func (s *Service) summaryForStation(ctx context.Context, base StationView, since, now time.Time, slotCap int) (StationView, error) {
	if slotCap <= 0 {
		slotCap = 12
	}
	runsPage, err := s.store.Runs().List(ctx, store.RunListFilter{StationID: base.StationID, Limit: 200})
	if err != nil {
		return base, err
	}
	acceptedPage, err := s.store.Tasks().List(ctx, store.ListFilter{
		DestinationStationID: base.StationID,
		State:                "accepted",
		Limit:                200,
	})
	if err != nil {
		return base, err
	}

	// When completedVisibilityTimeout is 0 there is no cutoff: all completed
	// runs within the fetched page are eligible. When > 0, apply the cutoff.
	var cutoff time.Time
	if s.completedVisibilityTimeout > 0 {
		cutoff = now.Add(-s.completedVisibilityTimeout)
	}
	queuedAcceptedNoRun := 0
	for _, t := range acceptedPage.Items {
		if !t.LatestRetryIndex.Valid {
			queuedAcceptedNoRun++
		}
	}

	running := make([]*store.RunRecord, 0)
	completed := make([]*store.RunRecord, 0)
	queued := make([]*store.RunRecord, 0)
	failed := make([]*store.RunRecord, 0)
	for _, r := range runsPage.Items {
		switch r.State {
		case "running", "finalizing":
			running = append(running, r)
		case "ready", "dispatched":
			queued = append(queued, r)
		case "complete":
			if !cutoff.IsZero() && r.TerminalAt.Valid && r.TerminalAt.Time.UTC().Before(cutoff) {
				// Aged-out per the configured cutoff; exclude from slot list.
				if r.TerminalAt.Valid && !r.TerminalAt.Time.UTC().Before(since) {
					base.Counts.Success++
				}
				continue
			}
			completed = append(completed, r)
			if r.TerminalAt.Valid && !r.TerminalAt.Time.UTC().Before(since) {
				base.Counts.Success++
			}
		case "failed", "cancelled":
			failed = append(failed, r)
			if r.TerminalAt.Valid && !r.TerminalAt.Time.UTC().Before(since) {
				base.Counts.Failure++
			}
		}
	}

	sort.Slice(running, func(i, j int) bool {
		left := running[i].CreatedAt
		if running[i].StartedAt.Valid {
			left = running[i].StartedAt.Time.UTC()
		}
		right := running[j].CreatedAt
		if running[j].StartedAt.Valid {
			right = running[j].StartedAt.Time.UTC()
		}
		return left.Before(right)
	})
	sort.Slice(queued, func(i, j int) bool { return queued[i].CreatedAt.Before(queued[j].CreatedAt) })
	sort.Slice(completed, func(i, j int) bool {
		return completed[i].TerminalAt.Time.UTC().After(completed[j].TerminalAt.Time.UTC())
	})
	sort.Slice(failed, func(i, j int) bool {
		left := failed[i].CreatedAt
		if failed[i].TerminalAt.Valid {
			left = failed[i].TerminalAt.Time.UTC()
		}
		right := failed[j].CreatedAt
		if failed[j].TerminalAt.Valid {
			right = failed[j].TerminalAt.Time.UTC()
		}
		return left.After(right)
	})

	base.RunningCount = len(running)
	base.QueuedCount = queuedAcceptedNoRun + len(queued)
	base.LastRefresh = now

	slots := make([]Slot, 0, slotCap)
	appendSlots := func(kind string, in []*store.RunRecord) {
		for _, r := range in {
			if len(slots) >= slotCap {
				return
			}
			slot := Slot{Kind: kind, RunID: r.RunID, TaskID: r.TaskID, RetryIndex: r.RetryIndex, State: r.State}
			if r.TerminalAt.Valid {
				tm := r.TerminalAt.Time.UTC()
				slot.TerminalAt = &tm
			}
			slots = append(slots, slot)
		}
	}
	appendSlots("running", running)
	appendSlots("completed", completed)
	appendSlots("queued", queued)
	appendSlots("failed", failed)
	base.Slots = slots
	return base, nil
}
