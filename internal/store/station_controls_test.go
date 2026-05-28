package store

import (
	"context"
	"testing"
	"time"
)

func TestStationControls_DefaultAndSetPaused(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	initial, err := s.StationControls().Get(ctx, "SCENE-L2")
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	if initial.StationID != "SCENE-L2" || initial.Paused {
		t.Fatalf("unexpected default record: %+v", initial)
	}

	mkStation(t, s, "SCENE-L2")
	at := time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC)
	if err := s.StationControls().SetPaused(ctx, "SCENE-L2", true, at); err != nil {
		t.Fatalf("set paused: %v", err)
	}
	got, err := s.StationControls().Get(ctx, "SCENE-L2")
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if !got.Paused {
		t.Fatalf("paused = false, want true")
	}
	if !got.UpdatedAt.Equal(at) {
		t.Fatalf("updated_at = %s, want %s", got.UpdatedAt, at)
	}

	paused, err := s.StationControls().IsPaused(ctx, "SCENE-L2")
	if err != nil {
		t.Fatalf("is paused: %v", err)
	}
	if !paused {
		t.Fatalf("is paused = false, want true")
	}

	if err := s.StationControls().SetPaused(ctx, "SCENE-L2", false, at.Add(time.Minute)); err != nil {
		t.Fatalf("unset paused: %v", err)
	}
	paused, err = s.StationControls().IsPaused(ctx, "SCENE-L2")
	if err != nil {
		t.Fatalf("is paused after unset: %v", err)
	}
	if paused {
		t.Fatalf("is paused = true, want false")
	}
}

func TestStationControls_UnknownStationDoesNotDependOnFK(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.StationControls().SetPaused(ctx, "UNKNOWN-STATION", true, time.Date(2025, 7, 3, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("set paused on unknown station: %v", err)
	}
	paused, err := s.StationControls().IsPaused(ctx, "UNKNOWN-STATION")
	if err != nil {
		t.Fatalf("is paused on unknown station: %v", err)
	}
	if !paused {
		t.Fatalf("paused = false, want true")
	}
}
