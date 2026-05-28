// Package health implements the health and readiness aggregation logic that
// backs the corresponding REST endpoints (Spec 5.7) and CLI commands
// (Spec 6.3.12, 6.3.13).
//
// Liveness reports process state only and never depends on external systems.
// Readiness aggregates the results of registered Checkers, each of which
// represents one dependency (database, scheduler adapter, station registry,
// shared storage, rolling-archive configuration, …). Milestone 0 ships with
// no checkers; later milestones register their own.
package health

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// State enumerates the possible aggregated states.
type State string

const (
	StateOK      State = "ok"
	StateReady   State = "ready"
	StateUnready State = "unready"
	StateDown    State = "down"
)

// DependencyState represents one dependency's reported state.
type DependencyState string

const (
	DepUp      DependencyState = "up"
	DepDown    DependencyState = "down"
	DepUnknown DependencyState = "unknown"
)

// CheckResult is one dependency's reported status at a point in time.
type CheckResult struct {
	Name    string          `json:"name"`
	State   DependencyState `json:"state"`
	Message string          `json:"message,omitempty"`
}

// Checker is implemented by anything that can report dependency liveness.
type Checker interface {
	Name() string
	Check(ctx context.Context) CheckResult
}

// Aggregator combines liveness signals and dependency checkers.
type Aggregator struct {
	mu          sync.RWMutex
	checkers    []Checker
	shuttingDn  atomic.Bool
	checkBudget time.Duration
}

// NewAggregator returns an aggregator with the supplied per-check timeout
// budget. Pass 0 for "no timeout".
func NewAggregator(checkBudget time.Duration) *Aggregator {
	return &Aggregator{checkBudget: checkBudget}
}

// Register adds a dependency checker. Registration is concurrency-safe.
func (a *Aggregator) Register(c Checker) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.checkers = append(a.checkers, c)
}

// BeginShutdown flips the aggregator into a shutting-down state. After this
// call, Liveness reports StateDown so that load balancers can drain traffic.
func (a *Aggregator) BeginShutdown() { a.shuttingDn.Store(true) }

// LivenessReport is the response payload for /health.
type LivenessReport struct {
	Status     State  `json:"status"`
	InstanceID string `json:"instance_id,omitempty"`
	APIVersion string `json:"api_version,omitempty"`
	Version    string `json:"version,omitempty"`
	Commit     string `json:"commit,omitempty"`
}

// ReadinessReport is the response payload for /readiness.
type ReadinessReport struct {
	ReadinessState State                  `json:"readiness_state"`
	InstanceID     string                 `json:"instance_id,omitempty"`
	APIVersion     string                 `json:"api_version,omitempty"`
	Dependencies   map[string]CheckResult `json:"dependencies"`
}

// Liveness returns the current liveness snapshot.
func (a *Aggregator) Liveness() LivenessReport {
	if a.shuttingDn.Load() {
		return LivenessReport{Status: StateDown}
	}
	return LivenessReport{Status: StateOK}
}

// Readiness invokes all registered checkers and returns an aggregated report.
// The aggregate state is StateReady iff every dependency reports DepUp; any
// other case is StateUnready. The result is also StateUnready while the
// aggregator is in shutdown mode.
func (a *Aggregator) Readiness(ctx context.Context) ReadinessReport {
	a.mu.RLock()
	checkers := append([]Checker(nil), a.checkers...)
	budget := a.checkBudget
	a.mu.RUnlock()

	deps := make(map[string]CheckResult, len(checkers))
	allUp := true
	for _, c := range checkers {
		ctx := ctx
		if budget > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, budget)
			defer cancel()
		}
		r := c.Check(ctx)
		if r.Name == "" {
			r.Name = c.Name()
		}
		deps[r.Name] = r
		if r.State != DepUp {
			allUp = false
		}
	}

	state := StateReady
	if a.shuttingDn.Load() || !allUp {
		state = StateUnready
	}
	return ReadinessReport{ReadinessState: state, Dependencies: deps}
}
