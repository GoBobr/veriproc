package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gobobr/veriproc/internal/cleaner"
	"github.com/gobobr/veriproc/internal/config"
	"github.com/gobobr/veriproc/internal/health"
	"github.com/gobobr/veriproc/internal/httpapi"
	"github.com/gobobr/veriproc/internal/store"
	"github.com/rs/zerolog"
)

type cleanAPI struct {
	base string
	st   *store.Store
}

func newCleanAPI(t *testing.T) *cleanAPI {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "clean-api.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	router := httpapi.NewRouter(httpapi.Deps{
		Config:  &config.Config{InstanceID: "test"},
		Health:  health.NewAggregator(time.Second),
		Logger:  zerolog.Nop(),
		Cleaner: cleaner.New(st, zerolog.Nop()),
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return &cleanAPI{base: srv.URL, st: st}
}

func (a *cleanAPI) seedTaskRun(t *testing.T, taskID, runID string, start, end time.Time) string {
	t.Helper()
	ctx := context.Background()
	if err := a.st.Stations().Insert(ctx, &store.StationRevisionRecord{
		RevisionID: "rev-SCENE-L2", StationID: "SCENE-L2", StationName: "SCE_2",
		ContentHash: "sha256:scene", SchemaVersion: "veriproc.station/v1",
	}); err != nil && !errors.Is(err, store.ErrConflict) {
		t.Fatalf("station: %v", err)
	}
	if err := a.st.Tasks().Insert(ctx, &store.TaskRecord{
		TaskID: taskID, SchemaVersion: "veriproc.task-submission/v1",
		DestinationStationID: "SCENE-L2", WindowStart: start, WindowEnd: end,
		ClientMetadata:     json.RawMessage(`{}`),
		RoutingContent:     json.RawMessage(`{"destination":{"station_id":"SCENE-L2"}}`),
		RoutingContentHash: "sha256:routing-" + taskID, SubmissionOrigin: "client", State: "accepted",
	}); err != nil {
		t.Fatalf("task insert: %v", err)
	}
	dir := filepath.Join(t.TempDir(), runID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := a.st.Runs().Insert(ctx, &store.RunRecord{
		RunID: runID, TaskID: taskID, StationRevisionID: "rev-SCENE-L2",
		RetryIndex: 0, WorkingRoot: dir, State: "succeeded", Canonicality: "canonical",
	}); err != nil {
		t.Fatalf("run insert: %v", err)
	}
	return dir
}

func decodeReport(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

// TestCleanerAPI_DeleteTaskDryRun previews without deleting.
func TestCleanerAPI_DeleteTaskDryRun(t *testing.T) {
	a := newCleanAPI(t)
	dir := a.seedTaskRun(t, "t1", "run-1", ts(7, 3), ts(7, 4))

	resp, err := http.NewRequest(http.MethodDelete, a.base+"/api/v1/tasks/t1?dry_run=true", nil)
	if err != nil {
		t.Fatal(err)
	}
	r, err := http.DefaultClient.Do(resp)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	m := decodeReport(t, r.Body)
	if m["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", m["dry_run"])
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("dry-run removed working root: %v", err)
	}
	if _, err := a.st.Tasks().Get(context.Background(), "t1"); err != nil {
		t.Errorf("dry-run deleted task: %v", err)
	}
}

// TestCleanerAPI_DeleteTask deletes for real (cascade=true removes working roots).
func TestCleanerAPI_DeleteTask(t *testing.T) {
	a := newCleanAPI(t)
	dir := a.seedTaskRun(t, "t1", "run-1", ts(7, 3), ts(7, 4))

	req, _ := http.NewRequest(http.MethodDelete, a.base+"/api/v1/tasks/t1?cascade=true", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("working root still present: %v", err)
	}
	if _, err := a.st.Tasks().Get(context.Background(), "t1"); !isNotFound(err) {
		t.Errorf("task not deleted: %v", err)
	}
}

// TestCleanerAPI_DeleteTaskNoCascade deletes task+runs (non-cascade preserves
// descendants, working roots are always removed from disk).
func TestCleanerAPI_DeleteTaskNoCascade(t *testing.T) {
	a := newCleanAPI(t)
	dir := a.seedTaskRun(t, "t1", "run-1", ts(7, 3), ts(7, 4))

	req, _ := http.NewRequest(http.MethodDelete, a.base+"/api/v1/tasks/t1", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	// Working root should still be removed (deleted tasks' working roots are always cleaned).
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("working root should be removed: %v", err)
	}
	if _, err := a.st.Tasks().Get(context.Background(), "t1"); !isNotFound(err) {
		t.Errorf("task should be deleted: %v", err)
	}
}

// TestCleanerAPI_DeleteTaskNotFound returns 404.
func TestCleanerAPI_DeleteTaskNotFound(t *testing.T) {
	a := newCleanAPI(t)
	req, _ := http.NewRequest(http.MethodDelete, a.base+"/api/v1/tasks/ghost", nil)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", r.StatusCode)
	}
}

// TestCleanerAPI_CleanNoCutoff returns 400.
func TestCleanerAPI_CleanNoCutoff(t *testing.T) {
	a := newCleanAPI(t)
	r, err := http.Post(a.base+"/api/v1/maintenance/clean", "application/json", strings.NewReader(`{"dry_run":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", r.StatusCode)
	}
}

// TestCleanerAPI_Clean deletes tasks within the window.
func TestCleanerAPI_Clean(t *testing.T) {
	a := newCleanAPI(t)
	a.seedTaskRun(t, "early", "run-early", ts(5, 1), ts(5, 2))
	a.seedTaskRun(t, "late", "run-late", ts(7, 2), ts(7, 3))

	r, err := http.Post(a.base+"/api/v1/maintenance/clean", "application/json",
		strings.NewReader(`{"before":"2025-06-01T00:00:00Z","basis":"processing-window","dry_run":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	m := decodeReport(t, r.Body)
	ids, _ := m["task_ids"].([]any)
	if len(ids) != 1 || ids[0] != "early" {
		t.Errorf("task_ids = %v, want [early]", ids)
	}
	if _, err := a.st.Tasks().Get(context.Background(), "late"); err != nil {
		t.Errorf("late task should survive: %v", err)
	}
}

// TestCleanerAPI_CleanStationFilter deletes only tasks matching station_id.
func TestCleanerAPI_CleanStationFilter(t *testing.T) {
	a := newCleanAPI(t)
	// Both tasks are in the same time window; only the station differs.
	a.seedTaskRun(t, "scene-early", "run-scene", ts(5, 1), ts(5, 2))
	// seedTaskRun always uses SCENE-L2, so insert a MAP-L1C task manually.
	ctx := context.Background()
	if err := a.st.Stations().Insert(ctx, &store.StationRevisionRecord{
		RevisionID: "rev-MAP-L1C", StationID: "MAP-L1C", StationName: "MAP_1C",
		ContentHash: "sha256:map", SchemaVersion: "veriproc.station/v1",
	}); err != nil && !errors.Is(err, store.ErrConflict) {
		t.Fatalf("station: %v", err)
	}
	if err := a.st.Tasks().Insert(ctx, &store.TaskRecord{
		TaskID: "map-early", SchemaVersion: "veriproc.task-submission/v1",
		DestinationStationID: "MAP-L1C", WindowStart: ts(5, 1), WindowEnd: ts(5, 2),
		ClientMetadata:     json.RawMessage(`{}`),
		RoutingContent:     json.RawMessage(`{"destination":{"station_id":"MAP-L1C"}}`),
		RoutingContentHash: "sha256:routing-map-early", SubmissionOrigin: "client", State: "accepted",
	}); err != nil {
		t.Fatalf("task insert: %v", err)
	}

	r, err := http.Post(a.base+"/api/v1/maintenance/clean", "application/json",
		strings.NewReader(`{"before":"2025-06-01T00:00:00Z","basis":"processing-window","station_id":"SCENE-L2","dry_run":false}`))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", r.StatusCode)
	}
	m := decodeReport(t, r.Body)
	ids, _ := m["task_ids"].([]any)
	if len(ids) != 1 || ids[0] != "scene-early" {
		t.Errorf("task_ids = %v, want [scene-early]", ids)
	}
	if _, err := a.st.Tasks().Get(ctx, "map-early"); err != nil {
		t.Errorf("map-early task should survive: %v", err)
	}
}

func isNotFound(err error) bool {
	return errors.Is(err, store.ErrNotFound)
}

func ts(month, day int) time.Time {
	return time.Date(2025, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}
