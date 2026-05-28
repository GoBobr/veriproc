package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/auth"
	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/executor"
	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/httpapi"
	runspkg "github.com/eum/veriproc/internal/runs"
	stationssvc "github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
	"github.com/eum/veriproc/internal/tasks"
)

type stationAPI struct {
	srv      *httptest.Server
	st       *store.Store
	tasks    *tasks.Service
	runs     *runspkg.Service
	dispatch *runspkg.Dispatcher
	tok      string
	tok2     string
}

func newStationAPI(t *testing.T, withAuth bool) *stationAPI {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "stations-api.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stationssvc.NewRegistry()
	if err := reg.Seed(context.Background(), st,
		stationssvc.Spec{StationID: "SCENE-L2", StationName: "SCE_2",
			ContentHash: "sha256:scene-l2", SchemaVersion: "veriproc.station/v1"},
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	taskN := 0
	taskIDs := func() string { taskN++; return fmt.Sprintf("%06x", taskN) }
	runN := 0
	runIDs := func() string { runN++; return "run-" + strconv.Itoa(runN) }
	now := time.Date(2025, 7, 3, 11, 50, 0, 0, time.UTC)
	tsvc := tasks.NewService(st, reg, func() time.Time { return now }, taskIDs)
	exec := executor.NewStubExecutor(func() time.Time { return now })
	rsvc := runspkg.NewService(runspkg.Config{
		Store: st, Executors: executor.NewSingleExecutorRegistry(exec), Resolver: reg,
		WorkingRootBase: t.TempDir(),
		Clock:           func() time.Time { return now },
		IDFactory:       runIDs,
	})
	ssvc := stationssvc.NewService(st, func() time.Time { return now })
	disp := runspkg.NewDispatcher(rsvc, time.Millisecond, zerolog.Nop())
	deps := httpapi.Deps{
		Config:   &config.Config{InstanceID: "test"},
		Health:   health.NewAggregator(time.Second),
		Logger:   zerolog.Nop(),
		Tasks:    tsvc,
		Runs:     rsvc,
		Stations: ssvc,
	}
	if withAuth {
		deps.Authn = auth.NewStaticAuthenticator(map[string]auth.Principal{
			"op-secret":     {Subject: "alice", Role: auth.RoleOperator},
			"reader-secret": {Subject: "bob", Role: auth.RoleReader},
		})
		deps.Quota = auth.NewQuotaEnforcer(time.Now)
	}
	srv := httptest.NewServer(httpapi.NewRouter(deps))
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return &stationAPI{srv: srv, st: st, tasks: tsvc, runs: rsvc, dispatch: disp, tok: "op-secret", tok2: "reader-secret"}
}

func (a *stationAPI) submitOne(t *testing.T) string {
	t.Helper()
	res, err := a.tasks.Submit(context.Background(), tasks.SubmitInput{
		Destination: tasks.Destination{StationID: "SCENE-L2"},
		Window: tasks.Window{
			Start: time.Date(2025, 7, 3, 11, 15, 0, 0, time.UTC),
			End:   time.Date(2025, 7, 3, 11, 30, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	return res.Task.TaskID
}

func (a *stationAPI) tickN(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := a.dispatch.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
}

func doReqJSONMap(t *testing.T, method, url, token string) (*http.Response, map[string]any) {
	t.Helper()
	resp, body := doReq(t, method, url, token)
	out := map[string]any{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("decode body: %v body=%s", err, body)
		}
	}
	return resp, out
}

func TestAPI_Stations_ListAndSummary(t *testing.T) {
	a := newStationAPI(t, false)
	completedTaskID := a.submitOne(t)
	a.tickN(t, 6)
	queuedTaskID := a.submitOne(t)

	resp, body := getJSONMap(t, a.srv, "/api/v1/stations")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	station := items[0].(map[string]any)
	if station["station_id"] != "SCENE-L2" {
		t.Fatalf("station_id = %v", station["station_id"])
	}
	if station["paused"] != false {
		t.Fatalf("paused = %v, want false", station["paused"])
	}

	since := time.Date(2025, 7, 3, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)
	resp, body = getJSONMap(t, a.srv, "/api/v1/stations/summary?since="+since)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary status = %d body=%v", resp.StatusCode, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("summary items = %d, want 1", len(items))
	}
	summary := items[0].(map[string]any)
	if got := int(summary["queued_count"].(float64)); got != 1 {
		t.Fatalf("queued_count = %d, want 1", got)
	}
	counts := summary["counts"].(map[string]any)
	if got := int(counts["success"].(float64)); got != 1 {
		t.Fatalf("success count = %d, want 1", got)
	}
	slots, _ := summary["slots"].([]any)
	if len(slots) == 0 {
		t.Fatalf("expected at least one summary slot")
	}
	seenCompletedTask := false
	for _, raw := range slots {
		slot := raw.(map[string]any)
		if slot["task_id"] == completedTaskID {
			seenCompletedTask = true
		}
		if slot["task_id"] == queuedTaskID && slot["retry_index"] == nil {
			t.Fatalf("queued task slot missing retry identity: %v", slot)
		}
	}
	if !seenCompletedTask {
		t.Fatalf("completed task %s not present in slots: %v", completedTaskID, slots)
	}

	resp, body = getJSONMap(t, a.srv, "/api/v1/stations/SCENE-L2/summary?since="+since)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("station summary status = %d body=%v", resp.StatusCode, body)
	}
	stationSummary := body["station"].(map[string]any)
	if stationSummary["station_id"] != "SCENE-L2" {
		t.Fatalf("station summary station_id = %v", stationSummary["station_id"])
	}
}

func TestAPI_Stations_PauseUnpause_AuthAndDispatch(t *testing.T) {
	a := newStationAPI(t, true)

	resp, body := doReqJSONMap(t, "POST", a.srv.URL+"/api/v1/stations/SCENE-L2/pause", a.tok2)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("reader pause status = %d body=%v, want 403", resp.StatusCode, body)
	}
	if code := errCode(t, mustJSONBody(t, body)); code != "unauthorized" {
		t.Fatalf("reader pause code = %s, want unauthorized", code)
	}

	resp, body = doReqJSONMap(t, "POST", a.srv.URL+"/api/v1/stations/SCENE-L2/pause", a.tok)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator pause status = %d body=%v", resp.StatusCode, body)
	}
	if body["paused"] != true {
		t.Fatalf("paused = %v, want true", body["paused"])
	}

	taskID := a.submitOne(t)
	a.tickN(t, 1)
	taskRec, err := a.st.Tasks().Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	runRec, err := a.st.Runs().Get(context.Background(), taskRec.LatestRunID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if runRec.State != "ready" {
		t.Fatalf("paused station run state = %s, want ready", runRec.State)
	}

	resp, body = doReqJSONMap(t, "POST", a.srv.URL+"/api/v1/stations/SCENE-L2/unpause", a.tok)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator unpause status = %d body=%v", resp.StatusCode, body)
	}
	if body["paused"] != false {
		t.Fatalf("paused = %v, want false", body["paused"])
	}
	a.tickN(t, 1)
	runRec, err = a.st.Runs().Get(context.Background(), taskRec.LatestRunID)
	if err != nil {
		t.Fatalf("get run after unpause: %v", err)
	}
	if runRec.State == "ready" {
		t.Fatalf("run remained ready after unpause")
	}
}

func mustJSONBody(t *testing.T, body map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	return raw
}
