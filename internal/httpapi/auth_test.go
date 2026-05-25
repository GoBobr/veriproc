package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"

	"github.com/eum/veriproc/internal/auth"
	"github.com/eum/veriproc/internal/config"
	"github.com/eum/veriproc/internal/health"
	"github.com/eum/veriproc/internal/httpapi"
	"github.com/eum/veriproc/internal/store"
)

// authTestAPI starts a minimal API with auth and quota wired in. No Tasks/Runs
// services are mounted; we exercise the middleware via /api/v1/health and via
// a POST to a missing route (which still passes through middleware).
type authTestAPI struct {
	srv  *httptest.Server
	tok  string
	tok2 string
}

func newAuthAPI(t *testing.T, quotaPerMin int) *authTestAPI {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "auth.db")
	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(context.Background(), st); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	authn := auth.NewStaticAuthenticator(map[string]auth.Principal{
		"op-secret":     {Subject: "alice", Role: auth.RoleOperator, QuotaPerMinute: quotaPerMin},
		"reader-secret": {Subject: "bob", Role: auth.RoleReader, QuotaPerMinute: 0},
	})
	q := auth.NewQuotaEnforcer(time.Now)
	router := httpapi.NewRouter(httpapi.Deps{
		Config: &config.Config{InstanceID: "test"},
		Health: health.NewAggregator(time.Second),
		Logger: zerolog.Nop(),
		Authn:  authn,
		Quota:  q,
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return &authTestAPI{srv: srv, tok: "op-secret", tok2: "reader-secret"}
}

func doReq(t *testing.T, method, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, body
}

func errCode(t *testing.T, body []byte) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("decode err: %v body=%s", err, body)
	}
	return env.Error.Code
}

// TestAPI_Auth_HealthSkipped — health and readiness are exempt from auth.
func TestAPI_Auth_HealthSkipped(t *testing.T) {
	a := newAuthAPI(t, 0)
	resp, _ := doReq(t, "GET", a.srv.URL+"/api/v1/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestAPI_Auth_MissingToken — protected endpoint without token → 401.
func TestAPI_Auth_MissingToken(t *testing.T) {
	a := newAuthAPI(t, 0)
	resp, body := doReq(t, "GET", a.srv.URL+"/api/v1/runs", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if c := errCode(t, body); c != "unauthenticated" {
		t.Errorf("code = %q, want unauthenticated", c)
	}
}

// TestAPI_Auth_InvalidToken — unknown token → 401 unauthenticated.
func TestAPI_Auth_InvalidToken(t *testing.T) {
	a := newAuthAPI(t, 0)
	resp, body := doReq(t, "GET", a.srv.URL+"/api/v1/runs", "wrong")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
	if c := errCode(t, body); c != "unauthenticated" {
		t.Errorf("code = %q, want unauthenticated", c)
	}
}

// TestAPI_Auth_ReaderForbiddenOnMutator — reader role on POST → 403.
func TestAPI_Auth_ReaderForbiddenOnMutator(t *testing.T) {
	a := newAuthAPI(t, 0)
	resp, body := doReq(t, "POST", a.srv.URL+"/api/v1/tasks", a.tok2)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d body=%s, want 403", resp.StatusCode, body)
	}
	if c := errCode(t, body); c != "unauthorized" {
		t.Errorf("code = %q, want unauthorized", c)
	}
}

// TestAPI_Auth_OperatorAllowed — operator passes auth (response may be 404
// because no Tasks service is mounted, but it must NOT be 401/403).
func TestAPI_Auth_OperatorAllowed(t *testing.T) {
	a := newAuthAPI(t, 0)
	resp, body := doReq(t, "POST", a.srv.URL+"/api/v1/tasks", a.tok)
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		t.Errorf("status = %d (auth blocked operator) body=%s", resp.StatusCode, body)
	}
}

// TestAPI_Quota_Exceeded — once an operator exceeds quotaPerMinute,
// subsequent mutating requests return 429 quota_exceeded with retryable=true.
func TestAPI_Quota_Exceeded(t *testing.T) {
	a := newAuthAPI(t, 2)
	for i := 0; i < 2; i++ {
		resp, _ := doReq(t, "POST", a.srv.URL+"/api/v1/tasks", a.tok)
		if resp.StatusCode == http.StatusTooManyRequests {
			t.Fatalf("hit 429 too early on call %d", i)
		}
	}
	resp, body := doReq(t, "POST", a.srv.URL+"/api/v1/tasks", a.tok)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s, want 429", resp.StatusCode, body)
	}
	if c := errCode(t, body); c != "quota_exceeded" {
		t.Errorf("code = %q, want quota_exceeded", c)
	}
	if !strings.Contains(string(body), "\"retryable\":true") {
		t.Errorf("expected retryable=true in body: %s", body)
	}
}
