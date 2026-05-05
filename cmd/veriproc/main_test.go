package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAPI is a tiny in-memory backend used to verify CLI request/response
// shape and exit-code mapping.
type fakeAPI struct {
	t          *testing.T
	tasks      map[string]map[string]any
	runs       map[string]map[string]any
	groups     map[string]map[string]any
	cancelHits map[string]int
	server     *httptest.Server
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t:          t,
		tasks:      map[string]map[string]any{},
		runs:       map[string]map[string]any{},
		groups:     map[string]map[string]any{},
		cancelHits: map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok", "version": "test"})
	})
	mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"status": "ok"})
	})
	mux.HandleFunc("/api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			id := "task-1"
			task := map[string]any{
				"task_id":        id,
				"state":          "accepted",
				"split_group_id": body["split_group_id"],
			}
			f.tasks[id] = task
			writeJSON(w, 201, map[string]any{"task": task, "links": map[string]string{"self": "/api/v1/tasks/" + id}})
			return
		}
		writeJSON(w, 200, map[string]any{"items": []any{}})
	})
	mux.HandleFunc("/api/v1/tasks/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/tasks/")
		if t, ok := f.tasks[id]; ok {
			writeJSON(w, 200, t)
			return
		}
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "task not found"}})
	})
	mux.HandleFunc("/api/v1/runs/run-conflict/cancel", func(w http.ResponseWriter, r *http.Request) {
		f.cancelHits["run-conflict"]++
		writeJSON(w, 409, map[string]any{"error": map[string]any{"code": "reconciliation_in_progress", "message": "busy"}})
	})
	mux.HandleFunc("/api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{
			"items": []any{
				map[string]any{"split_group_id": "g1", "state": "open", "member_count": 2,
					"canonical_count": 0, "failed_count": 0, "created_at": "2025-07-03T11:00:00Z"},
			},
			"ordering": "created_at_desc",
			"filters":  map[string]any{"state": ""},
		})
	})
	mux.HandleFunc("/api/v1/groups/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/groups/")
		if strings.HasSuffix(path, "/close") && r.Method == http.MethodPost {
			id := strings.TrimSuffix(path, "/close")
			writeJSON(w, 200, map[string]any{"split_group_id": id, "state": "complete",
				"member_count": 2, "canonical_count": 2, "failed_count": 0,
				"summary": "members=2 canonical=2"})
			return
		}
		writeJSON(w, 200, map[string]any{"split_group_id": path, "state": "open",
			"member_count": 1, "canonical_count": 0, "failed_count": 0})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

// runCLI invokes the CLI with the given args and returns (exit, stdout, stderr).
func runCLI(t *testing.T, baseURL string, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"--api-url", baseURL, "--output", "json"}, args...)
	var stdout, stderr bytes.Buffer
	code := run(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestCLI_Submit_M7 — submit succeeds, prints task JSON, exits 0.
func TestCLI_Submit_M7(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL,
		"submit", "--station", "S1",
		"--start", "2025-07-03T11:00:00Z",
		"--end", "2025-07-03T11:15:00Z",
		"--split-group", "g7")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "task-1") {
		t.Errorf("stdout missing task_id: %q", out)
	}
}

// TestCLI_TaskGet_NotFound_ExitCode_M7 — Spec §6.10: 404 → exit 4.
func TestCLI_TaskGet_NotFound_ExitCode_M7(t *testing.T) {
	api := newFakeAPI(t)
	code, _, _ := runCLI(t, api.server.URL, "task", "get", "missing")
	if code != ExitNotFound {
		t.Errorf("exit = %d, want %d", code, ExitNotFound)
	}
}

// TestCLI_Cancel_RequiresYes_M7 — operator safety: refuse without --yes.
func TestCLI_Cancel_RequiresYes_M7(t *testing.T) {
	api := newFakeAPI(t)
	code, _, _ := runCLI(t, api.server.URL, "cancel", "run-x")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
}

// TestCLI_Cancel_ReconciliationInProgress_ExitCode_M7 — 409
// reconciliation_in_progress → exit 5 (conflict). Spec §6.10.
func TestCLI_Cancel_ReconciliationInProgress_ExitCode_M7(t *testing.T) {
	api := newFakeAPI(t)
	code, _, errs := runCLI(t, api.server.URL, "cancel", "--yes", "run-conflict")
	if code != ExitConflict {
		t.Fatalf("exit = %d (want %d, stderr=%s)", code, ExitConflict, errs)
	}
	if api.cancelHits["run-conflict"] == 0 {
		t.Error("cancel endpoint not hit")
	}
}

// TestCLI_GroupList_M7 — group list returns JSON envelope.
func TestCLI_GroupList_M7(t *testing.T) {
	api := newFakeAPI(t)
	code, out, _ := runCLI(t, api.server.URL, "group", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "g1") {
		t.Errorf("missing group id in output: %s", out)
	}
}

// TestCLI_GroupClose_M7 — group close transitions to complete.
func TestCLI_GroupClose_M7(t *testing.T) {
	api := newFakeAPI(t)
	code, out, _ := runCLI(t, api.server.URL, "group", "close", "g1")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, `"state": "complete"`) {
		t.Errorf("expected state=complete in JSON: %s", out)
	}
}

// TestCLI_Version_NoNetwork_M7 — local version command never hits the API.
func TestCLI_Version_NoNetwork_M7(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", "http://invalid.invalid:9", "version"}, &stdout, &stderr)
	if code != ExitOK {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "veriproc-cli") {
		t.Errorf("missing version string: %s", stdout.String())
	}
}

// TestCLI_UnknownCommand_M7 — usage error → exit 2.
func TestCLI_UnknownCommand_M7(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"frobnicate"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
}

// TestCLI_OutputFormatYAML_M7 — --output yaml emits key/value lines.
func TestCLI_OutputFormatYAML_M7(t *testing.T) {
	api := newFakeAPI(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--api-url", api.server.URL, "--output", "yaml",
		"group", "get", "g1",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "split_group_id: g1") {
		t.Errorf("yaml output missing key: %s", stdout.String())
	}
}
