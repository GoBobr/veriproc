package console

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newRecordingRelayGateway builds a gateway backed by a real HTTP upstream
// that records the Authorization header of every request, so tests can assert
// which credential the gateway presents on the upstream hop.
func newRecordingRelayGateway(t *testing.T, serviceToken string) (*Gateway, *recordingUpstream) {
	t.Helper()
	rec := &recordingUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r.Header)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/stations/summary":
			_, _ = io.WriteString(w, `{"since":"2026-05-28T12:00:00Z","items":[]}`)
		default:
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)

	profilePath := writeProfile(t, testProfileText)
	gw := newRelayGateway(t, profilePath, true, serviceToken, srv.URL)
	return gw, rec
}

// Scenario: Authenticated Caller Credential Relayed (console spec): a signed-in
// console session's own credential travels upstream, so the daemon sees the
// logged-in user's rights rather than only the gateway's service credential.
func TestGateway_AuthenticatedCallerCredentialRelayed(t *testing.T) {
	gw, rec := newRecordingRelayGateway(t, "svc-service-token")
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest(http.MethodGet,
		"/api/console/instances/vp1/dashboard", "alice-operator-token", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if got := rec.lastAuth(); got != "Bearer alice-operator-token" {
		t.Errorf("upstream auth = %q, want the caller's credential relayed", got)
	}
}

// Scenario: Anonymous Caller Uses Service Credential (console spec): an
// anonymous console session falls back to the configured per-instance service
// credential on the upstream hop.
func TestGateway_AnonymousCallerUsesServiceCredential(t *testing.T) {
	gw, rec := newRecordingRelayGateway(t, "svc-service-token")
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
		"/api/console/instances/vp1/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("anonymous dashboard: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if got := rec.lastAuth(); got != "Bearer svc-service-token" {
		t.Errorf("upstream auth = %q, want the service credential", got)
	}
}

// Scenario: Anonymous Caller Without Service Credential Sends None (console
// spec): with no service credential configured the upstream request carries no
// Authorization header at all.
func TestGateway_AnonymousCallerWithoutServiceCredentialSendsNone(t *testing.T) {
	gw, rec := newRecordingRelayGateway(t, "")
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
		"/api/console/instances/vp1/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("anonymous dashboard: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if got := rec.lastAuth(); got != "" {
		t.Errorf("upstream auth = %q, want no Authorization header", got)
	}
}

// A rejected credential downgraded to guest must NOT leak the rejected token
// upstream: the relay only carries credentials the gateway accepted.
func TestGateway_RejectedCredentialDoesNotRelayUpstream(t *testing.T) {
	gw, rec := newRecordingRelayGateway(t, "svc-service-token")
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest(http.MethodGet,
		"/api/console/instances/vp1/dashboard", "wrong-token", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard with rejected credential: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if got := rec.lastAuth(); got != "Bearer svc-service-token" {
		t.Errorf("upstream auth = %q, want the service credential (rejected token must not be relayed)", got)
	}
}

// ── 10.3 Access denial reported structurally ─────────────────────────────

// newDeniedContentGateway builds a gateway whose upstream health probe
// succeeds but whose dashboard summary rejects every credential with 401, so
// the instance stays listed while its content is refused.
func newDeniedContentGateway(t *testing.T) *Gateway {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasPrefix(r.URL.Path, "/api/v1/stations/summary") {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"code":"unauthenticated","message":"missing bearer token"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(srv.Close)

	profilePath := writeProfile(t, testProfileText)
	return newRelayGateway(t, profilePath, true, "", srv.URL)
}

// Scenario: Content Rejection Does Not Hide Instance (console spec): an
// instance whose health probe succeeds but whose content rejects the caller
// stays in the instance list — the pane explains itself instead of vanishing.
func TestGateway_ContentRejectionDoesNotHideInstance(t *testing.T) {
	gw := newDeniedContentGateway(t)
	rt := gw.Router(nil)

	ids := visibleIDs(t, rt, httptest.NewRequest(http.MethodGet, "/api/console/instances", nil))
	if !contains(ids, "vp1") {
		t.Errorf("instance with content rejection hidden from list: %v", ids)
	}
}

// Scenario: Access Denial Reported Structurally (console spec): the dashboard
// reports a structured access-denied state with an empty (non-null) station
// array instead of a raw upstream error blob.
func TestGateway_AccessDenialReportedStructurally(t *testing.T) {
	gw := newDeniedContentGateway(t)
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet,
		"/api/console/instances/vp1/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("dashboard: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"status":"access_denied"`) {
		t.Errorf("dashboard should report access_denied: %s", body)
	}
	if strings.Contains(body, `"stations":null`) {
		t.Errorf("stations must never be null: %s", body)
	}
	if strings.Contains(body, "missing bearer token") {
		t.Errorf("raw upstream error blob leaked into the response: %s", body)
	}
}