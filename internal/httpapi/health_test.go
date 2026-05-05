package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/logging"
	"github.com/rs/zerolog"
)

func newTestRouter(t *testing.T, agg *health.Aggregator) http.Handler {
	t.Helper()
	cfg, err := config.Load([]string{"--instance-id", "test-instance"}, nil)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	logger := zerolog.New(io.Discard)
	return NewRouter(Deps{Config: cfg, Health: agg, Logger: logger})
}

// TestAPI_Health_5_8_M0 — GET /health returns 200 + payload shape (Spec 5.7).
func TestAPI_Health_5_8_M0(t *testing.T) {
	r := newTestRouter(t, health.NewAggregator(0))

	for _, path := range []string{"/health", "/api/v1/health"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, rr.Code)
		}
		if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s content-type = %q", path, ct)
		}
		var body health.LivenessReport
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v body=%s", path, err, rr.Body.String())
		}
		if body.Status != health.StateOK {
			t.Errorf("%s status field = %q", path, body.Status)
		}
		if body.InstanceID != "test-instance" {
			t.Errorf("%s instance_id = %q", path, body.InstanceID)
		}
		if body.APIVersion == "" {
			t.Errorf("%s api_version empty", path)
		}
	}
}

// TestAPI_Health_Shutdown_M0 — once shutdown is initiated /health returns 503.
func TestAPI_Health_Shutdown_M0(t *testing.T) {
	agg := health.NewAggregator(0)
	r := newTestRouter(t, agg)
	agg.BeginShutdown()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// TestAPI_Readiness_5_8_M0 — GET /readiness returns 200 with no deps configured.
func TestAPI_Readiness_5_8_M0(t *testing.T) {
	r := newTestRouter(t, health.NewAggregator(0))
	req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var body health.ReadinessReport
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.ReadinessState != health.StateReady {
		t.Errorf("readiness_state = %q, want ready", body.ReadinessState)
	}
	if body.Dependencies == nil {
		t.Errorf("dependencies field must be present (got nil)")
	}
}

type downChecker struct{}

func (downChecker) Name() string { return "fake-dep" }
func (downChecker) Check(_ context.Context) health.CheckResult {
	return health.CheckResult{Name: "fake-dep", State: health.DepDown, Message: "boom"}
}

// TestAPI_Readiness_Unready_M0 — failing dependency surfaces 503 + message.
func TestAPI_Readiness_Unready_M0(t *testing.T) {
	agg := health.NewAggregator(0)
	agg.Register(downChecker{})
	r := newTestRouter(t, agg)

	req := httptest.NewRequest(http.MethodGet, "/readiness", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
	var body health.ReadinessReport
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	dep, ok := body.Dependencies["fake-dep"]
	if !ok || dep.State != health.DepDown || dep.Message == "" {
		t.Errorf("expected fake-dep down with message, got %+v", body.Dependencies)
	}
}

// TestAPI_CorrelationID_Echo_M0 — supplied X-Correlation-ID is echoed.
func TestAPI_CorrelationID_Echo_M0(t *testing.T) {
	r := newTestRouter(t, health.NewAggregator(0))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(logging.CorrelationHeader, "abc-123")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if got := rr.Header().Get(logging.CorrelationHeader); got != "abc-123" {
		t.Errorf("correlation echo = %q, want abc-123", got)
	}
}

// TestAPI_CorrelationID_Generated_M0 — missing correlation header is generated.
func TestAPI_CorrelationID_Generated_M0(t *testing.T) {
	r := newTestRouter(t, health.NewAggregator(0))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if got := rr.Header().Get(logging.CorrelationHeader); got == "" {
		t.Errorf("correlation id should be generated when absent")
	}
}

// TestAPI_MethodNotAllowed_M0 — wrong method on /health returns 405.
func TestAPI_MethodNotAllowed_M0(t *testing.T) {
	r := newTestRouter(t, health.NewAggregator(0))
	req := httptest.NewRequest(http.MethodPost, "/health", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

// TestAPI_NotFound_M0 — unknown route returns 404.
func TestAPI_NotFound_M0(t *testing.T) {
	r := newTestRouter(t, health.NewAggregator(0))
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}
