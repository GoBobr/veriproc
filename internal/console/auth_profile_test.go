package console

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/accessprofile"
	"github.com/gobobr/veriproc/internal/auth"
)

// writeProfile writes profileText to a temp file and returns its path.
func writeProfile(t *testing.T, profileText string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "access.profile")
	if err := os.WriteFile(path, []byte(profileText), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	return path
}

// newProfileGateway builds a gateway whose credential source is an access
// profile containing alice (operator) and bob (reader). legacyTokens, when
// non-empty, are seeded into the config's inline token list to prove the
// profile supersedes it.
func newProfileGateway(t *testing.T, profileText string, anonymousBrowsing bool, legacyTokens []TokenEntry) (*Gateway, string) {
	t.Helper()
	path := writeProfile(t, profileText)
	tmp := t.TempDir()
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
		Instances: []InstanceConfig{{
			ID: "vp1", Title: "VP1", BaseURL: "http://upstream",
			WorkingRootBase:   tmp,
			AllowedRoots:      []string{tmp},
			UpstreamTimeoutMS: 1000,
		}},
	}
	cfg.Auth.ProfilePath = path
	cfg.Auth.AllowAnonymousBrowsing = anonymousBrowsing
	cfg.Auth.Tokens = legacyTokens
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config: %v", err)
	}
	gw, err := NewGateway(Options{
		Config: cfg,
		DB:     newTestDB(t),
		Logger: zerolog.New(io.Discard),
		NewClient: func(_ InstanceConfig) (UpstreamClient, error) {
			return &fakeUpstream{}, nil
		},
		Now: nowForTest,
	})
	if err != nil {
		t.Fatalf("gateway: %v", err)
	}
	return gw, path
}

const testProfileText = `# test access profile
alice:operator:alice-operator-token  # alice ops
bob:reader:bob-reader-token
`

// recordingUpstream is an httptest-backed upstream that records the
// Authorization header of every request it receives.
type recordingUpstream struct {
	mu      sync.Mutex
	headers []string
}

func (r *recordingUpstream) record(h http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers = append(r.headers, h.Get("Authorization"))
}

func (r *recordingUpstream) lastAuth() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.headers) == 0 {
		return ""
	}
	return r.headers[len(r.headers)-1]
}

// newRelayGateway builds a gateway whose upstream client is a real
// HTTPUpstreamClient pointed at srv, so tests can observe the credential the
// gateway relays upstream.
func newRelayGateway(t *testing.T, profilePath string, anonymousBrowsing bool, serviceToken, baseURL string) *Gateway {
	t.Helper()
	tmp := t.TempDir()
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
		Instances: []InstanceConfig{{
			ID: "vp1", Title: "VP1", BaseURL: baseURL,
			Token:             serviceToken,
			WorkingRootBase:   tmp,
			AllowedRoots:      []string{tmp},
			UpstreamTimeoutMS: 2000,
		}},
	}
	cfg.Auth.ProfilePath = profilePath
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

// ── 5.1 Profile Replaces Local Token Definitions ─────────────────────────

func TestGateway_ProfileReplacesLocalTokenDefinitions(t *testing.T) {
	legacy := []TokenEntry{{Subject: "legacy@local", Role: "operator", Token: "legacy-token"}}
	gw, _ := newProfileGateway(t, testProfileText, false, legacy)
	rt := gw.Router(nil)

	// The legacy inline token no longer authenticates anyone.
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest(http.MethodGet, "/api/console/instances", "legacy-token", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("legacy token: got %d, want 401", rr.Code)
	}

	// A profile token does.
	rr = httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest(http.MethodGet, "/api/console/instances", "bob-reader-token", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("profile token: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
}

// ── 5.2 Scopes Are Independent ───────────────────────────────────────────

func TestGateway_ScopesAreIndependent(t *testing.T) {
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
	defer srv.Close()

	profilePath := writeProfile(t, testProfileText)
	// Gateway: anonymous browsing ON. The daemon behind it is not involved in
	// this decision - the relay below carries the gateway's own service
	// credential regardless of any daemon-side anonymous-reader setting.
	gw := newRelayGateway(t, profilePath, true, "svc-service-token", srv.URL)
	rt := gw.Router(nil)

	// Anonymous read of console-local content succeeds without a credential.
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/console/instances", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("anonymous console read: got %d, want 200", rr.Code)
	}

	// The relayed upstream read still carries the gateway's service
	// credential - the daemon's own anonymous-reader setting is irrelevant.
	rr = httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/console/instances/vp1/dashboard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("anonymous dashboard: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	if got := rec.lastAuth(); got != "Bearer svc-service-token" {
		t.Fatalf("upstream auth = %q, want the gateway service credential", got)
	}

	// And the daemon authenticator itself still rejects anonymous callers:
	// the daemon's anonymous-reader setting governs only direct API access.
	src, err := accessprofile.NewSource(profilePath, accessprofile.Options{ReloadInterval: -1})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	daemonAuthn := auth.NewProfileAuthenticator(src, 0)
	if _, err := daemonAuthn.Authenticate(context.Background(), ""); err == nil {
		t.Fatal("daemon authenticator admitted an empty credential; daemon scope leaked")
	}
}

// ── 5.3 Shared Identity Consistent ───────────────────────────────────────

func TestGateway_SharedIdentityConsistent(t *testing.T) {
	gw, profilePath := newProfileGateway(t, testProfileText, false, nil)

	src, err := accessprofile.NewSource(profilePath, accessprofile.Options{ReloadInterval: -1})
	if err != nil {
		t.Fatalf("source: %v", err)
	}
	daemonAuthn := auth.NewProfileAuthenticator(src, 0)

	for _, tc := range []struct {
		token       string
		wantSubject string
		wantDaemon  auth.Role
		wantConsole Role
	}{
		{"alice-operator-token", "alice", auth.RoleOperator, RoleOperator},
		{"bob-reader-token", "bob", auth.RoleReader, RoleViewer},
	} {
		// Daemon view.
		dp, err := daemonAuthn.Authenticate(context.Background(), tc.token)
		if err != nil {
			t.Fatalf("%s: daemon authenticate: %v", tc.token, err)
		}
		if dp.Subject != tc.wantSubject {
			t.Errorf("%s: daemon subject = %q, want %q", tc.token, dp.Subject, tc.wantSubject)
		}
		if dp.Role != tc.wantDaemon {
			t.Errorf("%s: daemon role = %q, want %q", tc.token, dp.Role, tc.wantDaemon)
		}

		// Console view of the same token.
		cp, err := gw.Authenticate(authedRequest(http.MethodGet, "/", tc.token, nil))
		if err != nil {
			t.Fatalf("%s: console authenticate: %v", tc.token, err)
		}
		if cp.Subject != dp.Subject {
			t.Errorf("%s: subject mismatch: console %q vs daemon %q", tc.token, cp.Subject, dp.Subject)
		}
		if cp.Role != tc.wantConsole {
			t.Errorf("%s: console role = %q, want %q", tc.token, cp.Role, tc.wantConsole)
		}
	}
}

// ── 5.4 Query-parameter credential removed ───────────────────────────────

func TestGateway_QueryParameterCredentialRejected(t *testing.T) {
	gw, _ := newProfileGateway(t, testProfileText, false, nil)
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/console/instances?access_token=alice-operator-token", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("query credential: got %d, want 401", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "alice-operator-token") {
		t.Fatalf("response echoed the token: %s", rr.Body.String())
	}
}

// ── 5.5 Identity endpoint ────────────────────────────────────────────────

func TestGateway_IdentityAuthenticated(t *testing.T) {
	gw, _ := newProfileGateway(t, testProfileText, false, nil)
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest(http.MethodGet, "/api/console/identity", "bob-reader-token", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("identity: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`"subject":"bob"`, `"role":"viewer"`, `"anonymous":false`} {
		if !strings.Contains(body, want) {
			t.Errorf("identity body missing %s: %s", want, body)
		}
	}
	if strings.Contains(body, "bob-reader-token") {
		t.Errorf("identity response leaked the token: %s", body)
	}
}

func TestGateway_IdentityAnonymous(t *testing.T) {
	gw, _ := newProfileGateway(t, testProfileText, true, nil)
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/console/identity", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("anonymous identity: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{`"subject":"anonymous"`, `"role":"viewer"`, `"anonymous":true`} {
		if !strings.Contains(body, want) {
			t.Errorf("identity body missing %s: %s", want, body)
		}
	}
}

// ── 5.6 Anonymous browsing ───────────────────────────────────────────────

func TestGateway_AnonymousReadAdmitted(t *testing.T) {
	gw, _ := newProfileGateway(t, testProfileText, true, nil)
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/console/instances", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("anonymous read: got %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(strings.ToLower(body), "token") {
		t.Errorf("anonymous read response contains secret material: %s", body)
	}
}

func TestGateway_AnonymousMutationRejected(t *testing.T) {
	gw, _ := newProfileGateway(t, testProfileText, true, nil)
	rt := gw.Router(nil)

	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, httptest.NewRequest(http.MethodPost,
		"/api/console/instances/vp1/stations/STA/pause", strings.NewReader("{}")))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous mutation: got %d, want 401", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "token") {
		t.Errorf("rejection should state a token is required: %s", rr.Body.String())
	}
}

func TestGateway_AnonymousBrowsingDisabled(t *testing.T) {
	gw, _ := newProfileGateway(t, testProfileText, false, nil)
	rt := gw.Router(nil)

	// Reads and identity are both rejected without a credential.
	for _, path := range []string{"/api/console/instances", "/api/console/identity"} {
		rr := httptest.NewRecorder()
		rt.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s without credential: got %d, want 401", path, rr.Code)
		}
	}
}

// ── 5.7 No console user management ──────────────────────────────────────

func TestGateway_NoProfileMutationEndpoint(t *testing.T) {
	gw, _ := newProfileGateway(t, testProfileText, false, nil)
	rt := gw.Router(nil)

	// Plausible user-management paths must not be served.
	for _, path := range []string{
		"/api/console/users",
		"/api/console/users/alice",
		"/api/console/profile",
		"/api/console/tokens",
		"/api/console/access-profile",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
			rr := httptest.NewRecorder()
			rt.ServeHTTP(rr, authedRequest(method, path, "alice-operator-token", strings.NewReader("{}")))
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s %s: got %d, want 404", method, path, rr.Code)
			}
		}
	}
}

func TestGateway_MutatingEndpointDoesNotWriteProfile(t *testing.T) {
	gw, profilePath := newProfileGateway(t, testProfileText, false, nil)
	rt := gw.Router(nil)

	before, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}

	// An operator-authenticated mutating call completes normally...
	rr := httptest.NewRecorder()
	rt.ServeHTTP(rr, authedRequest(http.MethodPost,
		"/api/console/instances/vp1/stations/STA/pause", "alice-operator-token", strings.NewReader("{}")))
	if rr.Code != http.StatusOK {
		t.Fatalf("operator mutation: got %d, want 200 (%s)", rr.Code, rr.Body.String())
	}

	// ...and the profile file on disk is byte-identical.
	after, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("re-read profile: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("profile file changed:\nbefore: %q\nafter:  %q", before, after)
	}
}
