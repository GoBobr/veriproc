package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAPI_RunCancel_Dispatched_5_4_10 — POST /runs/{id}/cancel on a
// dispatched run returns 200 with cancellation_complete=true (stub executor
// cancels synchronously). Spec §5.4.10 + §5.8.
func TestAPI_RunCancel_Dispatched_5_4_10(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 2) // prepare + dispatch
	tk, _ := a.st.Tasks().Get(context.Background(), taskID)

	resp, body := postCancel(t, a.srv, tk.LatestRunID)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d body=%v", resp.StatusCode, body)
	}
	if body["state"] != "cancelled" {
		t.Errorf("state = %v, want cancelled", body["state"])
	}
	if body["cancellation_complete"] != true {
		t.Errorf("cancellation_complete = %v, want true", body["cancellation_complete"])
	}
	if _, ok := body["cancellation_requested_at"]; !ok {
		t.Errorf("missing cancellation_requested_at: %v", body)
	}
}

// TestAPI_RunCancel_Idempotent_5_4_10 — re-cancelling a terminal run
// returns 200 with already_terminal=true.
func TestAPI_RunCancel_Idempotent_5_4_10(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	a.tickN(t, 2)
	tk, _ := a.st.Tasks().Get(context.Background(), taskID)
	postCancel(t, a.srv, tk.LatestRunID)
	resp, body := postCancel(t, a.srv, tk.LatestRunID)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d body=%v", resp.StatusCode, body)
	}
	if body["already_terminal"] != true {
		t.Errorf("already_terminal = %v, want true", body["already_terminal"])
	}
}

// TestAPI_RunCancel_NotFound_5_4_10 — unknown run id → 404 envelope.
func TestAPI_RunCancel_NotFound_5_4_10(t *testing.T) {
	a := newRunAPI(t)
	resp, body := postCancel(t, a.srv, "no-such")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Errorf("no error envelope: %v", body)
	}
}

func postCancel(t *testing.T, srv *httptest.Server, runID string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/runs/"+runID+"/cancel", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body := readAll(t, resp)
	var out map[string]any
	if len(body) > 0 {
		_ = json.Unmarshal(body, &out)
	}
	return resp, out
}
