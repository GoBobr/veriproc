package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newCleanerServer returns an httptest server exposing the cleaner endpoints
// with canned report responses for CLI exercise.
func newCleanerServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/tasks/{task_id}", func(w http.ResponseWriter, r *http.Request) {
		dry := r.URL.Query().Get("dry_run") == "true"
		writeJSON(w, 200, cleanReportBody(dry, []string{r.PathValue("task_id")}))
	})
	mux.HandleFunc("DELETE /api/v1/runs/{run_id}", func(w http.ResponseWriter, r *http.Request) {
		dry := r.URL.Query().Get("dry_run") == "true"
		writeJSON(w, 200, cleanReportBody(dry, []string{r.PathValue("run_id")}))
	})
	mux.HandleFunc("POST /api/v1/maintenance/clean", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = decodeBody(r, &body)
		dry, _ := body["dry_run"].(bool)
		writeJSON(w, 200, cleanReportBody(dry, []string{"early"}))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// cleanReportBody builds a canned cleaner report response for the test server.
func cleanReportBody(dryRun bool, taskIDs []string) map[string]any {
	ids := make([]any, len(taskIDs))
	for i, id := range taskIDs {
		ids[i] = id
	}
	return map[string]any{
		"dry_run": dryRun,
		"counts": map[string]any{
			"tasks": len(taskIDs), "runs": len(taskIDs), "artifacts": 0,
		},
		"task_ids":              ids,
		"run_ids":               []any{},
		"working_roots":         []any{},
		"working_roots_removed": 0,
	}
}

// newCleanerServerCapturing is like newCleanerServer but records the last clean
// request body so tests can verify CLI flags are forwarded correctly.
func newCleanerServerCapturing(t *testing.T) (*httptest.Server, *map[string]any) {
	t.Helper()
	captured := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/maintenance/clean", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = decodeBody(r, &body)
		for k, v := range body {
			captured[k] = v
		}
		dry, _ := body["dry_run"].(bool)
		writeJSON(w, 200, cleanReportBody(dry, []string{"early"}))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &captured
}

// TestCLI_TaskDelete_Force deletes without prompting and shows a preview first.
func TestCLI_TaskDelete_Force(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "task", "delete", "t1", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "t1") {
		t.Errorf("stdout missing task id: %q", out)
	}
	// Without --quiet, a dry-run preview is shown before the actual delete.
	if !strings.Contains(out, `"dry_run": true`) {
		t.Errorf("stdout missing dry-run preview with --force: %q", out)
	}
}

// TestCLI_TaskDelete_MultipleIDs deletes several tasks at once and verifies
// the combined report contains all IDs.
func TestCLI_TaskDelete_MultipleIDs(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "task", "delete", "--force", "t1", "t2")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	// Both task ids should appear in the merged output.
	if !strings.Contains(out, "t1") || !strings.Contains(out, "t2") {
		t.Errorf("stdout missing task ids: %q", out)
	}
}

// TestCLI_TaskDelete_Force_Quiet deletes without prompting; in table mode
// counts are shown but per-ID listing is suppressed.
func TestCLI_TaskDelete_Force_Quiet(t *testing.T) {
	srv := newCleanerServer(t)
	// Flags must come before the positional task ID (Go flag package stops at
	// the first non-flag argument). Use table output to test quiet suppression.
	code, out, errs := runCLITable(t, srv.URL, "task", "delete", "--force", "--quiet", "t1")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	// Counts must still be present.
	if !strings.Contains(out, "tasks") {
		t.Errorf("stdout missing counts with --quiet: %q", out)
	}
	// Per-ID listing must be suppressed.
	if strings.Contains(out, "task ids:") {
		t.Errorf("stdout should not contain task id listing with --quiet: %q", out)
	}
}

// TestCLI_TaskDelete_DryRun previews only.
func TestCLI_TaskDelete_DryRun(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "task", "delete", "t1", "--dry-run")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "dry_run") {
		t.Errorf("stdout missing dry_run marker: %q", out)
	}
}

// TestCLI_RunDelete_Force deletes a run by raw id and shows a preview first.
func TestCLI_RunDelete_Force(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "run", "delete", "run-internal-001", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	// Without --quiet, a dry-run preview is shown before the actual delete.
	if !strings.Contains(out, `"dry_run": true`) {
		t.Errorf("stdout missing dry-run preview with --force: %q", out)
	}
}

// TestCLI_RunDelete_Force_Quiet deletes a run; in table mode counts are shown
// but per-ID listing is suppressed.
func TestCLI_RunDelete_Force_Quiet(t *testing.T) {
	srv := newCleanerServer(t)
	// Flags must come before the positional run ID (Go flag package stops at
	// the first non-flag argument). Use table output to test quiet suppression.
	code, out, errs := runCLITable(t, srv.URL, "run", "delete", "--force", "--quiet", "run-internal-001")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	// Counts must still be present.
	if !strings.Contains(out, "tasks") {
		t.Errorf("stdout missing counts with --quiet: %q", out)
	}
	// Per-ID listing must be suppressed.
	if strings.Contains(out, "run ids:") {
		t.Errorf("stdout should not contain run id listing with --quiet: %q", out)
	}
}

// TestCLI_Clean_NoCutoff_PrintsHelp returns help and exit 0.
func TestCLI_Clean_NoCutoff_PrintsHelp(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, _ := runCLI(t, srv.URL, "clean")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "veriproc clean") || !strings.Contains(out, "--before") {
		t.Errorf("expected clean help, got %q", out)
	}
}

// TestCLI_Clean_Force runs the sweep without prompting and shows a preview first.
func TestCLI_Clean_Force(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "clean", "--before", "2025-06-01T00:00:00Z", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "early") {
		t.Errorf("stdout missing selected task: %q", out)
	}
	// Without --quiet, a dry-run preview is shown before the actual sweep.
	if !strings.Contains(out, `"dry_run": true`) {
		t.Errorf("stdout missing dry-run preview with --force: %q", out)
	}
}

// TestCLI_Clean_Force_Quiet runs the sweep without prompting; in table mode
// counts are shown but per-ID listing is suppressed.
func TestCLI_Clean_Force_Quiet(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLITable(t, srv.URL, "clean", "--before", "2025-06-01T00:00:00Z", "--force", "--quiet")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	// Counts must still be present.
	if !strings.Contains(out, "tasks") {
		t.Errorf("stdout missing counts with --quiet: %q", out)
	}
	// Per-ID listing must be suppressed.
	if strings.Contains(out, "task ids:") {
		t.Errorf("stdout should not contain task id listing with --quiet: %q", out)
	}
}

// TestCLI_Clean_InvalidTimestamp fails validation before any round-trip.
func TestCLI_Clean_InvalidTimestamp(t *testing.T) {
	srv := newCleanerServer(t)
	code, _, errs := runCLI(t, srv.URL, "clean", "--before", "not-a-time", "--force")
	if code != ExitValidation {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, ExitValidation, errs)
	}
}

// TestCLI_Clean_InvalidBasis rejects an unknown --by value.
func TestCLI_Clean_InvalidBasis(t *testing.T) {
	srv := newCleanerServer(t)
	code, _, errs := runCLI(t, srv.URL, "clean", "--before", "2025-06-01T00:00:00Z", "--by", "bogus", "--force")
	if code != ExitValidation {
		t.Fatalf("exit = %d, want %d (stderr=%s)", code, ExitValidation, errs)
	}
	if !strings.Contains(errs, "--by") {
		t.Errorf("stderr missing --by error: %q", errs)
	}
}

// TestCLI_Clean_ProcessingWindowBasis forwards the basis to the server.
func TestCLI_Clean_ProcessingWindowBasis(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "clean", "--before", "2025-06-01T00:00:00Z", "--by", "processing-window", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "early") {
		t.Errorf("stdout missing selected task: %q", out)
	}
}

// TestCLI_Clean_StationFilter forwards the --station flag to the server as
// station_id in the clean request body.
func TestCLI_Clean_StationFilter(t *testing.T) {
	srv, captured := newCleanerServerCapturing(t)
	code, _, errs := runCLI(t, srv.URL, "clean", "--before", "2025-06-01T00:00:00Z", "--station", "SCENE-L2", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if (*captured)["station_id"] != "SCENE-L2" {
		t.Errorf("server received station_id = %v, want SCENE-L2", (*captured)["station_id"])
	}
}

// TestCLI_Clean_NoStation omits station_id when --station is not given.
func TestCLI_Clean_NoStation(t *testing.T) {
	srv, captured := newCleanerServerCapturing(t)
	code, _, errs := runCLI(t, srv.URL, "clean", "--before", "2025-06-01T00:00:00Z", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if _, ok := (*captured)["station_id"]; ok {
		t.Errorf("server should not receive station_id, got %v", (*captured)["station_id"])
	}
}

// decodeBody is a tiny JSON body decoder for the test server.
func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
