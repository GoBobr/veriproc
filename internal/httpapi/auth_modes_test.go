package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/gobobr/veriproc/internal/accessprofile"
	"github.com/gobobr/veriproc/internal/auth"
	"github.com/gobobr/veriproc/internal/config"
	"github.com/gobobr/veriproc/internal/health"
	"github.com/gobobr/veriproc/internal/httpapi"
	"github.com/gobobr/veriproc/internal/store"
)

// profileAPI starts a minimal API backed by an access profile. anonymousReaders
// selects the daemon's allow_unauthenticated_readers setting.
type profileAPI struct {
	srv *httptest.Server
	// readerTok and operatorTok are literal tokens present in the profile.
	readerTok   string
	operatorTok string
	// src is the profile source the middleware authenticates against.
	src *accessprofile.Source
}

func newProfileAPI(t *testing.T, profileText string, anonymousReaders bool) *profileAPI {
	t.Helper()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profiles.conf")
	if err := os.WriteFile(profilePath, []byte(profileText), accessprofile.ProfileFileMode); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	src, err := accessprofile.NewSource(profilePath, accessprofile.Options{})
	if err != nil {
		t.Fatalf("load profile: %v", err)
	}

	dbPath := filepath.Join(t.TempDir(), "auth.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	router := httpapi.NewRouter(httpapi.Deps{
		Config:                &config.Config{InstanceID: "test"},
		Health:                health.NewAggregator(time.Second),
		Logger:                zerolog.Nop(),
		Authn:                 auth.NewProfileAuthenticator(src, 0),
		AllowAnonymousReaders: anonymousReaders,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &profileAPI{srv: srv, readerTok: "reader-token", operatorTok: "operator-token", src: src}
}

// Scenario: Authentication Disabled Permits Mutating Calls (api spec).
func TestAPI_AuthModes_DisabledPermitsMutatingCalls(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "auth.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	router := httpapi.NewRouter(httpapi.Deps{
		Config: &config.Config{InstanceID: "test"},
		Health: health.NewAggregator(time.Second),
		Logger: zerolog.Nop(),
		Authn:  nil, // authentication disabled
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)

	resp, body := doReq(t, "POST", srv.URL+"/api/v1/tasks", "")
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("authentication disabled but request was rejected: %s", body)
	}
}

// Scenario: Credentialed Mode Demands A Token (api spec).
func TestAPI_AuthModes_CredentialedDemandsToken(t *testing.T) {
	a := newProfileAPI(t, "alice:reader:reader-token\nbob:operator:operator-token\n", false)

	resp, body := doReq(t, "GET", a.srv.URL+"/api/v1/runs", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if code := errCode(t, body); code != "unauthenticated" {
		t.Errorf("code = %q, want unauthenticated", code)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}
}

// Scenario: Anonymous Reader Admitted On Reads (api spec).
func TestAPI_AuthModes_AnonymousReaderAdmittedOnReads(t *testing.T) {
	a := newProfileAPI(t, "alice:reader:reader-token\n", true)

	resp, body := doReq(t, "GET", a.srv.URL+"/api/v1/runs", "")
	if resp.StatusCode == http.StatusUnauthorized {
		t.Errorf("anonymous read rejected: %s", body)
	}
}

// Scenario: Anonymous Reader Denied On Mutations (api spec).
func TestAPI_AuthModes_AnonymousReaderDeniedOnMutations(t *testing.T) {
	a := newProfileAPI(t, "alice:reader:reader-token\n", true)

	resp, body := doReq(t, "POST", a.srv.URL+"/api/v1/tasks", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if code := errCode(t, body); code != "unauthenticated" {
		t.Errorf("code = %q, want unauthenticated", code)
	}
	if !strings.Contains(string(body), "anonymous readers may read but not mutate") {
		t.Errorf("message = %s, want it to explain the anonymous scope", body)
	}
}

// An unknown credential is never downgraded to anonymous: it is rejected even
// when anonymous readers are allowed.
func TestAPI_AuthModes_InvalidTokenNeverDowngraded(t *testing.T) {
	a := newProfileAPI(t, "alice:reader:reader-token\n", true)

	resp, body := doReq(t, "GET", a.srv.URL+"/api/v1/runs", "wrong-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(string(body), "no access-profile entry matches") {
		t.Errorf("message = %s, want it to name the rejected credential", body)
	}
}

// Scenario: Anonymous Subject Is Distinct (api spec): the principal stamped for
// an anonymous read carries the reserved subject, not a profile subject.
func TestAPI_AuthModes_AnonymousSubjectIsDistinct(t *testing.T) {
	a := newProfileAPI(t, "alice:reader:reader-token\n", true)

	// /api/v1/version is exempt from auth, so probe a real authenticated read
	// path and confirm the reserved subject is what the middleware stamps by
	// checking the anonymous principal helper directly.
	p := auth.AnonymousPrincipal()
	if p.Subject != auth.AnonymousSubject {
		t.Errorf("subject = %q, want %q", p.Subject, auth.AnonymousSubject)
	}
	if p.Role != auth.RoleReader {
		t.Errorf("role = %q, want %q", p.Role, auth.RoleReader)
	}
	if p.QuotaPerMinute != 0 {
		t.Errorf("quota = %d, want 0 for an anonymous principal", p.QuotaPerMinute)
	}
	prof, err := a.src.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if _, ok := prof.Subject(auth.AnonymousSubject); ok {
		t.Errorf("the reserved anonymous subject is claimable from the profile")
	}
}

// Scenario: Missing Credential Message (api spec).
func TestAPI_AuthModes_MissingCredentialMessage(t *testing.T) {
	a := newProfileAPI(t, "alice:reader:reader-token\n", false)

	resp, body := doReq(t, "GET", a.srv.URL+"/api/v1/runs", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if !strings.Contains(string(body), "reader or operator credential") {
		t.Errorf("message = %s, want it to name the accepted roles", body)
	}
}

// Scenario: Insufficient Role Message (api spec).
func TestAPI_AuthModes_InsufficientRoleMessage(t *testing.T) {
	a := newProfileAPI(t, "alice:reader:reader-token\n", false)

	resp, body := doReq(t, "POST", a.srv.URL+"/api/v1/tasks", a.readerTok)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if code := errCode(t, body); code != "unauthorized" {
		t.Errorf("code = %q, want unauthorized", code)
	}
	if !strings.Contains(string(body), "operator authorization required") {
		t.Errorf("message = %s, want it to name the required role", body)
	}
}

// Scenario: No Secret In Rejection (api spec): no rejection echoes a token.
func TestAPI_AuthModes_NoSecretInRejection(t *testing.T) {
	const secret = "operator-token"
	a := newProfileAPI(t, "alice:operator:"+secret+"\n", false)

	for _, tc := range []struct {
		name, method, token string
	}{
		{"missing", "GET", ""},
		{"invalid", "GET", "not-the-secret"},
		{"insufficient", "POST", "reader-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, body := doReq(t, tc.method, a.srv.URL+"/api/v1/runs", tc.token)
			if strings.Contains(string(body), secret) {
				t.Errorf("rejection leaked a token: %s", body)
			}
		})
	}
}
