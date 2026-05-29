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
	report := func(dryRun bool, taskIDs []string) map[string]any {
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
	mux.HandleFunc("DELETE /api/v1/tasks/{task_id}", func(w http.ResponseWriter, r *http.Request) {
		dry := r.URL.Query().Get("dry_run") == "true"
		writeJSON(w, 200, report(dry, []string{r.PathValue("task_id")}))
	})
	mux.HandleFunc("DELETE /api/v1/runs/{run_id}", func(w http.ResponseWriter, r *http.Request) {
		dry := r.URL.Query().Get("dry_run") == "true"
		writeJSON(w, 200, report(dry, []string{r.PathValue("run_id")}))
	})
	mux.HandleFunc("POST /api/v1/maintenance/clean", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = decodeBody(r, &body)
		dry, _ := body["dry_run"].(bool)
		writeJSON(w, 200, report(dry, []string{"early"}))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestCLI_TaskDelete_Force deletes without prompting.
func TestCLI_TaskDelete_Force(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "task", "delete", "t1", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "t1") {
		t.Errorf("stdout missing task id: %q", out)
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

// TestCLI_RunDelete_Force deletes a run by raw id.
func TestCLI_RunDelete_Force(t *testing.T) {
	srv := newCleanerServer(t)
	code, _, errs := runCLI(t, srv.URL, "run", "delete", "run-internal-001", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
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

// TestCLI_Clean_Force runs the sweep without prompting.
func TestCLI_Clean_Force(t *testing.T) {
	srv := newCleanerServer(t)
	code, out, errs := runCLI(t, srv.URL, "clean", "--before", "2025-06-01T00:00:00Z", "--force")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "early") {
		t.Errorf("stdout missing selected task: %q", out)
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

// decodeBody is a tiny JSON body decoder for the test server.
func decodeBody(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}
