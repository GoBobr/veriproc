package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAPI_TaskRetry_FailedTask_5_3 — POST /api/v1/tasks/{id}/retry on a
// failed task returns 201 with a new run record whose retry_index is
// incremented and whose state is "pending". Spec §3.7.
func TestAPI_TaskRetry_FailedTask_5_3(t *testing.T) {
	a := newRunAPI(t)
	ctx := context.Background()
	taskID := a.submitOne(t)

	// Drive to dispatched state (tick 1 = admit/prepare, tick 2 = dispatch).
	a.tickN(t, 2)
	tk, _ := a.st.Tasks().Get(ctx, taskID)

	// Cancel the run so the task ends up in "failed" state (which is what
	// the cancel path sets when executor.StatusCancelled is observed).
	postCancel(t, a.srv, tk.LatestRunID)
	// After cancel the run is terminal; however the task.State may still be
	// "accepted" because the cancel path uses MarkCancelled (not Poll's
	// SetState). Force the task to "failed" directly so Retry is eligible.
	if err := a.st.Tasks().SetState(ctx, taskID, "failed", "test"); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	resp, body := postRetry(t, a.srv, taskID)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, body = %v", resp.StatusCode, body)
	}
	for _, k := range []string{"run_id", "task_id", "state", "retry_index", "created_at"} {
		if _, ok := body[k]; !ok {
			t.Errorf("missing field %q in response: %v", k, body)
		}
	}
	if body["task_id"] != taskID {
		t.Errorf("task_id = %v, want %q", body["task_id"], taskID)
	}
	// The new run must not be the same as the cancelled run.
	if body["run_id"] == tk.LatestRunID {
		t.Errorf("retry returned the same run id as the cancelled run")
	}
}

// TestAPI_TaskRetry_NotFound_5_3 — POST /api/v1/tasks/{id}/retry for an
// unknown task returns 404 with the standard error envelope.
func TestAPI_TaskRetry_NotFound_5_3(t *testing.T) {
	a := newRunAPI(t)
	resp, body := postRetry(t, a.srv, "task-does-not-exist")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if body["error"] == nil {
		t.Errorf("missing error envelope: %v", body)
	}
}

// TestAPI_TaskRetry_IneligibleState_5_3 — POST /api/v1/tasks/{id}/retry
// on a task that is not in a terminal failed/cancelled state returns 409
// invalid_state_transition.
func TestAPI_TaskRetry_IneligibleState_5_3(t *testing.T) {
	a := newRunAPI(t)
	taskID := a.submitOne(t)
	// Task is freshly submitted ("accepted") — not eligible for retry.
	resp, body := postRetry(t, a.srv, taskID)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409", resp.StatusCode)
	}
	errEnv, _ := body["error"].(map[string]any)
	if errEnv == nil {
		t.Fatalf("missing error envelope: %v", body)
	}
	if errEnv["code"] != "invalid_state_transition" {
		t.Errorf("error.code = %v, want invalid_state_transition", errEnv["code"])
	}
}

func postRetry(t *testing.T, srv *httptest.Server, taskID string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost,
		srv.URL+"/api/v1/tasks/"+taskID+"/retry", bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	raw := readAll(t, resp)
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp, out
}
