package health

import (
	"context"
	"testing"
)

type stubChecker struct {
	name  string
	state DependencyState
	msg   string
}

func (s stubChecker) Name() string { return s.name }
func (s stubChecker) Check(_ context.Context) CheckResult {
	return CheckResult{Name: s.name, State: s.state, Message: s.msg}
}

// TestHealth_Liveness_Default — fresh aggregator reports ok liveness.
func TestHealth_Liveness_Default(t *testing.T) {
	a := NewAggregator(0)
	if got := a.Liveness().Status; got != StateOK {
		t.Errorf("liveness = %q, want %q", got, StateOK)
	}
}

// TestHealth_Liveness_Shutdown — after BeginShutdown liveness flips to down.
func TestHealth_Liveness_Shutdown(t *testing.T) {
	a := NewAggregator(0)
	a.BeginShutdown()
	if got := a.Liveness().Status; got != StateDown {
		t.Errorf("liveness = %q, want %q", got, StateDown)
	}
}

// TestHealth_Readiness_NoDeps — empty deps means ready.
func TestHealth_Readiness_NoDeps(t *testing.T) {
	a := NewAggregator(0)
	r := a.Readiness(context.Background())
	if r.ReadinessState != StateReady {
		t.Errorf("readiness state = %q, want %q", r.ReadinessState, StateReady)
	}
	if len(r.Dependencies) != 0 {
		t.Errorf("expected no dependencies, got %v", r.Dependencies)
	}
}

// TestHealth_Readiness_AllUp — every checker up → ready.
func TestHealth_Readiness_AllUp(t *testing.T) {
	a := NewAggregator(0)
	a.Register(stubChecker{name: "db", state: DepUp})
	a.Register(stubChecker{name: "scheduler", state: DepUp})
	r := a.Readiness(context.Background())
	if r.ReadinessState != StateReady {
		t.Errorf("readiness = %q, want ready; deps=%v", r.ReadinessState, r.Dependencies)
	}
	if len(r.Dependencies) != 2 {
		t.Errorf("dep count = %d, want 2", len(r.Dependencies))
	}
}

// TestHealth_Readiness_OneDown — any down dep yields unready.
func TestHealth_Readiness_OneDown(t *testing.T) {
	a := NewAggregator(0)
	a.Register(stubChecker{name: "db", state: DepUp})
	a.Register(stubChecker{name: "scheduler", state: DepDown, msg: "connection refused"})
	r := a.Readiness(context.Background())
	if r.ReadinessState != StateUnready {
		t.Errorf("readiness = %q, want unready", r.ReadinessState)
	}
	if r.Dependencies["scheduler"].Message == "" {
		t.Errorf("expected propagated message for failing dep")
	}
}

// TestHealth_Readiness_ShutdownForcesUnready — readiness is unready while
// shutting down, regardless of dependency state.
func TestHealth_Readiness_ShutdownForcesUnready(t *testing.T) {
	a := NewAggregator(0)
	a.Register(stubChecker{name: "db", state: DepUp})
	a.BeginShutdown()
	r := a.Readiness(context.Background())
	if r.ReadinessState != StateUnready {
		t.Errorf("readiness = %q, want unready (shutdown)", r.ReadinessState)
	}
}
