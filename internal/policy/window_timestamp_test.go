package policy

import (
	"testing"
	"time"
)

// TestParseWindowTimestamp covers all four accepted forms and rejection cases.
func TestParseWindowTimestamp(t *testing.T) {
	// All valid inputs normalize to the same UTC instant.
	wantSec := time.Date(2026, 5, 13, 13, 14, 29, 0, time.UTC)
	wantMs := time.Date(2026, 5, 13, 13, 14, 29, 100_000_000, time.UTC) // 100 ms

	cases := []struct {
		name    string
		input   string
		want    time.Time
		wantErr bool
	}{
		// RFC 3339 whole-second UTC.
		{"rfc3339_seconds", "2026-05-13T13:14:29Z", wantSec, false},
		// RFC 3339 whole-second with offset (normalized to UTC).
		{"rfc3339_offset", "2026-05-13T14:14:29+01:00", wantSec, false},
		// RFC 3339 with milliseconds.
		{"rfc3339_millis", "2026-05-13T13:14:29.100Z", wantMs, false},
		// RFC 3339 with more precision (nanoseconds truncated to millis via test).
		{"rfc3339_3frac", "2026-05-13T13:14:29.000Z", wantSec, false},
		// Compact UTC seconds (15 chars).
		{"compact_sec", "20260513T131429", wantSec, false},
		// Compact UTC milliseconds (18 chars, 000 ms → same second instant).
		{"compact_ms_zero", "20260513T131429000", wantSec, false},
		// Compact UTC milliseconds with non-zero ms.
		{"compact_ms_100", "20260513T131429100", wantMs, false},
		// Compact UTC max millis.
		{"compact_ms_999", "20260513T131429999",
			time.Date(2026, 5, 13, 13, 14, 29, 999_000_000, time.UTC), false},

		// RFC 3339 without timezone (interpreted as UTC).
		{"rfc3339_no_tz", "2026-05-13T13:14:29", wantSec, false},
		// RFC 3339 without timezone, with fractional seconds.
		{"rfc3339_no_tz_ms", "2026-05-13T13:14:29.100", wantMs, false},
		// ISO 8601 date only (midnight UTC).
		{"date_iso", "2026-05-13", time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC), false},
		// Compact date only (midnight UTC).
		{"date_compact", "20260513", time.Date(2026, 5, 13, 0, 0, 0, 0, time.UTC), false},

		// Error cases.
		{"empty", "", time.Time{}, true},
		{"garbage", "not-a-timestamp", time.Time{}, true},
		{"compact_wrong_len_16", "20260513T131429Z", time.Time{}, true},
		{"compact_wrong_len_17", "20260513T1314290", time.Time{}, true},
		{"compact_ms_bad_digits", "20260513T131429abc", time.Time{}, true},
		{"compact_ms_over_999", "20260513T1314291000", time.Time{}, true},
		{"compact_invalid_date", "20261332T131429", time.Time{}, true},
		{"compact_invalid_time", "20260513T256100", time.Time{}, true},
		{"date_compact_invalid", "20261332", time.Time{}, true},
		{"date_iso_invalid", "2026-13-32", time.Time{}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseWindowTimestamp(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Errorf("ParseWindowTimestamp(%q) = %v, want error", tc.input, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseWindowTimestamp(%q) error = %v", tc.input, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("ParseWindowTimestamp(%q) = %v, want %v", tc.input, got, tc.want)
			}
			if got.Location() != time.UTC {
				t.Errorf("ParseWindowTimestamp(%q) location = %v, want UTC", tc.input, got.Location())
			}
		})
	}
}

// TestParseWindowTimestamp_Compact_ms_overflow — milliseconds > 999 must fail.
func TestParseWindowTimestamp_Compact_ms_overflow(t *testing.T) {
	_, err := ParseWindowTimestamp("20260513T1314291000")
	if err == nil {
		t.Error("expected error for 19-char compact timestamp (ms part 1000)")
	}
}
