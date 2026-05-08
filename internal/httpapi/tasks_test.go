package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/httpapi"
	"github.com/eum/veriproc/internal/stations"
	"github.com/eum/veriproc/internal/store"
	"github.com/eum/veriproc/internal/tasks"
	"github.com/rs/zerolog"
)

// newAPI sets up an httptest server with the M2 router wired against an
// isolated in-memory state-store and a registry seeded with one station.
func newAPI(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "api.db")
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
	svc := tasks.NewService(st, reg, nil, nil)

	router := httpapi.NewRouter(httpapi.Deps{
		Config: &config.Config{InstanceID: "test"},
		Health: health.NewAggregator(time.Second),
		Logger: zerolog.Nop(),
		Tasks:  svc,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(func() { srv.Close(); _ = st.Close() })
	return srv, st
}

func validBody() map[string]any {
	return map[string]any{
		"destination": map[string]any{"station_id": "SCENE-L2"},
		"window": map[string]any{
			"start": "2025-07-03T11:15:39Z",
			"end":   "2025-07-03T11:30:00Z",
		},
	}
}

func postJSON(t *testing.T, srv *httptest.Server, path string, body any, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	out := readAll(t, resp)
	return resp, out
}

func readAll(t *testing.T, resp *http.Response) []byte {
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// TestAPI_TaskSubmit_Created_5_3_1_M2 — POST /tasks returns 201 with the
// canonical envelope and links.
func TestAPI_TaskSubmit_Created_5_3_1_M2(t *testing.T) {
	srv, _ := newAPI(t)
	resp, body := postJSON(t, srv, "/api/v1/tasks", validBody(), nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", resp.StatusCode, body)
	}
	var out struct {
		Task  map[string]any    `json:"task"`
		Links map[string]string `json:"links"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Task["state"] != "accepted" {
		t.Errorf("state = %v", out.Task["state"])
	}
	if !strings.HasPrefix(out.Links["self"], "/api/v1/tasks/") {
		t.Errorf("self link = %q", out.Links["self"])
	}
}

// TestAPI_TaskSubmit_IdempotencyHeaderReplay_5_3_5_M2 — header-driven
// idempotency replay returns 200 with the same task_id.
func TestAPI_TaskSubmit_IdempotencyHeaderReplay_5_3_5_M2(t *testing.T) {
	srv, _ := newAPI(t)
	hdr := map[string]string{"Idempotency-Key": "client-abc"}
	r1, b1 := postJSON(t, srv, "/api/v1/tasks", validBody(), hdr)
	r2, b2 := postJSON(t, srv, "/api/v1/tasks", validBody(), hdr)
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("first status = %d", r1.StatusCode)
	}
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", r2.StatusCode)
	}
	id1 := taskIDFrom(t, b1)
	id2 := taskIDFrom(t, b2)
	if id1 != id2 {
		t.Errorf("task_id mismatch: %s vs %s", id1, id2)
	}
}

// TestAPI_TaskSubmit_IdempotencyConflict_5_3_5_M2 — same key, different body
// returns 409 with idempotency_conflict envelope code.
func TestAPI_TaskSubmit_IdempotencyConflict_5_3_5_M2(t *testing.T) {
	srv, _ := newAPI(t)
	hdr := map[string]string{"Idempotency-Key": "client-xyz"}
	postJSON(t, srv, "/api/v1/tasks", validBody(), hdr)
	body := validBody()
	body["force"] = true
	resp, raw := postJSON(t, srv, "/api/v1/tasks", body, hdr)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if code := errorCode(t, raw); code != "idempotency_conflict" {
		t.Errorf("code = %q, want idempotency_conflict", code)
	}
}

// TestAPI_TaskSubmit_UnknownStation_5_3_2_M2 — unresolvable station → 400 with
// unknown_station envelope code.
func TestAPI_TaskSubmit_UnknownStation_5_3_2_M2(t *testing.T) {
	srv, _ := newAPI(t)
	b := validBody()
	b["destination"] = map[string]any{"station_id": "GHOST"}
	resp, raw := postJSON(t, srv, "/api/v1/tasks", b, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if code := errorCode(t, raw); code != "unknown_station" {
		t.Errorf("code = %q", code)
	}
}

// TestAPI_TaskSubmit_InvalidWindow_5_3_3_M2 — end < start → 400 invalid_request.
func TestAPI_TaskSubmit_InvalidWindow_5_3_3_M2(t *testing.T) {
	srv, _ := newAPI(t)
	b := validBody()
	b["window"] = map[string]any{
		"start": "2025-07-03T12:00:00Z",
		"end":   "2025-07-03T11:00:00Z",
	}
	resp, raw := postJSON(t, srv, "/api/v1/tasks", b, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if code := errorCode(t, raw); code != "invalid_request" {
		t.Errorf("code = %q", code)
	}
}

// TestAPI_TaskSubmit_MalformedJSON_5_5_8_M2 — invalid body returns the
// envelope with a correlation id.
func TestAPI_TaskSubmit_MalformedJSON_5_5_8_M2(t *testing.T) {
	srv, _ := newAPI(t)
	req, _ := http.NewRequest("POST", srv.URL+"/api/v1/tasks", strings.NewReader("{not json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw := readAll(t, resp)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if errorCode(t, raw) != "invalid_request" {
		t.Errorf("code mismatch: %s", raw)
	}
}

// TestAPI_TaskGet_NotFound_5_4_1_M2 — unknown task returns 404 envelope.
func TestAPI_TaskGet_NotFound_5_4_1_M2(t *testing.T) {
	srv, _ := newAPI(t)
	resp, raw := getJSON(t, srv, "/api/v1/tasks/nonexistent")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if errorCode(t, raw) != "not_found" {
		t.Errorf("code = %s", raw)
	}
}

// TestAPI_TaskGetRoundTrip_5_4_1_M2 — POST then GET returns matching record.
func TestAPI_TaskGetRoundTrip_5_4_1_M2(t *testing.T) {
	srv, _ := newAPI(t)
	_, body := postJSON(t, srv, "/api/v1/tasks", validBody(), nil)
	id := taskIDFrom(t, body)

	resp, raw := getJSON(t, srv, "/api/v1/tasks/"+id)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, raw)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out["task_id"] != id {
		t.Errorf("task_id = %v, want %s", out["task_id"], id)
	}
	if out["state"] != "accepted" {
		t.Errorf("state = %v", out["state"])
	}
	for _, key := range []string{"station_id", "start", "end"} {
		if out[key] == nil || out[key] == "" {
			t.Errorf("missing readable context field %s in %#v", key, out)
		}
	}
}

// TestAPI_TaskList_Pagination_5_4_2_M2 — returns next_cursor and is consistent
// across pages.
func TestAPI_TaskList_Pagination_5_4_2_M2(t *testing.T) {
	srv, _ := newAPI(t)
	for i := 0; i < 3; i++ {
		body := validBody()
		body["idempotency_key"] = "k-" + string(rune('a'+i))
		postJSON(t, srv, "/api/v1/tasks", body, nil)
	}
	resp, raw := getJSON(t, srv, "/api/v1/tasks?limit=2")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var page struct {
		Items      []map[string]any `json:"items"`
		NextCursor string           `json:"next_cursor"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("items = %d", len(page.Items))
	}
	if page.NextCursor == "" {
		t.Fatal("expected next_cursor")
	}
	resp2, raw2 := getJSON(t, srv, "/api/v1/tasks?limit=2&cursor="+page.NextCursor)
	if resp2.StatusCode != 200 {
		t.Fatalf("status = %d", resp2.StatusCode)
	}
	var page2 struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw2, &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 {
		t.Fatalf("page2 items = %d", len(page2.Items))
	}
}

func getJSON(t *testing.T, srv *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp, readAll(t, resp)
}

func taskIDFrom(t *testing.T, body []byte) string {
	t.Helper()
	var wrap struct {
		Task struct {
			TaskID string `json:"task_id"`
		} `json:"task"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	return wrap.Task.TaskID
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Code          string `json:"code"`
			CorrelationID string `json:"correlation_id"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode envelope: %v body=%s", err, body)
	}
	return env.Error.Code
}
