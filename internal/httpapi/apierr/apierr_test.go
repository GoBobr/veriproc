package apierr_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gobobr/veriproc/internal/httpapi/apierr"
	"github.com/gobobr/veriproc/internal/logging"
)

// TestAPIErr_Envelope_AllCodes_5_7 — every advertised code maps to a
// well-formed envelope with a sensible default status.
func TestAPIErr_Envelope_AllCodes_5_7(t *testing.T) {
	cases := []struct {
		code   apierr.Code
		status int
	}{
		{apierr.CodeInvalidRequest, http.StatusBadRequest},
		{apierr.CodeUnknownStation, http.StatusBadRequest},
		{apierr.CodeNotFound, http.StatusNotFound},
		{apierr.CodeMethodNotAllowed, http.StatusMethodNotAllowed},
		{apierr.CodeIdempotencyConflict, http.StatusConflict},
		{apierr.CodeInvalidStateTransition, http.StatusConflict},
		{apierr.CodeCancellationUnsupported, http.StatusConflict},
		{apierr.CodeReconciliationInProgress, http.StatusConflict},
		{apierr.CodeDependencyUnavailable, http.StatusServiceUnavailable},
		{apierr.CodeUnauthenticated, http.StatusUnauthorized},
		{apierr.CodeUnauthorized, http.StatusForbidden},
		{apierr.CodeQuotaExceeded, http.StatusTooManyRequests},
		{apierr.CodeRateLimited, http.StatusTooManyRequests},
		{apierr.CodeInternal, http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := apierr.HTTPStatusFor(tc.code); got != tc.status {
			t.Errorf("HTTPStatusFor(%s) = %d, want %d", tc.code, got, tc.status)
		}
	}
}

// TestAPIErr_DetailFields_5_7 — Retryable/Dependency/ConflictingResourceID
// are emitted only when populated; CorrelationID is auto-injected.
func TestAPIErr_DetailFields_5_7(t *testing.T) {
	rec := httptest.NewRecorder()
	ctx := logging.WithCorrelationID(context.Background(), "corr-xyz")
	r := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	apierr.WriteWithDetail(rec, r, http.StatusServiceUnavailable, apierr.Detail{
		Code:       apierr.CodeDependencyUnavailable,
		Message:    "registry offline",
		Retryable:  apierr.BoolPtr(true),
		Dependency: "station-registry",
	})
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", rec.Code)
	}
	var body apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != apierr.CodeDependencyUnavailable {
		t.Errorf("code = %v", body.Error.Code)
	}
	if body.Error.Retryable == nil || *body.Error.Retryable != true {
		t.Errorf("retryable = %v", body.Error.Retryable)
	}
	if body.Error.Dependency != "station-registry" {
		t.Errorf("dependency = %v", body.Error.Dependency)
	}
	if body.Error.CorrelationID != "corr-xyz" {
		t.Errorf("correlation_id = %v", body.Error.CorrelationID)
	}
	if body.Error.ConflictingResourceID != "" {
		t.Errorf("unexpected conflicting_resource_id: %v", body.Error.ConflictingResourceID)
	}
}
