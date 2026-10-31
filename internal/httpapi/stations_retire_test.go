package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/cleaner"
	"github.com/gobobr/veriproc/internal/config"
	"github.com/gobobr/veriproc/internal/health"
	"github.com/gobobr/veriproc/internal/httpapi"
	stationssvc "github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
)

// retireAPI is a minimal harness for the station retirement endpoints. The
// stations service is wired to a registry holding only the "live" station,
// simulating a daemon restarted after the "ghost" station's folder was
// renamed away.
type retireAPI struct {
	srv *httptest.Server
	st  *store.Store
	reg *stationssvc.Registry
}

func newRetireAPI(t *testing.T, withCleaner bool) *retireAPI {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "retire-api.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stationssvc.NewRegistry()
	ctx := context.Background()
	if err := reg.Seed(ctx, st,
		stationssvc.Spec{StationID: "live", StationName: "Live",
			ContentHash: "sha256:live", SchemaVersion: "veriproc.station/v1"},
	); err != nil {
		t.Fatalf("seed live: %v", err)
	}
	// Ghost station: persisted in the store but never loaded into the
	// registry (its folder is gone).
	if err := st.Stations().Insert(ctx, &store.StationRevisionRecord{
		StationID: "ghost", StationName: "Ghost",
		RevisionID: "rev-ghost", ContentHash: "sha256:ghost",
		SchemaVersion: "veriproc.station/v1",
	}); err != nil {
		t.Fatalf("insert ghost: %v", err)
	}
	ssvc := stationssvc.NewService(st, nil)
	ssvc.SetConfigSource(reg, reg, nil, nil)
	deps := httpapi.Deps{
		Config:   &config.Config{InstanceID: "test"},
		Health:   health.NewAggregator(time.Second),
		Logger:   zerolog.Nop(),
		Stations: ssvc,
	}
	if withCleaner {
		deps.Cleaner = cleaner.New(st, zerolog.Nop())
	}
	srv := httptest.NewServer(httpapi.NewRouter(deps))
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return &retireAPI{srv: srv, st: st, reg: reg}
}

// seedGhostTaskRun adds a task+run with working root for station ghost.
func (a *retireAPI) seedGhostTaskRun(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "wr-ghost-1")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := a.st.Tasks().Insert(ctx, &store.TaskRecord{
		TaskID: "task-ghost-1", SchemaVersion: "veriproc.task-submission/v1",
		DestinationStationID: "ghost",
		WindowStart:          time.Date(2025, 7, 3, 0, 0, 0, 0, time.UTC),
		WindowEnd:            time.Date(2025, 7, 3, 1, 0, 0, 0, time.UTC),
		ClientMetadata:       json.RawMessage(`{}`),
		RoutingContent:       json.RawMessage(`{"destination":{"station_id":"ghost"}}`),
		RoutingContentHash:   "sha256:routing-ghost-1",
		SubmissionOrigin:     "client", State: "accepted",
	}); err != nil {
		t.Fatalf("task insert: %v", err)
	}
	if err := a.st.Runs().Insert(ctx, &store.RunRecord{
		RunID: "run-ghost-1", TaskID: "task-ghost-1", StationRevisionID: "rev-ghost",
		RetryIndex: 0, WorkingRoot: root, State: "succeeded", Canonicality: "canonical",
	}); err != nil {
		t.Fatalf("run insert: %v", err)
	}
	return root
}

func (a *retireAPI) stationIDs(t *testing.T) []string {
	t.Helper()
	items, _, err := getJSONList(t, a.srv.URL+"/api/v1/stations")
	if err != nil {
		t.Fatalf("list stations: %v", err)
	}
	ids := []string{}
	for _, it := range items {
		if m, ok := it.(map[string]any); ok {
			if id, ok := m["station_id"].(string); ok {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func getJSONList(t *testing.T, url string) ([]any, *http.Response, error) {
	t.Helper()
	resp, body := doReq(t, http.MethodGet, url, "")
	if resp.StatusCode != http.StatusOK {
		return nil, resp, errors.New("status " + resp.Status + " body=" + string(body))
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, resp, err
	}
	raw, _ := m["items"].([]any)
	return raw, resp, nil
}

// TestAPI_StationRetire_KeepsRuns — retire without purge hides the ghost
// station but its runs stay queryable (Spec: Station Retirement Endpoint —
// "Retire via API keeps runs").
func TestAPI_StationRetire_KeepsRuns(t *testing.T) {
	a := newRetireAPI(t, false)
	_ = a.seedGhostTaskRun(t)

	resp, body := doReq(t, http.MethodDelete, a.srv.URL+"/api/v1/stations/ghost", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", resp.StatusCode, body)
	}
	ids := a.stationIDs(t)
	for _, id := range ids {
		if id == "ghost" {
			t.Error("ghost still listed after retire")
		}
		if id != "live" {
			t.Errorf("unexpected station %q in listing", id)
		}
	}
	// Run preserved and queryable.
	if _, err := a.st.Runs().Get(context.Background(), "run-ghost-1"); err != nil {
		t.Errorf("run must remain queryable after retire: %v", err)
	}

	// Idempotent: second retire succeeds.
	resp, body = doReq(t, http.MethodDelete, a.srv.URL+"/api/v1/stations/ghost", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("re-retire status = %d, body=%s", resp.StatusCode, body)
	}
}

// TestAPI_StationRetire_Purge — purge deletes tasks, runs, and working
// roots before retiring (Spec: Station Retirement Endpoint — "Purge removes
// station data").
func TestAPI_StationRetire_Purge(t *testing.T) {
	a := newRetireAPI(t, true)
	root := a.seedGhostTaskRun(t)

	resp, body := doReq(t, http.MethodDelete, a.srv.URL+"/api/v1/stations/ghost?purge=true", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d, body=%s", resp.StatusCode, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if m["station_id"] != "ghost" {
		t.Errorf("station_id = %v, want ghost", m["station_id"])
	}
	if _, err := a.st.Runs().Get(context.Background(), "run-ghost-1"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run should be purged, got err=%v", err)
	}
	if fi, err := os.Stat(root); err == nil {
		_ = fi
		t.Error("working root still on disk after purge")
	}
	for _, id := range a.stationIDs(t) {
		if id == "ghost" {
			t.Error("ghost still listed after purge+retire")
		}
	}
}

// TestAPI_StationRetire_RefusesActiveStation — a station still loaded from
// disk cannot be retired (Spec: Station Retirement Endpoint / Station
// Remove CLI — active-station refusal).
func TestAPI_StationRetire_RefusesActiveStation(t *testing.T) {
	a := newRetireAPI(t, false)

	resp, body := doReq(t, http.MethodDelete, a.srv.URL+"/api/v1/stations/live", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body=%s", resp.StatusCode, body)
	}
	// Station must still be listed.
	found := false
	for _, id := range a.stationIDs(t) {
		if id == "live" {
			found = true
		}
	}
	if !found {
		t.Error("live station vanished from listing after refused retire")
	}
}

// TestAPI_StationRetire_UnknownStation — 404 with error envelope (Spec:
// Station Retirement Endpoint — "Unknown station rejected").
func TestAPI_StationRetire_UnknownStation(t *testing.T) {
	a := newRetireAPI(t, false)

	resp, body := doReq(t, http.MethodDelete, a.srv.URL+"/api/v1/stations/nosuch", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", resp.StatusCode, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	errDetail, _ := m["error"].(map[string]any)
	if errDetail["code"] != "not_found" {
		t.Errorf("error code = %v, want not_found", errDetail["code"])
	}
}

// TestAPI_StationMissing — lists store-known stations without folders and
// excludes retired ones (Spec: Station Prune CLI Command discovery).
func TestAPI_StationMissing(t *testing.T) {
	a := newRetireAPI(t, false)

	resp, body := doReq(t, http.MethodGet, a.srv.URL+"/api/v1/stations/missing", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, _ := m["items"].([]any)
	if len(items) != 1 || items[0] != "ghost" {
		t.Errorf("missing items = %v, want [ghost]", items)
	}

	// After retiring, the ghost is no longer reported missing.
	if _, err := doReq(t, http.MethodDelete, a.srv.URL+"/api/v1/stations/ghost", ""); err != nil && resp.StatusCode != http.StatusOK {
		t.Fatalf("retire: %v", err)
	}
	resp, body = doReq(t, http.MethodGet, a.srv.URL+"/api/v1/stations/missing", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items, _ = m["items"].([]any)
	if len(items) != 0 {
		t.Errorf("missing items after retire = %v, want empty", items)
	}
}
