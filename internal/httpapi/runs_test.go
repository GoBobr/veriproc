package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/config"
	"github.com/gobobr/veriproc/internal/executor"
	"github.com/gobobr/veriproc/internal/health"
	"github.com/gobobr/veriproc/internal/httpapi"
	runspkg "github.com/gobobr/veriproc/internal/runs"
	"github.com/gobobr/veriproc/internal/stations"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/gobobr/veriproc/internal/tasks"
	"github.com/rs/zerolog"
)

type runAPI struct {
	srv      *httptest.Server
	st       *store.Store
	tasks    *tasks.Service
	runs     *runspkg.Service
	dispatch *runspkg.Dispatcher
}

func newRunAPI(t *testing.T) *runAPI {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "runs-api.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	reg := stations.NewRegistry()
	if err := reg.Seed(context.Background(), st,
		stations.Spec{StationID: "SCENE-L2", StationName: "SCE_2",
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
	disp := runspkg.NewDispatcher(rsvc, time.Millisecond, zerolog.Nop())

	router := httpapi.NewRouter(httpapi.Deps{
		Config: &config.Config{InstanceID: "test"},
		Health: health.NewAggregator(time.Second),
		Logger: zerolog.Nop(),
		Tasks:  tsvc,
		Runs:   rsvc,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return &runAPI{srv: srv, st: st, tasks: tsvc, runs: rsvc, dispatch: disp}
}

func (a *runAPI) submitOne(t *testing.T) string {
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

func (a *runAPI) tickN(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := a.dispatch.Tick(context.Background()); err != nil {
			t.Fatalf("tick %d: %v", i, err)
		}
	}
}

func getJSONMap(t *testing.T, srv *httptest.Server, path string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatalf("get %s: %v", path, err)
	}
	body := readAll(t, resp)
	var out map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	return resp, out
}

// TestAPI_RunGet_5_4_3_5_5_4 — GET /runs/{id} returns RunDetail with all
// required Spec §5.5.4 fields, simplified public state, plus jobs/artifacts.
func TestAPI_RunGet_5_4_3_5_5_4(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)
	tk, _ := a.st.Tasks().Get(context.Background(), taskID)
	resp, body := getJSONMap(t, a.srv, "/api/v1/runs/"+tk.LatestRunID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%v", resp.StatusCode, body)
	}
	required := []string{"run_id", "task_id", "station_id", "start", "end", "station_revision_id", "state",
		"canonicality", "retry_index", "created_at"}
	for _, k := range required {
		if _, ok := body[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
	if body["state"] != "complete" {
		t.Errorf("public state = %v, want complete", body["state"])
	}
	if body["canonicality"] != "canonical" {
		t.Errorf("canonicality = %v, want canonical", body["canonicality"])
	}
	if jobs, ok := body["jobs"].([]any); !ok || len(jobs) == 0 {
		t.Errorf("no jobs in detail: %v", body["jobs"])
	}
	if arts, ok := body["artifacts"].([]any); !ok || len(arts) == 0 {
		t.Errorf("no artifacts in detail: %v", body["artifacts"])
	}
}

// TestAPI_RunGet_NotFound — unknown run id returns 404 with apierr envelope.
func TestAPI_RunGet_NotFound(t *testing.T) {
	a := newRunAPI(t)
	resp, body := getJSONMap(t, a.srv, "/api/v1/runs/no-such")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Errorf("no error envelope: %v", body)
	}
}

// TestAPI_RunList_Pagination_5_4_4_5_5_9 — list envelope must include items,
// page_size, ordering, filters, and next_cursor when more results exist.
func TestAPI_RunList_Pagination_5_4_4_5_5_9(t *testing.T) {
	a := newRunAPI(t)
	for i := 0; i < 3; i++ {
		a.submitOne(t)
	}
	a.tickN(t, 10)
	resp, body := getJSONMap(t, a.srv, "/api/v1/runs?limit=2")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%v", resp.StatusCode, body)
	}
	for _, k := range []string{"items", "page_size", "ordering", "filters"} {
		if _, ok := body[k]; !ok {
			t.Errorf("envelope missing %q: %v", k, body)
		}
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Errorf("page items = %d, want 2", len(items))
	}
	if body["next_cursor"] == nil || body["next_cursor"] == "" {
		t.Errorf("expected next_cursor; body=%v", body)
	}
}

func TestAPI_RunDispatchedReportsQueued(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	ctx := context.Background()
	r, _ := a.runs.PrepareRun(ctx, taskID)
	if _, err := a.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	resp, body := getJSONMap(t, a.srv, "/api/v1/runs/"+r.RunID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%v", resp.StatusCode, body)
	}
	if body["state"] != "queued" {
		t.Fatalf("public state = %v, want queued", body["state"])
	}
	if body["internal_state"] != "dispatched" {
		t.Fatalf("internal_state = %v, want dispatched", body["internal_state"])
	}

	resp, body = getJSONMap(t, a.srv, "/api/v1/tasks/"+taskID+"/runs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("task runs status = %d, body=%v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("task run items = %d, want 1: %v", len(items), body)
	}
	item := items[0].(map[string]any)
	if item["state"] != "queued" {
		t.Fatalf("task run public state = %v, want queued", item["state"])
	}

	resp, body = getJSONMap(t, a.srv, "/api/v1/runs?state=queued")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("queued list status = %d, body=%v", resp.StatusCode, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("queued list items = %d, want 1: %v", len(items), body)
	}
}

// TestAPI_RunList_InvalidLimit_5_7 — invalid query param → 400 invalid_request.
func TestAPI_RunList_InvalidLimit_5_7(t *testing.T) {
	a := newRunAPI(t)
	resp, body := getJSONMap(t, a.srv, "/api/v1/runs?limit=abc")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "invalid_request" {
		t.Errorf("code = %v, want invalid_request", errObj["code"])
	}
}

func TestAPI_TaskRunsAlias(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)

	resp, body := getJSONMap(t, a.srv, "/api/v1/tasks/"+taskID+"/runs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatalf("expected run items")
	}
	for _, raw := range items {
		it := raw.(map[string]any)
		if it["task_id"] != taskID {
			t.Fatalf("task_id = %v, want %s", it["task_id"], taskID)
		}
	}
}

// TestAPI_RunArtifacts_5_4_6_5_5_6 — GET /runs/{id}/artifacts returns the
// artifact representation per §5.5.6 (must include availability + logical_type).
func TestAPI_RunArtifacts_5_4_6_5_5_6(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)
	tk, _ := a.st.Tasks().Get(context.Background(), taskID)
	resp, body := getJSONMap(t, a.srv, "/api/v1/runs/"+tk.LatestRunID+"/artifacts")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body=%v", resp.StatusCode, body)
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no artifacts")
	}
	first := items[0].(map[string]any)
	for _, k := range []string{"artifact_id", "logical_type", "availability", "created_at"} {
		if _, ok := first[k]; !ok {
			t.Errorf("missing artifact field %q", k)
		}
	}
}

// TestAPI_RunLogs_5_4_7 — /runs/{id}/logs returns log artifacts only.
func TestAPI_RunLogs_5_4_7(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)
	tk, _ := a.st.Tasks().Get(context.Background(), taskID)
	resp, body := getJSONMap(t, a.srv, "/api/v1/runs/"+tk.LatestRunID+"/logs")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	items, _ := body["items"].([]any)
	if len(items) == 0 {
		t.Fatal("no log artifacts")
	}
	for _, it := range items {
		if it.(map[string]any)["logical_type"] != "log" {
			t.Errorf("non-log artifact returned: %v", it)
		}
	}
}

// TestAPI_JobGet_5_4_5_5_5_5 — /jobs/{id} returns required JobSummary fields.
func TestAPI_JobGet_5_4_5_5_5_5(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)
	tk, _ := a.st.Tasks().Get(context.Background(), taskID)
	jobs, _ := a.st.Jobs().ListByRun(context.Background(), tk.LatestRunID)
	if len(jobs) == 0 {
		t.Fatal("no jobs")
	}
	resp, body := getJSONMap(t, a.srv, "/api/v1/jobs/"+jobs[0].JobID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	for _, k := range []string{"job_id", "run_id", "executor_type", "scheduler_native_state", "submitted_at"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing field %q in job: %v", k, body)
		}
	}
}

// TestAPI_ArtifactContent — fetching artifact bytes returns the stored file.
func TestAPI_ArtifactContent(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)
	tk, _ := a.st.Tasks().Get(context.Background(), taskID)
	arts, _ := a.st.Artifacts().ListByRun(context.Background(), tk.LatestRunID, "log")
	var artifactID string
	for _, art := range arts {
		if art.Size > 0 {
			artifactID = art.ArtifactID
			break
		}
	}
	if artifactID == "" {
		t.Fatalf("no non-empty log artifact: %#v", arts)
	}
	resp, err := a.srv.Client().Get(a.srv.URL + "/api/v1/artifacts/" + artifactID + "/content")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%s", resp.StatusCode, body)
	}
	if len(body) == 0 {
		t.Error("empty content")
	}
}

func TestAPI_DirectoryArtifactContentRejected(t *testing.T) {
	a := newRunAPI(t)
	dir := filepath.Join(t.TempDir(), "dir-artifact")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir artifact dir: %v", err)
	}
	artifact := &store.ArtifactRecord{ArtifactID: "art-dir-content", LogicalType: "output", ObjectKind: store.ObjectKindDirectory, Path: dir, Availability: "available"}
	if err := a.st.Artifacts().Insert(context.Background(), artifact); err != nil {
		t.Fatalf("insert artifact: %v", err)
	}
	resp, body := getJSONMap(t, a.srv, "/api/v1/artifacts/"+artifact.ArtifactID+"/content")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%v", resp.StatusCode, body)
	}
	if body["error"] == nil {
		t.Fatalf("missing error envelope: %v", body)
	}
}

// TestAPI_TaskShowsLatestAndCanonical_5_5_1 — after finalization the task
// representation exposes both latest_retry_index/latest_run_ref and
// canonical_retry_index/canonical_run_ref (Spec §3.6.1, §5.5.1 updated).
func TestAPI_TaskShowsLatestAndCanonical_5_5_1(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 6)
	resp, body := getJSONMap(t, a.srv, "/api/v1/tasks/"+taskID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["latest_retry_index"] == nil {
		t.Errorf("missing latest_retry_index: %v", body)
	}
	if body["latest_run_ref"] == nil {
		t.Errorf("missing latest_run_ref: %v", body)
	}
	if body["canonical_retry_index"] == nil {
		t.Errorf("missing canonical_retry_index: %v", body)
	}
	if body["canonical_run_ref"] == nil {
		t.Errorf("missing canonical_run_ref: %v", body)
	}
}

// TestAPI_RunNotComplete_BeforeFinalization_5_6 — verifies the Spec §5.6
// completion gate at the API surface: a run whose underlying executor has
// reported success but which has not yet been finalized must NOT be reported
// as state=complete.
func TestAPI_RunNotComplete_BeforeFinalization_5_6(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	ctx := context.Background()
	r, _ := a.runs.PrepareRun(ctx, taskID)
	if _, err := a.runs.Dispatch(ctx, r.RunID); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	// Poll twice: queued → running. Do not poll a third time (would succeed)
	// nor finalize. Run must report state=running.
	if _, err := a.runs.Poll(ctx, r.RunID); err != nil {
		t.Fatalf("poll1: %v", err)
	}
	if _, err := a.runs.Poll(ctx, r.RunID); err != nil {
		t.Fatalf("poll2: %v", err)
	}
	// Third poll observes succeeded → run becomes finalizing, NOT complete.
	if _, err := a.runs.Poll(ctx, r.RunID); err != nil {
		t.Fatalf("poll3: %v", err)
	}
	resp, body := getJSONMap(t, a.srv, "/api/v1/runs/"+r.RunID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["state"] == "complete" {
		t.Errorf("run reported complete before finalization (Spec §5.6 violated): %v", body)
	}
	if body["state"] != "finalizing" && body["state"] != "running" {
		t.Errorf("unexpected pre-finalize state: %v", body["state"])
	}
}
