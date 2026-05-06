// Package stations provides station-revision resolution.
//
// Milestone 2 ships an in-memory Registry seeded at startup (from config,
// station.yaml files, or tests). Both the file loader and direct seeding
// satisfy the Resolver interface so the task service is insulated from the
// source of station-revision material.
package stations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/eum/veriproc/internal/store"
)

// ErrUnknownStation is returned when the supplied station_id / proc_type
// cannot be resolved (Spec §5.3.2, §5.7 unknown_station).
var ErrUnknownStation = errors.New("stations: unknown station")

// ErrAmbiguousProcType is returned when proc_type alone resolves to more
// than one station and the caller did not pin a station_id (Spec §5.3.2).
var ErrAmbiguousProcType = errors.New("stations: ambiguous proc_type")

// ErrDuplicateStation is returned when two different revisions are registered
// for the same station_id during one startup load. Later milestones may add a
// revision-selection policy; the M0-M7 profile keeps startup deterministic.
var ErrDuplicateStation = errors.New("stations: duplicate station")

// Resolver is the interface used by the task service.
type Resolver interface {
	// Resolve returns a station revision for the destination request. Either
	// stationID or procType must be non-empty; if both are supplied they must
	// resolve consistently.
	Resolve(ctx context.Context, stationID, procType string) (*store.StationRevisionRecord, error)
}

// Spec describes one station the registry will serve.
type Spec struct {
	StationID      string
	ProcType       string
	ContentHash    string
	SchemaVersion  string
	Inputs         []InputDefinition
	Outputs        []OutputDefinition
	Downstream     []DownstreamTarget
	Publication    PublicationPolicy
	RollingFolders map[string][]string
	// Scripts maps script verb (e.g. "run") to the resolved absolute path of
	// the script file. Populated by the file loader; empty for seed-only stations.
	Scripts map[string]string
}

// Registry is the in-memory Resolver used by M2.
type Registry struct {
	mu     sync.RWMutex
	byID   map[string]*store.StationRevisionRecord
	byProc map[string][]*store.StationRevisionRecord
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byID:   map[string]*store.StationRevisionRecord{},
		byProc: map[string][]*store.StationRevisionRecord{},
	}
}

// Seed registers a station revision both in memory and (best-effort) in the
// store, so foreign-key constraints from runs remain satisfiable. If a
// matching (station_id, content_hash) row already exists the store insert is
// treated as a no-op.
func (r *Registry) Seed(ctx context.Context, s *store.Store, specs ...Spec) error {
	for _, sp := range specs {
		if sp.SchemaVersion == "" {
			sp.SchemaVersion = DefaultSchemaVersion
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
		publicationPolicy := ""
		if sp.Publication.Enabled {
			b, _ := json.Marshal(sp.Publication)
			publicationPolicy = string(b)
		}
		rollingFolders := ""
		if len(sp.RollingFolders) > 0 {
			b, _ := json.Marshal(sp.RollingFolders)
			rollingFolders = string(b)
		}
		declaredScripts := ""
		if len(sp.Scripts) > 0 {
			b, _ := json.Marshal(sp.Scripts)
			declaredScripts = string(b)
		}
		rec := &store.StationRevisionRecord{
			RevisionID:         fmt.Sprintf("rev-%s-%s", sp.StationID, shortHash(sp.ContentHash)),
			StationID:          sp.StationID,
			ContentHash:        sp.ContentHash,
			SchemaVersion:      sp.SchemaVersion,
			DeclaredInputs:     declaredInputs,
			DeclaredOutputs:    declaredOutputs,
			DeclaredDownstream: declaredDownstream,
			PublicationPolicy:  publicationPolicy,
			RollingFolders:     rollingFolders,
			DeclaredScripts:    declaredScripts,
		}
		r.mu.Lock()
		if existing, ok := r.byID[sp.StationID]; ok {
			if existing.ContentHash == rec.ContentHash && existing.SchemaVersion == rec.SchemaVersion {
				r.mu.Unlock()
				continue
			}
			r.mu.Unlock()
			return fmt.Errorf("%w: station_id=%s", ErrDuplicateStation, sp.StationID)
		}
		r.mu.Unlock()
		if s != nil {
			if err := s.Stations().Insert(ctx, rec); err != nil && !errors.Is(err, store.ErrConflict) {
				return fmt.Errorf("seed station %s: %w", sp.StationID, err)
			}
		}
		r.mu.Lock()
		r.byID[sp.StationID] = rec
		if sp.ProcType != "" {
			r.byProc[sp.ProcType] = append(r.byProc[sp.ProcType], rec)
		}
		r.mu.Unlock()
	}
	return nil
}

// Resolve implements Resolver.
func (r *Registry) Resolve(_ context.Context, stationID, procType string) (*store.StationRevisionRecord, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	switch {
	case stationID != "" && procType != "":
		rec, ok := r.byID[stationID]
		if !ok {
			return nil, fmt.Errorf("%w: station_id=%s", ErrUnknownStation, stationID)
		}
		// Cross-check proc_type consistency.
		candidates := r.byProc[procType]
		for _, c := range candidates {
			if c.StationID == stationID {
				return rec, nil
			}
		}
		return nil, fmt.Errorf("%w: station %s does not match proc_type %s",
			ErrUnknownStation, stationID, procType)
	case stationID != "":
		rec, ok := r.byID[stationID]
		if !ok {
			return nil, fmt.Errorf("%w: station_id=%s", ErrUnknownStation, stationID)
		}
		return rec, nil
	case procType != "":
		candidates := r.byProc[procType]
		switch len(candidates) {
		case 0:
			return nil, fmt.Errorf("%w: proc_type=%s", ErrUnknownStation, procType)
		case 1:
			return candidates[0], nil
		default:
			ids := make([]string, 0, len(candidates))
			for _, c := range candidates {
				ids = append(ids, c.StationID)
			}
			sort.Strings(ids)
			return nil, fmt.Errorf("%w: proc_type %s -> %v", ErrAmbiguousProcType, procType, ids)
		}
	default:
		return nil, fmt.Errorf("%w: empty destination", ErrUnknownStation)
	}
}

// shortHash returns a stable short suffix for revision id derivation.
func shortHash(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[len(s)-12:]
}
