package console

import (
	"testing"
	"time"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestExpandStationRow_OrderingAndPadding(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	summary := StationSummary{
		StationID: "STA",
		Slots: []SummarySlot{
			{Kind: "running", TaskID: "t1", RetryIndex: 0, State: "running"},
			{Kind: "running", TaskID: "t2", RetryIndex: 1, State: "running"},
			{Kind: "queued", TaskID: "t3", RetryIndex: 0, State: "dispatched"},
			{Kind: "completed", TaskID: "t4", RetryIndex: 0, State: "complete", TerminalAt: ptrTime(now.Add(-5 * time.Second))},
			{Kind: "failed", TaskID: "t5", RetryIndex: 0, State: "failed", TerminalAt: ptrTime(now.Add(-1 * time.Minute))},
		},
		RunningCount: 2,
		QueuedCount:  1,
		Counts:       SummaryCounts{Success: 10, Failure: 1},
	}
	row := ExpandStationRow(summary, nil, "vp1", 12, 30*time.Second, now)
	if len(row.Slots) != 12 {
		t.Fatalf("expected 12 slots, got %d", len(row.Slots))
	}
	wantOrder := []SlotKind{SlotRunning, SlotRunning, SlotCompleted, SlotQueued, SlotFailed}
	for i, want := range wantOrder {
		if row.Slots[i].Kind != want {
			t.Errorf("slot[%d] kind = %q, want %q", i, row.Slots[i].Kind, want)
		}
	}
	for i := len(wantOrder); i < 12; i++ {
		if row.Slots[i].Kind != SlotEmpty {
			t.Errorf("slot[%d] should be empty, got %q", i, row.Slots[i].Kind)
		}
	}
}

func TestExpandStationRow_CompletedAgesOut(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	summary := StationSummary{
		StationID: "STA",
		Slots: []SummarySlot{
			{Kind: "completed", TaskID: "t1", TerminalAt: ptrTime(now.Add(-31 * time.Second))},
			{Kind: "completed", TaskID: "t2", TerminalAt: ptrTime(now.Add(-1 * time.Second))},
		},
	}
	row := ExpandStationRow(summary, nil, "vp1", 12, 30*time.Second, now)
	completed := 0
	for _, s := range row.Slots {
		if s.Kind == SlotCompleted {
			completed++
			if s.TaskID != "t2" {
				t.Errorf("expected aged-out completed slot to be filtered; got t=%s", s.TaskID)
			}
		}
	}
	if completed != 1 {
		t.Errorf("expected exactly one completed slot to survive, got %d", completed)
	}
}

func TestExpandStationRow_HiddenFailedSuppressed(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	summary := StationSummary{
		StationID: "STA",
		Slots: []SummarySlot{
			{Kind: "failed", TaskID: "t1", RetryIndex: 0, TerminalAt: ptrTime(now.Add(-time.Minute))},
			{Kind: "failed", TaskID: "t2", RetryIndex: 0, TerminalAt: ptrTime(now.Add(-time.Minute))},
		},
	}
	hidden := map[HiddenRunKey]struct{}{
		{InstanceID: "vp1", StationID: "STA", TaskID: "t1", RetryIndex: 0}: {},
	}
	row := ExpandStationRow(summary, hidden, "vp1", 12, 30*time.Second, now)
	failed := 0
	for _, s := range row.Slots {
		if s.Kind == SlotFailed {
			failed++
			if s.TaskID == "t1" {
				t.Errorf("hidden task t1 should be suppressed")
			}
		}
	}
	if failed != 1 {
		t.Errorf("expected exactly one failed slot to remain visible, got %d", failed)
	}
}

func TestExpandStationRow_Overflow(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	slots := make([]SummarySlot, 0, 15)
	for i := 0; i < 15; i++ {
		slots = append(slots, SummarySlot{Kind: "running", TaskID: "t", RetryIndex: i, State: "running"})
	}
	row := ExpandStationRow(StationSummary{StationID: "STA", Slots: slots}, nil, "vp1", 12, 30*time.Second, now)
	if row.Overflow != 3 {
		t.Errorf("overflow = %d, want 3", row.Overflow)
	}
	if len(row.Slots) != 12 {
		t.Fatalf("slots truncated to %d, want 12", len(row.Slots))
	}
}

func TestExpandStationRow_MixedKinds(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	summary := StationSummary{
		StationID: "STA",
		Slots: []SummarySlot{
			{Kind: "running", TaskID: "r1", RetryIndex: 0},
			{Kind: "queued", TaskID: "q1", RetryIndex: 0},
			{Kind: "completed", TaskID: "c1", TerminalAt: ptrTime(now)},
			{Kind: "failed", TaskID: "f1", TerminalAt: ptrTime(now.Add(-time.Second))},
		},
	}
	row := ExpandStationRow(summary, nil, "vp1", 12, 30*time.Second, now)
	if row.Slots[0].Kind != SlotRunning {
		t.Errorf("first slot must be running")
	}
	if row.Slots[1].Kind != SlotCompleted {
		t.Errorf("second slot must be completed")
	}
	if row.Slots[2].Kind != SlotQueued {
		t.Errorf("third slot must be queued")
	}
	if row.Slots[3].Kind != SlotFailed {
		t.Errorf("fourth slot must be failed")
	}
}
