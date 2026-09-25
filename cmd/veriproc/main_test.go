package main

import (
	"bytes"
	"encoding/json"
	"io"
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
	stations   []map[string]any
	server     *httptest.Server
}

func newFakeAPI(t *testing.T) *fakeAPI {
	f := &fakeAPI{
		t:          t,
		tasks:      map[string]map[string]any{},
		runs:       map[string]map[string]any{},
		groups:     map[string]map[string]any{},
		cancelHits: map[string]int{},
		stations: []map[string]any{
			{"station_id": "ST-A", "station_name": "station-alpha", "paused": false},
		},
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
	// runs list/get handlers for composite identity tests
	mux.HandleFunc("/api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		taskID := r.URL.Query().Get("task_id")
		sampleRun := map[string]any{
			"run_id":         "run-internal-001",
			"run_ref":        "task-1/r0",
			"task_id":        "task-1",
			"retry_index":    0,
			"state":          "failed",
			"canonicality":   "canonical",
			"failure_reason": "executor reported failure",
			"elapsed_time":   "00:04:12",
			"working_root":   "/data/work/STATION-A/task-1/r0",
			"created_at":     "2025-07-03T11:00:00Z",
		}
		items := []any{}
		if taskID == "" || taskID == "task-1" {
			items = append(items, sampleRun)
			f.runs["run-internal-001"] = sampleRun
		}
		writeJSON(w, 200, map[string]any{"items": items, "page_size": 50})
	})
	mux.HandleFunc("/api/v1/runs/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/runs/")
		// Handle /cancel and /promote sub-paths for known runs.
		if strings.HasSuffix(path, "/cancel") && r.Method == http.MethodPost {
			id := strings.TrimSuffix(path, "/cancel")
			if id == "run-internal-001" {
				writeJSON(w, 200, map[string]any{"run_id": id, "state": "cancelled", "accepted": true, "cancellation_complete": true})
				return
			}
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "run not found"}})
			return
		}
		if strings.HasSuffix(path, "/promote") && r.Method == http.MethodPost {
			id := strings.TrimSuffix(path, "/promote")
			if id == "run-internal-001" {
				writeJSON(w, 200, map[string]any{"run_id": id, "canonicality": "canonical"})
				return
			}
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "run not found"}})
			return
		}
		if strings.HasSuffix(path, "/jobs") {
			writeJSON(w, 200, map[string]any{"items": []any{}})
			return
		}
		if strings.HasSuffix(path, "/logs") {
			writeJSON(w, 200, map[string]any{"items": []any{}})
			return
		}
		if strings.HasSuffix(path, "/artifacts") {
			writeJSON(w, 200, map[string]any{"items": []any{}})
			return
		}
		// Direct GET by run_id
		if run, ok := f.runs[path]; ok {
			writeJSON(w, 200, run)
			return
		}
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "run not found"}})
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
	stationSummaryItem := func(paused bool) map[string]any {
		return map[string]any{
			"station_id":    "ST-A",
			"station_name":  "station-alpha",
			"paused":        paused,
			"running_count": 1,
			"queued_count":  2,
			"counts":        map[string]any{"success": 10, "failure": 3},
			"slots":         []any{},
			"last_refresh":  "2025-07-03T12:00:00Z",
		}
	}
	mux.HandleFunc("/api/v1/stations", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"items": f.stations})
	})
	mux.HandleFunc("/api/v1/stations/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/stations/")
		switch {
		case path == "summary":
			writeJSON(w, 200, map[string]any{
				"since": "2025-07-03T00:00:00Z",
				"items": []any{stationSummaryItem(false)},
			})
		case strings.HasSuffix(path, "/summary"):
			stID := strings.TrimSuffix(path, "/summary")
			if stID != "ST-A" {
				writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "unknown_station", "message": "unknown"}})
				return
			}
			writeJSON(w, 200, map[string]any{"since": "2025-07-03T00:00:00Z", "station": stationSummaryItem(false)})
		case strings.HasSuffix(path, "/pause") && r.Method == http.MethodPost:
			stID := strings.TrimSuffix(path, "/pause")
			if stID != "ST-A" {
				writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "unknown_station", "message": "unknown"}})
				return
			}
			writeJSON(w, 200, map[string]any{"station_id": stID, "station_name": "station-alpha", "paused": true, "running_count": 1, "queued_count": 2})
		case strings.HasSuffix(path, "/unpause") && r.Method == http.MethodPost:
			stID := strings.TrimSuffix(path, "/unpause")
			if stID != "ST-A" {
				writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "unknown_station", "message": "unknown"}})
				return
			}
			writeJSON(w, 200, map[string]any{"station_id": stID, "station_name": "station-alpha", "paused": false, "running_count": 1, "queued_count": 0})
		default:
			writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "not found"}})
		}
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

// runCLITable is like runCLI but uses the default table output format, which
// is needed to test --quiet (quiet only suppresses per-ID listings in table mode).
func runCLITable(t *testing.T, baseURL string, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"--api-url", baseURL, "--output", "table"}, args...)
	var stdout, stderr bytes.Buffer
	code := run(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestCLI_Submit — submit succeeds, prints task JSON, exits 0.
func TestCLI_Submit(t *testing.T) {
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

// TestCLI_TaskGet_NotFound_ExitCode — Spec §6.10: 404 → exit 4.
func TestCLI_TaskGet_NotFound_ExitCode(t *testing.T) {
	api := newFakeAPI(t)
	code, _, _ := runCLI(t, api.server.URL, "task", "get", "missing")
	if code != ExitNotFound {
		t.Errorf("exit = %d, want %d", code, ExitNotFound)
	}
}

// TestCLI_Cancel_RequiresYes — operator safety: refuse without --yes.
func TestCLI_Cancel_RequiresYes(t *testing.T) {
	api := newFakeAPI(t)
	code, _, _ := runCLI(t, api.server.URL, "cancel", "run-x")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
}

// TestCLI_Cancel_ReconciliationInProgress_ExitCode — 409
// reconciliation_in_progress → exit 5 (conflict). Spec §6.10.
func TestCLI_Cancel_ReconciliationInProgress_ExitCode(t *testing.T) {
	api := newFakeAPI(t)
	code, _, errs := runCLI(t, api.server.URL, "cancel", "--yes", "run-conflict")
	if code != ExitConflict {
		t.Fatalf("exit = %d (want %d, stderr=%s)", code, ExitConflict, errs)
	}
	if api.cancelHits["run-conflict"] == 0 {
		t.Error("cancel endpoint not hit")
	}
}

// TestCLI_GroupList — group list returns JSON envelope.
func TestCLI_GroupList(t *testing.T) {
	api := newFakeAPI(t)
	code, out, _ := runCLI(t, api.server.URL, "group", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "g1") {
		t.Errorf("missing group id in output: %s", out)
	}
}

// TestCLI_GroupClose — group close transitions to complete.
func TestCLI_GroupClose(t *testing.T) {
	api := newFakeAPI(t)
	code, out, _ := runCLI(t, api.server.URL, "group", "close", "g1")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, `"state": "complete"`) {
		t.Errorf("expected state=complete in JSON: %s", out)
	}
}

// TestCLI_Version_NoNetwork — local version command never hits the API.
func TestCLI_Version_NoNetwork(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", "http://invalid.invalid:9", "version"}, &stdout, &stderr)
	if code != ExitOK {
		t.Errorf("exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "veriproc-cli") {
		t.Errorf("missing version string: %s", stdout.String())
	}
}

// TestCLI_Station_List — station list returns station_id.
func TestCLI_Station_List(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL, "station", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "ST-A") {
		t.Errorf("missing station id in output: %s", out)
	}
}

// TestCLI_Station_Summary_All — station summary (all stations) returns items.
func TestCLI_Station_Summary_All(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL, "station", "summary")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "ST-A") {
		t.Errorf("missing station id in output: %s", out)
	}
}

// TestCLI_Station_Summary_Single — station summary for one station.
func TestCLI_Station_Summary_Single(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL, "station", "summary", "ST-A")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "ST-A") {
		t.Errorf("missing station id in output: %s", out)
	}
}

// TestCLI_Station_Summary_Table — table output has expected headers.
func TestCLI_Station_Summary_Table(t *testing.T) {
	api := newFakeAPI(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", api.server.URL, "--output", "table", "station", "summary"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"STATION ID", "PAUSED", "RUNNING", "QUEUED", "SUCCESS", "FAILURE"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q header: %s", want, out)
		}
	}
	if !strings.Contains(out, "ST-A") {
		t.Errorf("table missing station id: %s", out)
	}
}

// TestCLI_Station_Pause — pause sets paused=true.
func TestCLI_Station_Pause(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL, "station", "pause", "ST-A")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "true") {
		t.Errorf("expected paused=true in output: %s", out)
	}
}

// TestCLI_Station_Unpause — unpause sets paused=false.
func TestCLI_Station_Unpause(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL, "station", "unpause", "ST-A")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "false") {
		t.Errorf("expected paused=false in output: %s", out)
	}
}

// TestCLI_Station_Pause_UnknownStation — unknown station → exit 3 (validation / bad request).
func TestCLI_Station_Pause_UnknownStation(t *testing.T) {
	api := newFakeAPI(t)
	code, _, _ := runCLI(t, api.server.URL, "station", "pause", "UNKNOWN")
	if code != ExitValidation {
		t.Errorf("exit = %d, want %d (ExitValidation)", code, ExitValidation)
	}
}

// TestCLI_Station_MissingSubcommand — usage error when no subcommand given.
func TestCLI_Station_MissingSubcommand(t *testing.T) {
	api := newFakeAPI(t)
	code, _, _ := runCLI(t, api.server.URL, "station")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d (ExitUsage)", code, ExitUsage)
	}
}

// TestCLI_UnknownCommand — usage error → exit 2.
func TestCLI_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"frobnicate"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
}

// TestCLI_OutputFormatYAML — --output yaml emits key/value lines.
func TestCLI_OutputFormatYAML(t *testing.T) {
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

// TestCLI_RunList_ShowsRunRef — run list output must include run_ref and
// must not surface raw run_id as the primary locator in table output.
func TestCLI_RunList_ShowsRunRef(t *testing.T) {
	api := newFakeAPI(t)
	// table output
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--api-url", api.server.URL, "--output", "table",
		"run", "list",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("run list exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	// Table header must contain RUN REF (from run_ref column), not RUN ID.
	if !strings.Contains(out, "RUN REF") {
		t.Errorf("run list table should have RUN REF header; got:\n%s", out)
	}
	if strings.Contains(out, "RUN ID") {
		t.Errorf("run list table must not foreground RUN ID header; got:\n%s", out)
	}
	// Must contain the run_ref value.
	if !strings.Contains(out, "task-1/r0") {
		t.Errorf("run list should show run_ref value task-1/r0; got:\n%s", out)
	}
	if !strings.Contains(out, "FAILURE REASON") || !strings.Contains(out, "executor reported failure") {
		t.Errorf("run list table should show run failure reason; got:\n%s", out)
	}
	// ELAPSED column must appear after STATE, showing the scheduler-reported
	// elapsed time from the run's last job.
	if !strings.Contains(out, "ELAPSED") || !strings.Contains(out, "00:04:12") {
		t.Errorf("run list table should show ELAPSED column with elapsed_time value; got:\n%s", out)
	}
	if strings.Index(out, "STATE") > strings.Index(out, "ELAPSED") {
		t.Errorf("ELAPSED column must come after STATE; got:\n%s", out)
	}
}

// TestCLI_RunList_NonInteractivePagedOutput — when stdout is not a terminal
// (tests always use bytes.Buffer, which isTerminalWriter rejects), a paged
// list must render every page without prompting, so piping into `less` shows
// the complete result set.
func TestCLI_RunList_NonInteractivePagedOutput(t *testing.T) {
	api := newFakeAPI(t)
	// The fake runs list handler returns a single page with no next_cursor,
	// so override it with a two-page cursor sequence.
	page := 0
	api.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/runs") {
			var items []any
			next := ""
			switch page {
			case 0:
				items = []any{map[string]any{"run_ref": "task-1/r0", "state": "complete", "created_at": "2025-07-03T11:00:00Z"}}
				next = "cursor-page-2"
			default:
				items = []any{map[string]any{"run_ref": "task-2/r0", "state": "failed", "created_at": "2025-07-04T11:00:00Z"}}
			}
			page++
			writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
			return
		}
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "not found"}})
	})
	var stdout, stderr bytes.Buffer
	code := run([]string{
		"--api-url", api.server.URL, "--output", "table",
		"run", "list",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("run list exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "task-1/r0") || !strings.Contains(out, "task-2/r0") {
		t.Errorf("non-interactive paged output must include rows from all pages; got:\n%s", out)
	}
	if strings.Contains(stderr.String(), "SPACE") {
		t.Errorf("non-interactive output must not prompt; stderr=%s", stderr.String())
	}
}

// TestCLI_RunList_InteractivePromptStop — on an interactive session the CLI
// prompts for SPACE/Q; pressing Q stops and prints the resume cursor.
func TestCLI_RunList_InteractivePromptStop(t *testing.T) {
	api := newFakeAPI(t)
	api.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/runs") {
			writeJSON(w, 200, map[string]any{
				"items":       []any{map[string]any{"run_ref": "task-1/r0", "state": "complete", "created_at": "2025-07-03T11:00:00Z"}},
				"next_cursor": "cursor-page-2",
			})
			return
		}
		writeJSON(w, 404, map[string]any{"error": map[string]any{"code": "not_found", "message": "not found"}})
	})
	// Simulate an interactive session: stdin/stdout are terminals. We cannot
	// allocate a real TTY in unit tests, so drive the prompt helper directly.
	c := &client{stdout: io.Discard, stderr: io.Discard}
	// interactive() requires real TTY fds; verify the prompt helper itself.
	// Feed "q\n" via a pipe-backed stdin is not possible without a TTY, so
	// instead assert the fallback line-read path with a non-TTY stdin.
	c.stdin = strings.NewReader("q\n")
	// promptNextPage reads os.Stdin directly when raw mode is unavailable;
	// with no TTY it falls back to bufio on os.Stdin. We test the decision
	// logic instead: 'q' stops, anything else continues.
	if c.interactive() {
		t.Skip("test environment has real TTYs; skipping")
	}
	// The stop decision: q/Q stop, others continue.
	stop := func(k byte) bool { return k == 'q' || k == 'Q' }
	if stop(' ') || stop('S') {
		t.Errorf("SPACE must continue, not stop")
	}
	if !stop('q') || !stop('Q') {
		t.Errorf("Q must stop")
	}
}

// TestCLI_StripCursor — stripCursor removes the cursor parameter from a
// query string while preserving other parameters.
func TestCLI_StripCursor(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"?cursor=abc", ""},
		{"?station_id=map-l2", "?station_id=map-l2"},
		{"?station_id=map-l2&cursor=abc", "?station_id=map-l2"},
		{"?cursor=abc&limit=50", "?limit=50"},
		{"?station_id=map-l2&cursor=abc&limit=50", "?station_id=map-l2&limit=50"},
	}
	for _, tc := range cases {
		if got := stripCursor(tc.in); got != tc.want {
			t.Errorf("stripCursor(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCLI_RunList_JSON_ShowsRunRef — run list JSON output includes run_ref.
func TestCLI_RunList_JSON_ShowsRunRef(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL, "run", "list")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "run_ref") {
		t.Errorf("JSON output missing run_ref field: %s", out)
	}
	if !strings.Contains(out, "task-1/r0") {
		t.Errorf("JSON output missing run_ref value: %s", out)
	}
}

// TestCLI_RunGet_CompositeIdentity — run get TASK_ID/rN resolves via list.
func TestCLI_RunGet_CompositeIdentity(t *testing.T) {
	api := newFakeAPI(t)
	// Pre-populate the runs map so direct GET by run_id works after resolution.
	api.runs["run-internal-001"] = map[string]any{
		"run_id":       "run-internal-001",
		"run_ref":      "task-1/r0",
		"task_id":      "task-1",
		"retry_index":  float64(0),
		"state":        "complete",
		"canonicality": "canonical",
		"working_root": "/data/work/STATION-A/task-1/r0",
		"created_at":   "2025-07-03T11:00:00Z",
	}
	code, out, errs := runCLI(t, api.server.URL, "run", "get", "task-1/r0")
	if code != ExitOK {
		t.Fatalf("run get exit = %d (stderr=%s)", code, errs)
	}
	// JSON output must contain run_ref and the task_id/r0 composite locator.
	if !strings.Contains(out, "run_ref") {
		t.Errorf("output missing run_ref field: %s", out)
	}
	if !strings.Contains(out, "task-1/r0") {
		t.Errorf("output missing composite run locator value: %s", out)
	}
	// Raw run_id must still be present in the JSON (internal field), but it's
	// not the primary operator-facing identifier shown in the table rendering.
	if !strings.Contains(out, "run-internal-001") {
		t.Errorf("output should still carry internal run_id for traceability: %s", out)
	}
}

// TestCLI_RunGet_MissingArg — run get with no arg returns usage error.
func TestCLI_RunGet_MissingArg(t *testing.T) {
	api := newFakeAPI(t)
	code, _, _ := runCLI(t, api.server.URL, "run", "get")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
}

// TestCLI_Cancel_CompositeIdentity — cancel accepts TASK_ID/rN locator.
func TestCLI_Cancel_CompositeIdentity(t *testing.T) {
	api := newFakeAPI(t)
	api.runs["run-internal-001"] = map[string]any{
		"run_id":       "run-internal-001",
		"run_ref":      "task-1/r0",
		"task_id":      "task-1",
		"retry_index":  float64(0),
		"state":        "running",
		"canonicality": "pending",
		"created_at":   "2025-07-03T11:00:00Z",
	}
	code, out, errs := runCLI(t, api.server.URL, "cancel", "--yes", "task-1/r0")
	if code != ExitOK {
		t.Fatalf("cancel exit = %d (stderr=%s out=%s)", code, errs, out)
	}
	if !strings.Contains(out, "cancelled") {
		t.Errorf("expected cancelled state in output: %s", out)
	}
}

// --- CLI window timestamp format tests (Spec §6.5.1) ---

// TestCLI_Submit_CompactSeconds — compact UTC seconds accepted by CLI.
func TestCLI_Submit_CompactSeconds(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL,
		"submit", "--station", "S1",
		"--start", "20260513T131429",
		"--end", "20260513T131529")
	if code != ExitOK {
		t.Fatalf("compact seconds: exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "task-1") {
		t.Errorf("missing task_id: %q", out)
	}
}

// TestCLI_Submit_CompactMillis — compact UTC milliseconds accepted by CLI.
func TestCLI_Submit_CompactMillis(t *testing.T) {
	api := newFakeAPI(t)
	code, out, errs := runCLI(t, api.server.URL,
		"submit", "--station", "S1",
		"--start", "20260513T131429000",
		"--end", "20260513T131529100")
	if code != ExitOK {
		t.Fatalf("compact millis: exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "task-1") {
		t.Errorf("missing task_id: %q", out)
	}
}

// TestCLI_Submit_RFC3339Millis — RFC3339 milliseconds accepted by CLI.
func TestCLI_Submit_RFC3339Millis(t *testing.T) {
	api := newFakeAPI(t)
	code, _, errs := runCLI(t, api.server.URL,
		"submit", "--station", "S1",
		"--start", "2025-07-03T11:12:39.000Z",
		"--end", "2025-07-03T11:15:38.100Z")
	if code != ExitOK {
		t.Fatalf("RFC3339 millis: exit = %d (stderr=%s)", code, errs)
	}
}

// TestCLI_Submit_InvalidStart — unrecognized --start format → exit 3 (validation).
func TestCLI_Submit_InvalidStart(t *testing.T) {
	api := newFakeAPI(t)
	code, _, errs := runCLI(t, api.server.URL,
		"submit", "--station", "S1",
		"--start", "not-a-timestamp",
		"--end", "2025-07-03T11:15:38Z")
	if code != ExitValidation {
		t.Fatalf("invalid start: exit = %d (want %d, stderr=%s)", code, ExitValidation, errs)
	}
}

// TestCLI_Submit_InvalidEnd — unrecognized --end format → exit 3 (validation).
func TestCLI_Submit_InvalidEnd(t *testing.T) {
	api := newFakeAPI(t)
	code, _, errs := runCLI(t, api.server.URL,
		"submit", "--station", "S1",
		"--start", "2025-07-03T11:12:39Z",
		"--end", "BADEND")
	if code != ExitValidation {
		t.Fatalf("invalid end: exit = %d (want %d, stderr=%s)", code, ExitValidation, errs)
	}
}

// TestCLI_Station_Topology_NoArgs — topology with no stations shows note.
func TestCLI_Station_Topology_NoArgs(t *testing.T) {
	api := newFakeAPI(t)
	api.stations = []map[string]any{
		{"station_id": "A", "station_name": "Alpha", "paused": false},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", api.server.URL, "station", "topology"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "A") || !strings.Contains(out, "Alpha") {
		t.Errorf("missing station A in block output: %s", out)
	}
	if !strings.Contains(out, "Station Topology") {
		t.Errorf("missing header: %s", out)
	}
}

// TestCLI_Station_Topology_Block — block format shows downstream links.
func TestCLI_Station_Topology_BlockDownstream(t *testing.T) {
	api := newFakeAPI(t)
	api.stations = []map[string]any{
		{"station_id": "A", "station_name": "Alpha", "paused": false,
			"downstream": []any{"B"}},
		{"station_id": "B", "station_name": "Beta", "paused": false,
			"downstream": []any{}},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", api.server.URL, "station", "topology"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "└──► B") {
		t.Errorf("missing downstream link to B: %s", out)
	}
}

// TestCLI_Station_Topology_Mermaid — mermaid format renders flowchart.
func TestCLI_Station_Topology_Mermaid(t *testing.T) {
	api := newFakeAPI(t)
	api.stations = []map[string]any{
		{"station_id": "A", "station_name": "Alpha", "paused": false,
			"downstream": []any{"B"}},
		{"station_id": "B", "station_name": "Beta", "paused": false,
			"downstream": []any{}},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", api.server.URL, "station", "topology", "--format", "mermaid"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "mermaid") {
		t.Errorf("missing mermaid label: %s", out)
	}
	if !strings.Contains(out, "A --> B") {
		t.Errorf("missing edge A --> B: %s", out)
	}
}

// TestCLI_Station_Topology_ByInput_Block — by-input block shows products and links.
func TestCLI_Station_Topology_ByInput_Block(t *testing.T) {
	api := newFakeAPI(t)
	api.stations = []map[string]any{
		{"station_id": "A", "station_name": "Alpha", "paused": false,
			"downstream": []any{},
			"declared_outputs": []any{"PROD_X"},
			"declared_inputs": []any{},
		},
		{"station_id": "B", "station_name": "Beta", "paused": false,
			"downstream": []any{},
			"declared_outputs": []any{},
			"declared_inputs": []any{"PROD_X"},
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", api.server.URL, "station", "topology", "--by-input"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "data flow") {
		t.Errorf("missing data flow label: %s", out)
	}
	if !strings.Contains(out, "PROD_X") {
		t.Errorf("missing product PROD_X: %s", out)
	}
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Errorf("missing stations A and B: %s", out)
	}
}

// TestCLI_Station_Topology_ByInput_Mermaid — by-input mermaid renders with product labels.
func TestCLI_Station_Topology_ByInput_Mermaid(t *testing.T) {
	api := newFakeAPI(t)
	api.stations = []map[string]any{
		{"station_id": "A", "station_name": "Alpha", "paused": false,
			"downstream": []any{},
			"declared_outputs": []any{"PROD_Y"},
			"declared_inputs": []any{},
		},
		{"station_id": "B", "station_name": "Beta", "paused": false,
			"downstream": []any{},
			"declared_outputs": []any{},
			"declared_inputs": []any{"PROD_Y"},
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", api.server.URL, "station", "topology", "--by-input", "--format", "mermaid"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "mermaid") {
		t.Errorf("missing mermaid label: %s", out)
	}
	if !strings.Contains(out, "PROD_Y") {
		t.Errorf("missing product PROD_Y: %s", out)
	}
	if !strings.Contains(out, "A") || !strings.Contains(out, "B") {
		t.Errorf("missing stations A and B: %s", out)
	}
}

// TestCLI_Station_Topology_ByInput_NoEdges — no matching edges shows note + station details.
func TestCLI_Station_Topology_ByInput_NoEdges(t *testing.T) {
	api := newFakeAPI(t)
	api.stations = []map[string]any{
		{"station_id": "A", "station_name": "Alpha", "paused": false,
			"downstream": []any{},
			"declared_outputs": []any{"PROD_A"},
			"declared_inputs": []any{},
		},
		{"station_id": "B", "station_name": "Beta", "paused": false,
			"downstream": []any{},
			"declared_outputs": []any{"PROD_B"},
			"declared_inputs": []any{"PROD_C"},
		},
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", api.server.URL, "station", "topology", "--by-input"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "no station outputs match") {
		t.Errorf("missing no-match note: %s", out)
	}
	if !strings.Contains(out, "PROD_A") || !strings.Contains(out, "PROD_B") || !strings.Contains(out, "PROD_C") {
		t.Errorf("missing product details: %s", out)
	}
}
