// Package stations provides station-revision resolution.
//
// The registry is seeded at startup (from config, station.yaml files, or
// tests). Both the file loader and direct seeding satisfy the Resolver
// interface so the task service is insulated from the source of
// station-revision material.
package stations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/gobobr/veriproc/internal/store"
)

// ErrUnknownStation is returned when the supplied station_id cannot be
// resolved (Spec §5.3.2, §5.7 unknown_station).
var ErrUnknownStation = errors.New("stations: unknown station")

// ErrDuplicateStation is returned when two different revisions are registered
// for the same station_id during one startup load.
var ErrDuplicateStation = errors.New("stations: duplicate station")

// ErrDuplicateStationName is returned when two different stations share the
// same active station_name (operator-readable identity must be unique within
// deployment scope, Spec §3.2 / §3.4).
var ErrDuplicateStationName = errors.New("stations: duplicate station_name")

// Resolver is the interface used by the task service.
type Resolver interface {
	// Resolve returns the station revision for the supplied station_id.
	// station_id is the canonical destination identity; proc_type is no
	// longer accepted (Spec §3.2 / §5.3.2).
	Resolve(ctx context.Context, stationID string) (*store.StationRevisionRecord, error)
}

// JoinCounter is an optional extension to Resolver that counts how many
// upstream stations contribute to a given join target. It is implemented by
// Registry and used by the join wakeup logic in the runs service.
type JoinCounter interface {
	// CountJoinProducers returns the number of registered stations that
	// declare a downstream join route with the supplied join_id to
	// targetStationID.
	CountJoinProducers(joinID, targetStationID string) int
}

// Spec describes one station the registry will serve.
type Spec struct {
	StationID      string
	StationName    string
	ContentHash    string
	SchemaVersion  string
	Execution      Execution
	JobOrder       JobOrderConfig
	Inputs         []InputDefinition
	Outputs        []OutputDefinition
	Downstream     []DownstreamTarget
	RollingFolders map[string][]string
}

// Registry is the in-memory Resolver.
type Registry struct {
	mu     sync.RWMutex
	byID   map[string]*store.StationRevisionRecord
	byName map[string]string // station_name -> station_id (uniqueness)
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:   map[string]*store.StationRevisionRecord{},
		byName: map[string]string{},
	}
}

// Seed registers station revisions both in memory and (best-effort) in the
// store, so foreign-key constraints from runs remain satisfiable. If a
// matching (station_id, content_hash) row already exists the store insert is
// treated as a no-op. Seed enforces active station_name uniqueness within
// deployment scope.
func (r *Registry) Seed(ctx context.Context, s *store.Store, specs ...Spec) error {
	for _, sp := range specs {
		if sp.SchemaVersion == "" {
			sp.SchemaVersion = DefaultSchemaVersion
		}
		if sp.StationName == "" {
			return fmt.Errorf("stations: station_name required for station_id=%s", sp.StationID)
		}
		declaredInputs := ""
		if len(sp.Inputs) > 0 {
			b, _ := json.Marshal(sp.Inputs)
			declaredInputs = string(b)
		}
		declaredOutputs := ""
		if len(sp.Outputs) > 0 {
			b, _ := json.Marshal(sp.Outputs)
			declaredOutputs = string(b)
		}
		declaredDownstream := ""
		if len(sp.Downstream) > 0 {
			b, _ := json.Marshal(sp.Downstream)
			declaredDownstream = string(b)
		}
		rollingFolders := ""
		if len(sp.RollingFolders) > 0 {
			b, _ := json.Marshal(sp.RollingFolders)
			rollingFolders = string(b)
		}
		declaredExecution := ""
		if !sp.Execution.IsZero() {
			b, _ := json.Marshal(sp.Execution)
			declaredExecution = string(b)
		}
		declaredJobOrder := ""
		if sp.JobOrder.Renderer != "" || sp.JobOrder.Format != "" || sp.JobOrder.Name != "" || sp.JobOrder.Paths != "" || sp.JobOrder.TemplateFile != "" || sp.JobOrder.Template != "" || len(sp.JobOrder.Params) > 0 || len(sp.JobOrder.Include) > 0 || sp.JobOrder.Meta != nil {
			b, _ := json.Marshal(sp.JobOrder)
			declaredJobOrder = string(b)
		}
		rec := &store.StationRevisionRecord{
			RevisionID:         fmt.Sprintf("rev-%s-%s", sp.StationID, shortHash(sp.ContentHash)),
			StationID:          sp.StationID,
			StationName:        sp.StationName,
			ContentHash:        sp.ContentHash,
			SchemaVersion:      sp.SchemaVersion,
			DeclaredInputs:     declaredInputs,
			DeclaredOutputs:    declaredOutputs,
			DeclaredDownstream: declaredDownstream,
			RollingFolders:     rollingFolders,
			DeclaredExecution:  declaredExecution,
			DeclaredJobOrder:   declaredJobOrder,
		}
		r.mu.Lock()
		if existing, ok := r.byID[sp.StationID]; ok {
			if existing.ContentHash == rec.ContentHash && existing.SchemaVersion == rec.SchemaVersion && existing.StationName == rec.StationName {
				r.mu.Unlock()
				continue
			}
			r.mu.Unlock()
			return fmt.Errorf("%w: station_id=%s", ErrDuplicateStation, sp.StationID)
		}
		if other, ok := r.byName[sp.StationName]; ok && other != sp.StationID {
			r.mu.Unlock()
			return fmt.Errorf("%w: station_name=%q used by station_id=%s and %s",
				ErrDuplicateStationName, sp.StationName, other, sp.StationID)
		}
		r.mu.Unlock()
		if s != nil {
			if err := s.Stations().Upsert(ctx, rec); err != nil {
				return fmt.Errorf("seed station %s: %w", sp.StationID, err)
			}
		}
		r.mu.Lock()
		r.byID[sp.StationID] = rec
		r.byName[sp.StationName] = sp.StationID
		r.mu.Unlock()
	}
	return nil
}

// Resolve implements Resolver.
func (r *Registry) Resolve(_ context.Context, stationID string) (*store.StationRevisionRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if stationID == "" {
		return nil, fmt.Errorf("%w: empty station_id", ErrUnknownStation)
	}
	rec, ok := r.byID[stationID]
	if !ok {
		return nil, fmt.Errorf("%w: station_id=%s", ErrUnknownStation, stationID)
	}
	return rec, nil
}

// CountJoinProducers implements JoinCounter. It scans all registered stations
// and counts those that declare a downstream join with joinID to
// targetStationID.
func (r *Registry) CountJoinProducers(joinID, targetStationID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	count := 0
	for _, rec := range r.byID {
		if rec.DeclaredDownstream == "" {
			continue
		}
		var downstream []DownstreamTarget
		if err := json.Unmarshal([]byte(rec.DeclaredDownstream), &downstream); err != nil {
			continue
		}
		for _, d := range downstream {
			if d.Mode == "join" && d.JoinID == joinID && d.StationID == targetStationID {
				count++
				break
			}
		}
	}
	return count
}

// shortHash returns a stable short suffix for revision id derivation.
func shortHash(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[len(s)-12:]
}
