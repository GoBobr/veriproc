package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// StationControlRecord stores mutable per-station operator control state.
type StationControlRecord struct {
	StationID string
	Paused    bool
	UpdatedAt time.Time
}

// StationControlRepo persists station control state.
type StationControlRepo struct {
	q       querier
	dialect dialect
}

// SetPaused upserts the paused flag for a station.
func (r *StationControlRepo) SetPaused(ctx context.Context, stationID string, paused bool, at time.Time) error {
	if at.IsZero() {
		at = nowUTC()
	}
	_, err := r.q.ExecContext(ctx, `
		INSERT INTO station_controls (station_id, paused, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(station_id) DO UPDATE SET
			paused = excluded.paused,
			updated_at = excluded.updated_at`,
		stationID, boolToInt(paused), at.UTC())
	if err != nil {
		if r.dialect.IsForeignKeyViolation(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

// Get returns station control state or a default unpaused record when no row exists yet.
func (r *StationControlRepo) Get(ctx context.Context, stationID string) (*StationControlRecord, error) {
	row := r.q.QueryRowContext(ctx, `
		SELECT station_id, paused, updated_at
		FROM station_controls WHERE station_id = ?`, stationID)
	var rec StationControlRecord
	var paused int
	if err := row.Scan(&rec.StationID, &paused, &rec.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return &StationControlRecord{StationID: stationID, Paused: false}, nil
		}
		return nil, err
	}
	rec.Paused = paused != 0
	rec.UpdatedAt = rec.UpdatedAt.UTC()
	return &rec, nil
}

// IsPaused reports whether the station is currently paused.
func (r *StationControlRepo) IsPaused(ctx context.Context, stationID string) (bool, error) {
	rec, err := r.Get(ctx, stationID)
	if err != nil {
		return false, err
	}
	return rec.Paused, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
