package console

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// jsonStringArray extracts []string values for key under an array field of a
// JSON object, e.g. items[].id.
func jsonStringArray(t *testing.T, body []byte, field, sub string) []string {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(obj[field], &arr); err != nil {
		t.Fatalf("decode %s: %v", field, err)
	}
	out := make([]string, 0, len(arr))
	for _, item := range arr {
		if v, ok := item[sub].(string); ok {
			out = append(out, v)
		}
	}
	return out
}

// newVisibilityGateway builds a gateway with two instances backed by real
// HTTP upstreams: vp-ok always answers 200, vp-denied answers whatever
// deniedCode says (401/403) until flipped.
func newVisibilityGateway(t *testing.T, anonymousBrowsing bool, deniedStatus *atomic.Int32) *Gateway {
	t.Helper()
	tmp := t.TempDir()

	okSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(okSrv.Close)

	deniedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := deniedStatus.Load(); code != http.StatusOK {
			w.WriteHeader(int(code))
			_, _ = io.WriteString(w, `{"error":{"code":"unauthenticated","message":"missing credential"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok"}`)
	}))
	t.Cleanup(deniedSrv.Close)

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
			StationRunsPageSize:    50,
		},
		Instances: []InstanceConfig{
			{
				ID: "vp-ok", Title: "OK", BaseURL: okSrv.URL,
				Token: "svc-token", WorkingRootBase: tmp, AllowedRoots: []string{tmp},
				UpstreamTimeoutMS: 2000,
			},
			{
				ID: "vp-denied", Title: "Denied", BaseURL: deniedSrv.URL,
				Token: "svc-token", WorkingRootBase: tmp, AllowedRoots: []string{tmp},
				UpstreamTimeoutMS: 2000,
			},
		},
	}
	cfg.Auth.ProfilePath = writeProfile(t, testProfileText)
	cfg.Auth.AllowAnonymousBrowsing = anonymousBrowsing
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	gw, err := NewGateway(Options{
		Config: cfg,
		DB:     newTestDB(t),
		Logger: zerolog.New(io.Discard),
		Now:    nowForTest,
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	return gw
}

// visibleIDs returns the instance ids present in the list response.
func visibleIDs(t *testing.T, rt http.Handler, req *http.Request) []string {
	t.Helper()
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("instance list: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	return jsonStringArray(t, rr.Body.Bytes(), "items", "id")
}

// TestGateway_InaccessibleInstanceHidden covers "Inaccessible Instance
// Hidden": an instance whose upstream rejects the gateway's credential is
// omitted from the list.
func TestGateway_InaccessibleInstanceHidden(t *testing.T) {
	denied := &atomic.Int32{}
	denied.Store(http.StatusUnauthorized)
	gw := newVisibilityGateway(t, true, denied)
	rt := gw.Router(nil)

	ids := visibleIDs(t, rt, httptest.NewRequest(http.MethodGet, "/api/console/instances", nil))
	if contains(ids, "vp-denied") {
		t.Errorf("instance with 401 upstream present in list: %v", ids)
	}
	if !contains(ids, "vp-ok") {
		t.Errorf("reachable instance missing from list: %v", ids)
	}
}

// TestGateway_InstanceAppearsAfterLogin covers "Instance Appears After
// Login": an instance hidden while its upstream rejects the gateway becomes
// visible once the upstream accepts it and the identity endpoint re-probes.
func TestGateway_InstanceAppearsAfterLogin(t *testing.T) {
	denied := &atomic.Int32{}
	denied.Store(http.StatusUnauthorized)
	gw := newVisibilityGateway(t, true, denied)
	rt := gw.Router(nil)

	// Hidden while the upstream rejects the gateway credential.
	ids := visibleIDs(t, rt, httptest.NewRequest(http.MethodGet, "/api/console/instances", nil))
	if contains(ids, "vp-denied") {
		t.Fatalf("vp-denied visible before login: %v", ids)
	}

	// The upstream starts accepting the gateway credential (e.g. the
	// instance was redeployed with the right service token).
	denied.Store(int32(http.StatusOK))

	// A successful identity resolution re-probes the upstreams.
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest(http.MethodGet, "/api/console/identity", "alice-operator-token", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("identity: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}

	// The instance is now present.
	ids = visibleIDs(t, rt, authedRequest(http.MethodGet, "/api/console/instances", "alice-operator-token", nil))
	if !contains(ids, "vp-denied") {
		t.Errorf("vp-denied still hidden after login: %v", ids)
	}
}

// contains reports whether ss contains s.
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
