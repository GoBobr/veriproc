package console

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func nowForTest() time.Time {
	return time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
}

func newTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "console.db")
	db, err := OpenDB("file:" + path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// fakeUpstream is a deterministic in-memory UpstreamClient used by gateway
// handler tests. It records the calls it receives so assertions can verify
// upstream interactions.
type fakeUpstream struct {
	mu           sync.Mutex
	stations     []StationListItem
	summary      StationsSummary
	pauseCalls   []string
	unpauseCalls []string
	submitCalls  int
	submitErr    error
	retryCalls   int
	cancelCalls  int
	cancelErr    error
	taskRuns     map[string][]map[string]any
	tasks        map[string]map[string]any
	runs         map[string]map[string]any
	health       error
}

func (f *fakeUpstream) Health(_ context.Context) error { return f.health }
func (f *fakeUpstream) GetHealth(_ context.Context) (map[string]any, error) {
	if f.health != nil {
		return nil, f.health
	}
	return map[string]any{"status": "ok", "version": "test", "api_version": "v1"}, nil
}

func (f *fakeUpstream) Stations(_ context.Context) ([]StationListItem, error) {
	return f.stations, nil
}
func (f *fakeUpstream) StationsSummary(_ context.Context, _ time.Time) (StationsSummary, error) {
	return f.summary, nil
}
func (f *fakeUpstream) StationSummary(_ context.Context, _ string, _ time.Time) (StationSummary, error) {
	if len(f.summary.Items) == 0 {
		return StationSummary{}, nil
	}
	return f.summary.Items[0], nil
}
func (f *fakeUpstream) PauseStation(_ context.Context, id string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pauseCalls = append(f.pauseCalls, id)
	return map[string]any{"station_id": id, "paused": true}, nil
}
func (f *fakeUpstream) UnpauseStation(_ context.Context, id string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unpauseCalls = append(f.unpauseCalls, id)
	return map[string]any{"station_id": id, "paused": false}, nil
}
func (f *fakeUpstream) SubmitTask(_ context.Context, body map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submitCalls++
	if f.submitErr != nil {
		return nil, f.submitErr
	}
	return map[string]any{"task_id": "TASK-1", "echo": body}, nil
}
func (f *fakeUpstream) GetTask(_ context.Context, taskID string) (map[string]any, error) {
	if t, ok := f.tasks[taskID]; ok {
		return t, nil
	}
	return nil, &UpstreamError{Status: 404, Body: "not found"}
}
func (f *fakeUpstream) RetryTask(_ context.Context, taskID string, _ map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retryCalls++
	return map[string]any{"task_id": taskID, "retry": true}, nil
}
func (f *fakeUpstream) ListTasks(_ context.Context, _ url.Values) ([]map[string]any, error) {
	return nil, nil
}
func (f *fakeUpstream) ListTaskRuns(_ context.Context, taskID string) ([]map[string]any, error) {
	return f.taskRuns[taskID], nil
}
func (f *fakeUpstream) GetRun(_ context.Context, runID string) (map[string]any, error) {
	if r, ok := f.runs[runID]; ok {
		return r, nil
	}
	return nil, &UpstreamError{Status: 404, Body: "not found"}
}
func (f *fakeUpstream) ListRunJobs(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"items": []any{}}, nil
}
func (f *fakeUpstream) ListRunArtifacts(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"items": []any{}}, nil
}
func (f *fakeUpstream) ListRunLogs(_ context.Context, _ string) (map[string]any, error) {
	return map[string]any{"items": []any{}}, nil
}
func (f *fakeUpstream) CancelRun(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelCalls++
	if f.cancelErr != nil {
		return nil, f.cancelErr
	}
	return map[string]any{"cancelled": true}, nil
}

// newTestGateway returns a fully wired gateway with a single fake instance.
func newTestGateway(t *testing.T, fake *fakeUpstream) (*Gateway, *DB) {
	t.Helper()
	tmp := t.TempDir()
	db := newTestDB(t)
	cfg := &Config{
		SchemaVersion: SchemaVersionExpected,
		HTTP:          HTTPConfig{BindAddr: "127.0.0.1:0"},
		UI: UIConfig{
			RefreshInterval:        5 * time.Second,
			VisibleSlotCount:       12,
			CompletedVisibility:    FlexDuration{D: 30 * time.Second, Set: true},
			DefaultStatsSince:      time.Hour,
			UpstreamSummaryTimeout: 30 * time.Second,
			PreviewMaxBytes:        1024,
		},
		Instances: []InstanceConfig{{
			ID: "vp1", Title: "VP1", BaseURL: "http://upstream",
			WorkingRootBase:   tmp,
			AllowedRoots:      []string{tmp},
			UpstreamTimeoutMS: 1000,
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	gw, err := NewGateway(Options{
		Config: cfg,
		DB:     db,
		Logger: zerolog.New(io.Discard),
		NewClient: func(_ InstanceConfig) (UpstreamClient, error) {
			return fake, nil
		},
		Now: nowForTest,
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	// Register tokens directly.
	if err := db.RegisterToken(context.Background(), HashToken("viewertok"), "view@local", string(RoleViewer), nowForTest()); err != nil {
		t.Fatal(err)
	}
	if err := db.RegisterToken(context.Background(), HashToken("operatortok"), "op@local", string(RoleOperator), nowForTest()); err != nil {
		t.Fatal(err)
	}
	return gw, db
}

func authedRequest(method, url, token string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, url, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	return r
}

func TestRouter_DashboardForViewer(t *testing.T) {
	fake := &fakeUpstream{
		summary: StationsSummary{
			Items: []StationSummary{{
				StationID: "STA", Paused: false, RunningCount: 1,
				Slots: []SummarySlot{{Kind: "running", TaskID: "t1", RetryIndex: 0, State: "running"}},
			}},
		},
	}
	gw, _ := newTestGateway(t, fake)
	rt := gw.Router(nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("GET", "/api/console/instances/vp1/dashboard", "viewertok", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var view DashboardInstance
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Stations) != 1 || view.Stations[0].Slots[0].Kind != SlotRunning {
		t.Fatalf("unexpected view: %+v", view)
	}
}

func TestRouter_PauseRequiresOperator(t *testing.T) {
	fake := &fakeUpstream{}
	gw, _ := newTestGateway(t, fake)
	rt := gw.Router(nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("POST", "/api/console/instances/vp1/stations/STA/pause", "viewertok", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("viewer pause = %d, want 403", rr.Code)
	}

	rr = httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("POST", "/api/console/instances/vp1/stations/STA/pause", "operatortok", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("operator pause = %d body=%s", rr.Code, rr.Body.String())
	}
	if len(fake.pauseCalls) != 1 || fake.pauseCalls[0] != "STA" {
		t.Errorf("pause not propagated: %+v", fake.pauseCalls)
	}
}

func TestRouter_UnauthenticatedRejected(t *testing.T) {
	gw, _ := newTestGateway(t, &fakeUpstream{})
	rt := gw.Router(nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest("GET", "/api/console/instances", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestRouter_SubmitValidatesTimes(t *testing.T) {
	gw, _ := newTestGateway(t, &fakeUpstream{})
	rt := gw.Router(nil)
	body := strings.NewReader(`{"start":"2026-05-28T10:00:00Z","end":"2026-05-28T09:00:00Z"}`)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("POST", "/api/console/instances/vp1/stations/STA/submissions", "operatortok", body))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (end before start)", rr.Code)
	}
}

func TestRouter_SubmitHappyPath(t *testing.T) {
	fake := &fakeUpstream{}
	gw, _ := newTestGateway(t, fake)
	rt := gw.Router(nil)
	body := strings.NewReader(`{"start":"2026-05-28T08:00:00Z","end":"2026-05-28T10:00:00Z"}`)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("POST", "/api/console/instances/vp1/stations/STA/submissions", "operatortok", body))
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if fake.submitCalls != 1 {
		t.Errorf("submitCalls = %d", fake.submitCalls)
	}
}

func TestRouter_HideRunIdempotentAndAudited(t *testing.T) {
	gw, db := newTestGateway(t, &fakeUpstream{
		tasks: map[string]map[string]any{"TASK-1": {"station_id": "STA"}},
	})
	rt := gw.Router(nil)
	for i := 0; i < 2; i++ {
		rr := httptest.NewRecorder()
		rt.ServeHTTP(rr, authedRequest("POST", "/api/console/instances/vp1/tasks/TASK-1/runs/0/hide?station_id=STA", "operatortok", strings.NewReader("{}")))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	}
	hidden, err := db.HiddenForStation(context.Background(), "vp1", "STA")
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 1 {
		t.Errorf("expected one hidden record after repeat, got %d", len(hidden))
	}
	count, err := db.AuditCount(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Errorf("audit count = %d, want >=2", count)
	}
}

func TestRouter_HideStationFailures(t *testing.T) {
	fake := &fakeUpstream{
		summary: StationsSummary{Items: []StationSummary{{
			StationID: "STA",
			Slots: []SummarySlot{
				{Kind: "failed", TaskID: "TASK-1", RetryIndex: 0, State: "failed"},
				{Kind: "cancelled", TaskID: "TASK-2", RetryIndex: 1, State: "cancelled"},
				{Kind: "running", TaskID: "TASK-3", RetryIndex: 0, State: "running"},
			},
		}}},
	}
	gw, db := newTestGateway(t, fake)
	rt := gw.Router(nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("POST", "/api/console/instances/vp1/stations/STA/hide-failed", "operatortok", strings.NewReader("{}")))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	hidden, err := db.HiddenForStation(context.Background(), "vp1", "STA")
	if err != nil {
		t.Fatal(err)
	}
	if len(hidden) != 2 {
		t.Fatalf("hidden = %d, want 2", len(hidden))
	}
}

func TestRouter_CancelResolvesRunID(t *testing.T) {
	fake := &fakeUpstream{
		taskRuns: map[string][]map[string]any{
			"TASK-1": {
				{"run_id": "RID-0", "retry_index": float64(0)},
				{"run_id": "RID-1", "retry_index": float64(1)},
			},
		},
	}
	gw, _ := newTestGateway(t, fake)
	rt := gw.Router(nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("POST", "/api/console/instances/vp1/tasks/TASK-1/runs/1/cancel", "operatortok", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if fake.cancelCalls != 1 {
		t.Errorf("cancelCalls = %d", fake.cancelCalls)
	}
}

func TestRouter_DegradedInstance(t *testing.T) {
	fake := &fakeFailingUpstream{err: errors.New("upstream down")}
	gw, _ := newTestGateway(t, &fakeUpstream{})
	// Replace the upstream client with one that fails.
	gw.instances["vp1"].client = fake
	rt := gw.Router(nil)
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("GET", "/api/console/instances/vp1/dashboard", "viewertok", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard should always return 200 (degraded); got %d", rr.Code)
	}
	var view DashboardInstance
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Status != "degraded" || view.Error == "" {
		t.Errorf("expected degraded view, got %+v", view)
	}
}

func TestRouter_TreeAndPreview(t *testing.T) {
	// Set up a working root on disk.
	gw, _ := newTestGateway(t, &fakeUpstream{})
	wr := gw.cfg.Instances[0].AllowedRoots[0]
	runDir := filepath.Join(wr, "run0")
	if err := mkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(runDir, "out.log"), []byte("log content\n")); err != nil {
		t.Fatal(err)
	}
	fake := gw.instances["vp1"].client.(*fakeUpstream)
	fake.taskRuns = map[string][]map[string]any{
		"TASK-1": {{"run_id": "RID-0", "retry_index": float64(0), "working_root": runDir}},
	}
	rt := gw.Router(nil)
	// Tree
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("GET", "/api/console/instances/vp1/tasks/TASK-1/runs/0/tree", "viewertok", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("tree = %d body=%s", rr.Code, rr.Body.String())
	}
	var tree TreeResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != 1 || tree.Entries[0].Name != "out.log" {
		t.Fatalf("unexpected tree: %+v", tree)
	}
	// Preview
	rr = httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest("GET", "/api/console/instances/vp1/tasks/TASK-1/runs/0/file?path=out.log", "viewertok", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview = %d body=%s", rr.Code, rr.Body.String())
	}
	var prev PreviewResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &prev); err != nil {
		t.Fatal(err)
	}
	if prev.Kind != KindLog || !strings.HasPrefix(prev.Content, "log content") {
		t.Errorf("unexpected preview: %+v", prev)
	}
}

type fakeFailingUpstream struct{ err error }

func (f *fakeFailingUpstream) Health(_ context.Context) error { return f.err }
func (f *fakeFailingUpstream) GetHealth(_ context.Context) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) Stations(_ context.Context) ([]StationListItem, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) StationsSummary(_ context.Context, _ time.Time) (StationsSummary, error) {
	return StationsSummary{}, f.err
}
func (f *fakeFailingUpstream) StationSummary(_ context.Context, _ string, _ time.Time) (StationSummary, error) {
	return StationSummary{}, f.err
}
func (f *fakeFailingUpstream) PauseStation(_ context.Context, _ string) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) UnpauseStation(_ context.Context, _ string) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) SubmitTask(_ context.Context, _ map[string]any) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) GetTask(_ context.Context, _ string) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) RetryTask(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) ListTasks(_ context.Context, _ url.Values) ([]map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) ListTaskRuns(_ context.Context, _ string) ([]map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) GetRun(_ context.Context, _ string) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) ListRunJobs(_ context.Context, _ string) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) ListRunArtifacts(_ context.Context, _ string) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) ListRunLogs(_ context.Context, _ string) (map[string]any, error) {
	return nil, f.err
}
func (f *fakeFailingUpstream) CancelRun(_ context.Context, _ string, _ map[string]any) (map[string]any, error) {
	return nil, f.err
}
