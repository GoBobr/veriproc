package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// newRetireServer returns a stub daemon exposing the station retirement
// endpoints, recording the retire requests the CLI makes.
func newRetireServer(t *testing.T, missing []string, refuse, notFound string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	retired := []string{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/stations/missing", func(w http.ResponseWriter, r *http.Request) {
		items := make([]any, len(missing))
		for i, id := range missing {
			items[i] = id
		}
		writeJSON(w, 200, map[string]any{"items": items})
	})
	mux.HandleFunc("DELETE /api/v1/stations/{station_id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("station_id")
		if id == refuse {
			writeJSON(w, 409, map[string]any{"error": map[string]any{
				"code":    "invalid_state_transition",
				"message": "stations: station_id=" + id + " still has a config folder; remove it and restart the daemon first"}})
			return
		}
		if id == notFound {
			writeJSON(w, 404, map[string]any{"error": map[string]any{
				"code": "not_found", "message": "stations: station_id=" + id}})
			return
		}
		mu.Lock()
		retired = append(retired, id+"?purge="+r.URL.Query().Get("purge"))
		mu.Unlock()
		writeJSON(w, 200, map[string]any{"station_id": id, "retired": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &retired
}

// TestCLI_StationRemove_Success retires a ghost station and exits 0
// (Spec: Station Remove CLI Command — "CLI remove retires ghost station").
func TestCLI_StationRemove_Success(t *testing.T) {
	srv, retired := newRetireServer(t, nil, "", "")
	code, out, errs := runCLI(t, srv.URL, "station", "remove", "oldname")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "oldname") {
		t.Errorf("stdout missing station id: %q", out)
	}
	if len(*retired) != 1 || !strings.HasPrefix((*retired)[0], "oldname?purge=") {
		t.Errorf("retired requests = %v", *retired)
	}
}

// TestCLI_StationRemove_PurgeFlag forwarded to the API.
func TestCLI_StationRemove_PurgeFlag(t *testing.T) {
	srv, retired := newRetireServer(t, nil, "", "")
	code, _, errs := runCLI(t, srv.URL, "station", "remove", "--purge", "oldname")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if len(*retired) != 1 || (*retired)[0] != "oldname?purge=true" {
		t.Errorf("retired requests = %v, want purge=true", *retired)
	}
}

// TestCLI_StationRemove_RefusesActiveStation — 409 → nonzero exit with a
// named error (Spec: Station Remove CLI Command — "CLI remove refuses
// active station").
func TestCLI_StationRemove_RefusesActiveStation(t *testing.T) {
	srv, retired := newRetireServer(t, nil, "live", "")
	code, out, errs := runCLI(t, srv.URL, "station", "remove", "live")
	if code != ExitConflict {
		t.Fatalf("exit = %d, want %d", code, ExitConflict)
	}
	if !strings.Contains(errs, "live") || !strings.Contains(errs, "config folder") {
		t.Errorf("stderr missing named refusal: %q", errs)
	}
	if len(*retired) != 0 {
		t.Errorf("unexpected retire requests: %v", *retired)
	}
	_ = out
}

// TestCLI_StationRemove_NotFound — unknown station → 404 exit code.
func TestCLI_StationRemove_NotFound(t *testing.T) {
	srv, _ := newRetireServer(t, nil, "", "nosuch")
	code, _, _ := runCLI(t, srv.URL, "station", "remove", "nosuch")
	if code != ExitNotFound {
		t.Fatalf("exit = %d, want %d", code, ExitNotFound)
	}
}

// TestCLI_StationPrune_Yes retires all missing-folder stations
// (Spec: Station Prune CLI Command — "Prune retires all missing-folder
// stations").
func TestCLI_StationPrune_Yes(t *testing.T) {
	srv, retired := newRetireServer(t, []string{"ghost1", "ghost2"}, "", "")
	code, out, errs := runCLI(t, srv.URL, "station", "prune", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "ghost1") || !strings.Contains(out, "ghost2") {
		t.Errorf("stdout should list pruned stations: %q", out)
	}
	if len(*retired) != 2 {
		t.Errorf("retired requests = %v, want 2", *retired)
	}
}

// TestCLI_StationPrune_NoConfirmation makes no retire requests
// (Spec: Station Prune CLI Command — "Prune without confirmation makes no
// changes").
func TestCLI_StationPrune_NoConfirmation(t *testing.T) {
	srv, retired := newRetireServer(t, []string{"ghost1"}, "", "")
	// stdin is /dev/null under go test: the confirm prompt reads EOF and
	// declines, so no DELETE may be issued.
	code, out, errs := runCLI(t, srv.URL, "station", "prune")
	if code != ExitOK {
		t.Fatalf("exit = %d (stderr=%s)", code, errs)
	}
	if !strings.Contains(out, "ghost1") {
		t.Errorf("stdout should list the stations that would be retired: %q", out)
	}
	if !strings.Contains(out, "aborted") {
		t.Errorf("stdout should report the abort: %q", out)
	}
	if len(*retired) != 0 {
		t.Errorf("retire requests issued without confirmation: %v", *retired)
	}
}

// TestCLI_StationPrune_None reports nothing to do.
func TestCLI_StationPrune_None(t *testing.T) {
	srv, retired := newRetireServer(t, nil, "", "")
	code, out, _ := runCLI(t, srv.URL, "station", "prune", "--yes")
	if code != ExitOK {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out, "no stations to prune") {
		t.Errorf("stdout = %q", out)
	}
	if len(*retired) != 0 {
		t.Errorf("unexpected retire requests: %v", *retired)
	}
}
