// Package groups implements split-group identity, membership, and the
// minimal aggregator required by Spec §3.11 / §5.5.7.
//
// A split group is a logical container for a set of related runs that
// originated from the same upstream split decision (e.g. one collection
// fanned out into per-station tasks). The aggregator computes a derived
// group state from the latest member-run states:
//
//   - any member failed and no member produced a canonical run → "failed"
//   - all members terminal AND at least one member is canonical → "complete"
//   - any member still active → "open" / "aggregating"
//
// The split-group state is purely derived: no runtime actions ever depend on
// it. Cancellation/promotion is still per-run.
package groups

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eum/veriproc/internal/store"
)

// ErrGroupNotFound is returned when the requested group id does not exist.
var ErrGroupNotFound = errors.New("split group not found")

// Service ties the split-group repository to a clock, providing
// EnsureGroup / RegisterRun / Aggregate / List / Get for HTTP and CLI use.
type Service struct {
	store *store.Store
	clock func() time.Time
}

// NewService constructs a Service.
func NewService(st *store.Store, clock func() time.Time) *Service {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	return &Service{store: st, clock: clock}
}

// EnsureGroup is idempotent: returns the existing group or creates an "open"
// one with the supplied id. Optional label/description and expected member
// count are honored on first creation.
func (s *Service) EnsureGroup(ctx context.Context, id, label, description string, expected int) (*store.SplitGroupRecord, error) {
	if id == "" {
		return nil, fmt.Errorf("split_group_id required")
	}
	return s.store.SplitGroups().EnsureGroup(ctx, id, label, description, expected, s.clock())
}

// RegisterRun declares (group, run) membership. The group is auto-created
// on first reference. Idempotent on (group_id, run_id).
func (s *Service) RegisterRun(ctx context.Context, groupID, runID, taskID, role string) error {
	if groupID == "" {
		return nil
	}
	if _, err := s.EnsureGroup(ctx, groupID, "", "", 0); err != nil {
		return err
	}
	return s.store.SplitGroups().AddMember(ctx, &store.SplitGroupMember{
		SplitGroupID: groupID, RunID: runID, TaskID: taskID,
		Role: role, AddedAt: s.clock(),
	})
}

// Get returns a single group with materialized member-derived counts.
func (s *Service) Get(ctx context.Context, id string) (*GroupView, error) {
	g, err := s.store.SplitGroups().Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	members, err := s.store.SplitGroups().ListMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.materialize(ctx, g, members)
}

// List returns groups filtered by optional state, ordered by created_at DESC.
func (s *Service) List(ctx context.Context, state string, limit int) ([]*GroupView, error) {
	gs, err := s.store.SplitGroups().List(ctx, store.SplitGroupListFilter{State: state, Limit: limit})
	if err != nil {
		return nil, err
	}
	out := make([]*GroupView, 0, len(gs))
	for _, g := range gs {
		ms, err := s.store.SplitGroups().ListMembers(ctx, g.SplitGroupID)
		if err != nil {
			return nil, err
		}
		v, err := s.materialize(ctx, g, ms)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Close transitions an open group → aggregating, then runs Aggregate to
// snapshot the current member states. Idempotent on already-closed groups.
func (s *Service) Close(ctx context.Context, id string) (*GroupView, error) {
	g, err := s.store.SplitGroups().Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	now := s.clock()
	if g.State == store.SplitGroupStateOpen {
		if err := s.store.SplitGroups().SetState(ctx, id, store.SplitGroupStateAggregating, now); err != nil {
			return nil, err
		}
	}
	if _, err := s.Aggregate(ctx, id); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Aggregate inspects every member run's state and writes the derived state
// back to the group. Safe to call repeatedly. Returns the new derived state.
func (s *Service) Aggregate(ctx context.Context, id string) (string, error) {
	g, err := s.store.SplitGroups().Get(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", ErrGroupNotFound
		}
		return "", err
	}
	members, err := s.store.SplitGroups().ListMembers(ctx, id)
	if err != nil {
		return "", err
	}
	canonical, failed, terminal, total, summary := s.tallyMembers(ctx, members)
	derived := derivedState(g.State, canonical, failed, terminal, total)
	if err := s.store.SplitGroups().SetAggregation(ctx, id, canonical, failed, summary); err != nil {
		return "", err
	}
	if derived != g.State {
		if err := s.store.SplitGroups().SetState(ctx, id, derived, s.clock()); err != nil {
			return "", err
		}
	}
	return derived, nil
}

func (s *Service) tallyMembers(ctx context.Context, members []*store.SplitGroupMember) (canonical, failed, terminal, total int, summary string) {
	total = len(members)
	for _, m := range members {
		r, err := s.store.Runs().Get(ctx, m.RunID)
		if err != nil {
			continue
		}
		if r.State == "complete" || r.State == "failed" || r.State == "cancelled" {
			terminal++
		}
		if r.State == "failed" {
			failed++
		}
		if r.Canonicality == "canonical" || r.Canonicality == "forced" {
			canonical++
		}
	}
	summary = fmt.Sprintf("members=%d canonical=%d failed=%d terminal=%d", total, canonical, failed, terminal)
	return
}

func derivedState(current string, canonical, failed, terminal, total int) string {
	if current == store.SplitGroupStateOpen && terminal < total {
		return store.SplitGroupStateOpen
	}
	if total == 0 {
		return current
	}
	if terminal < total {
		// Closed by operator but members still running: keep aggregating.
		return store.SplitGroupStateAggregating
	}
	if canonical == 0 && failed > 0 {
		return store.SplitGroupStateFailed
	}
	if canonical > 0 {
		return store.SplitGroupStateComplete
	}
	if failed > 0 {
		return store.SplitGroupStateFailed
	}
	return store.SplitGroupStateAggregating
}

// GroupView is the materialized API/CLI representation.
type GroupView struct {
	*store.SplitGroupRecord
	Members []MemberView
}

// MemberView pairs a membership with its current run state for display.
type MemberView struct {
	*store.SplitGroupMember
	State        string
	Canonicality string
}

func (s *Service) materialize(ctx context.Context, g *store.SplitGroupRecord, members []*store.SplitGroupMember) (*GroupView, error) {
	v := &GroupView{SplitGroupRecord: g, Members: make([]MemberView, 0, len(members))}
	for _, m := range members {
		mv := MemberView{SplitGroupMember: m}
		if r, err := s.store.Runs().Get(ctx, m.RunID); err == nil {
			mv.State = r.State
			mv.Canonicality = r.Canonicality
		}
		v.Members = append(v.Members, mv)
	}
	return v, nil
}
