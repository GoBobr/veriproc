package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gobobr/veriproc/internal/accessprofile"
)

// authAPI is a backend that rejects requests the way the daemon does, so the
// CLI's access-denied rendering can be exercised end to end.
func authAPI(t *testing.T, status int, code, message string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"` + message + `"}}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Scenario: Missing Token Message (cli spec).
func TestCLI_AccessDenied_MissingTokenMessage(t *testing.T) {
	srv := authAPI(t, http.StatusUnauthorized, "unauthenticated", "missing bearer token")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", srv.URL, "run", "list"}, &stdout, &stderr)

	if code != ExitAuth {
		t.Errorf("exit = %d, want %d", code, ExitAuth)
	}
	msg := stderr.String()
	if !strings.Contains(msg, "VERIPROC_TOKEN") || !strings.Contains(msg, "--token") {
		t.Errorf("stderr = %q, want it to name VERIPROC_TOKEN and --token", msg)
	}
	if !strings.Contains(msg, "reader or operator") {
		t.Errorf("stderr = %q, want it to name the accepted roles", msg)
	}
}

// Scenario: Insufficient Role Message (cli spec).
func TestCLI_AccessDenied_InsufficientRoleMessage(t *testing.T) {
	srv := authAPI(t, http.StatusForbidden, "unauthorized", "operator role required")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", srv.URL, "--token", "reader-tok", "station", "pause", "ST-A"}, &stdout, &stderr)

	if code != ExitAuth {
		t.Errorf("exit = %d, want %d", code, ExitAuth)
	}
	if !strings.Contains(stderr.String(), "operator access is required") {
		t.Errorf("stderr = %q, want it to name the required role", stderr.String())
	}
}

// Scenario: No Secret In Rejection (cli spec): the hint never echoes a token.
func TestCLI_AccessDenied_NeverEchoesToken(t *testing.T) {
	const secret = "super-secret-token-value"
	srv := authAPI(t, http.StatusUnauthorized, "unauthenticated", "invalid bearer token")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", srv.URL, "--token", secret, "run", "list"}, &stdout, &stderr)

	if code != ExitAuth {
		t.Errorf("exit = %d, want %d", code, ExitAuth)
	}
	if strings.Contains(stderr.String(), secret) {
		t.Errorf("stderr leaked the token: %q", stderr.String())
	}
}

// Scenario: Anonymous Read Succeeds (cli spec): a backend that admits
// anonymous reads lets a token-less read command succeed.
func TestCLI_AccessDenied_AnonymousReadSucceeds(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(w, 200, map[string]any{"items": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", srv.URL, "run", "list"}, &stdout, &stderr)
	if code != ExitOK {
		t.Errorf("exit = %d, want %d; stderr=%q", code, ExitOK, stderr.String())
	}
}

// guestFallbackAPI is a backend that behaves like the daemon with anonymous
// readers enabled: a request carrying an unrecognized credential is served as
// a read-only guest, and the response says so through the rejection header.
// Mutating requests are refused regardless.
func guestFallbackAPI(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthenticated","message":"a reader or operator credential is required to mutate state"}}`))
			return
		}
		if tok := r.Header.Get("Authorization"); tok != "" {
			// The credential did not match: serve the read as a guest and
			// report the rejection.
			w.Header().Set("X-VeriProc-Auth", "credential-rejected")
		}
		writeJSON(w, 200, map[string]any{"items": []any{}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// Scenario: Rejected Credential Reports Guest Fallback (cli spec): a read
// served as a guest after a rejection warns on stderr, never echoes the
// credential, and still exits successfully.
func TestCLI_AccessDenied_RejectedCredentialReportsGuestFallback(t *testing.T) {
	const secret = "stale-token-value"
	srv := guestFallbackAPI(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", srv.URL, "--token", secret, "run", "list"}, &stdout, &stderr)

	if code != ExitOK {
		t.Errorf("exit = %d, want %d; stderr=%q", code, ExitOK, stderr.String())
	}
	msg := stderr.String()
	if !strings.Contains(msg, "rejected") {
		t.Errorf("stderr = %q, want it to report the rejected credential", msg)
	}
	if !strings.Contains(msg, "guest") {
		t.Errorf("stderr = %q, want it to report the guest fallback", msg)
	}
	if strings.Contains(msg, secret) {
		t.Errorf("stderr leaked the token: %q", msg)
	}
}

// The warning is printed once, not once per request.
func TestCLI_AccessDenied_RejectedCredentialWarnsOnce(t *testing.T) {
	srv := guestFallbackAPI(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--api-url", srv.URL, "--token", "stale", "run", "list"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("exit = %d, want %d; stderr=%q", code, ExitOK, stderr.String())
	}
	if n := strings.Count(stderr.String(), "rejected"); n != 1 {
		t.Errorf("warning printed %d times, want 1: %q", n, stderr.String())
	}
}

// Scenario: Rejected Credential On Mutation Fails (cli spec): the guest
// fallback never applies to a mutating command.
func TestCLI_AccessDenied_RejectedCredentialOnMutationFails(t *testing.T) {
	srv := guestFallbackAPI(t)

	var stdout, stderr bytes.Buffer
	code := run([]string{"--api-url", srv.URL, "--token", "stale", "station", "pause", "ST-A"}, &stdout, &stderr)

	if code != ExitAuth {
		t.Errorf("exit = %d, want %d; stderr=%q", code, ExitAuth, stderr.String())
	}
	if strings.Contains(stderr.String(), "guest") {
		t.Errorf("stderr should not report a guest fallback for a mutation: %q", stderr.String())
	}
}

// --- users subcommand -------------------------------------------------------

// seedProfile writes a profile containing an operator and a reader entry and
// returns the path plus their literal tokens.
func seedProfile(t *testing.T) (path, operatorTok, readerTok string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "profiles.conf")
	operatorTok, readerTok = "operator-token-abc", "reader-token-xyz"
	content := "alice:operator:" + operatorTok + "  # Alice - ops\n" +
		"bob:reader:" + readerTok + "  # Bob - read only\n"
	if err := os.WriteFile(path, []byte(content), accessprofile.ProfileFileMode); err != nil {
		t.Fatalf("seed profile: %v", err)
	}
	return path, operatorTok, readerTok
}

// runUsers invokes the CLI with a users subcommand against a profile path.
// Flags are placed before the positional arguments, matching the flag
// package's stop-at-first-argument parsing.
func runUsers(t *testing.T, profilePath, token string, args ...string) (int, string, string) {
	t.Helper()
	if len(args) == 0 {
		t.Fatalf("runUsers: missing subcommand")
	}
	full := []string{"users", args[0]}
	if profilePath != "" {
		full = append(full, "--profile", profilePath)
	}
	full = append(full, args[1:]...)
	if token != "" {
		full = append([]string{"--token", token}, full...)
	}
	var stdout, stderr bytes.Buffer
	code := run(full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// Scenario: List Profile Entries (cli spec).
func TestCLI_Users_List(t *testing.T) {
	path, _, readerTok := seedProfile(t)
	code, out, errOut := runUsers(t, path, "", "list")

	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errOut)
	}
	if !strings.Contains(out, "alice") || !strings.Contains(out, "operator") {
		t.Errorf("stdout = %q, want the operator entry", out)
	}
	if !strings.Contains(out, "Alice - ops") {
		t.Errorf("stdout = %q, want the entry label", out)
	}
	if !strings.Contains(out, "bob") || !strings.Contains(out, "reader") {
		t.Errorf("stdout = %q, want the reader entry", out)
	}
	// Scenario: List Profile Entries - no token value is ever printed.
	if strings.Contains(out, readerTok) {
		t.Errorf("stdout leaked a token: %q", out)
	}
}

// Scenario: Profile Path Configuration (cli spec): the path comes from the
// flag, then from the environment.
func TestCLI_Users_ProfilePathConfiguration(t *testing.T) {
	path, _, _ := seedProfile(t)

	// Flag wins.
	code, out, _ := runUsers(t, path, "", "list")
	if code != ExitOK || !strings.Contains(out, path) {
		t.Errorf("flag path: exit=%d out=%q, want the file named", code, out)
	}

	// Environment fallback.
	t.Setenv("VERIPROC_ACCESS_PROFILE", path)
	var stdout, stderr bytes.Buffer
	code = run([]string{"users", "list"}, &stdout, &stderr)
	if code != ExitOK || !strings.Contains(stdout.String(), path) {
		t.Errorf("env path: exit=%d out=%q, want the file named", code, stdout.String())
	}

	// Neither configured: usage error naming both mechanisms.
	t.Setenv("VERIPROC_ACCESS_PROFILE", "")
	stdout.Reset()
	stderr.Reset()
	code = run([]string{"users", "list"}, &stdout, &stderr)
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(stderr.String(), "--profile") || !strings.Contains(stderr.String(), "VERIPROC_ACCESS_PROFILE") {
		t.Errorf("stderr = %q, want it to name both ways to set the path", stderr.String())
	}
}

// Scenario: Add Entry Mints Token (cli spec).
func TestCLI_Users_Add(t *testing.T) {
	path, operatorTok, _ := seedProfile(t)
	code, out, errOut := runUsers(t, path, operatorTok, "add", "--label", "Carol - laptop", "carol", "reader")

	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errOut)
	}
	// The plaintext token is printed exactly once.
	marker := "token (shown once, store it now): "
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("stdout = %q, want the minted token printed once", out)
	}
	minted := strings.TrimSpace(strings.Split(out[idx+len(marker):], "\n")[0])
	if len(minted) != 64 {
		t.Errorf("minted token length = %d, want 64", len(minted))
	}
	if strings.Count(out, minted) != 1 {
		t.Errorf("minted token printed %d times, want exactly once", strings.Count(out, minted))
	}

	// The profile stores only the digest, and the new entry authenticates.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	if strings.Contains(string(data), minted) {
		t.Errorf("plaintext token written to the profile:\n%s", data)
	}
	p, err := accessprofile.LoadFile(path)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	entry, ok := p.Lookup(minted)
	if !ok {
		t.Fatalf("minted token does not authenticate against the profile")
	}
	if entry.Subject != "carol" || entry.Role != accessprofile.RoleReader {
		t.Errorf("entry = %+v, want carol/reader", entry)
	}
	if entry.Label != "Carol - laptop" {
		t.Errorf("label = %q, want %q", entry.Label, "Carol - laptop")
	}
	// Existing entries survive an append.
	if !p.HasSubject("alice") || !p.HasSubject("bob") {
		t.Errorf("existing entries lost:\n%s", data)
	}
}

// Scenario: Revoke Entry Removes Access (cli spec).
func TestCLI_Users_Revoke(t *testing.T) {
	path, operatorTok, readerTok := seedProfile(t)
	code, _, errOut := runUsers(t, path, operatorTok, "revoke", "bob")

	if code != ExitOK {
		t.Fatalf("exit = %d, want 0; stderr=%q", code, errOut)
	}
	p, err := accessprofile.LoadFile(path)
	if err != nil {
		t.Fatalf("reload profile: %v", err)
	}
	if p.HasSubject("bob") {
		t.Errorf("bob still present after revoke")
	}
	if _, ok := p.Lookup(readerTok); ok {
		t.Errorf("revoked token still authenticates")
	}
	if !p.HasSubject("alice") {
		t.Errorf("unrelated entry lost by a revoke")
	}
	// Revoking an unknown subject is a validation failure, not a silent no-op.
	if code, _, _ := runUsers(t, path, operatorTok, "revoke", "mallory"); code != ExitValidation {
		t.Errorf("unknown subject exit = %d, want %d", code, ExitValidation)
	}
}

// Scenario: Mutating Command Requires Operator In Profile (cli spec).
func TestCLI_Users_AddRequiresOperatorInProfile(t *testing.T) {
	path, _, readerTok := seedProfile(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	code, _, errOut := runUsers(t, path, readerTok, "add", "carol", "reader")
	if code != ExitAuth {
		t.Errorf("reader exit = %d, want %d", code, ExitAuth)
	}
	if !strings.Contains(errOut, "operator access is required") {
		t.Errorf("stderr = %q, want it to name the required role", errOut)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("profile changed despite the refusal:\n%s", after)
	}
}

// Scenario: Mutating Command Requires No Credential Without Operator Entry
// (cli spec): a missing or unknown credential refuses too.
func TestCLI_Users_AddRequiresCredential(t *testing.T) {
	path, _, _ := seedProfile(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	for name, token := range map[string]string{"missing": "", "unknown": "not-in-profile"} {
		t.Run(name, func(t *testing.T) {
			code, _, errOut := runUsers(t, path, token, "add", "carol", "reader")
			if code != ExitAuth {
				t.Errorf("exit = %d, want %d", code, ExitAuth)
			}
			if !strings.Contains(errOut, "operator access is required") {
				t.Errorf("stderr = %q, want it to name the required role", errOut)
			}
		})
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("profile changed despite the refusal:\n%s", after)
	}
}

// The gate applies to revoke as well as add.
func TestCLI_Users_RevokeRequiresOperator(t *testing.T) {
	path, _, readerTok := seedProfile(t)
	code, _, _ := runUsers(t, path, readerTok, "revoke", "alice")
	if code != ExitAuth {
		t.Errorf("exit = %d, want %d", code, ExitAuth)
	}
	p, err := accessprofile.LoadFile(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !p.HasSubject("alice") {
		t.Errorf("alice revoked by a reader credential")
	}
}

// A role outside the taxonomy is a usage error before any file is touched.
func TestCLI_Users_AddRejectsUnknownRole(t *testing.T) {
	path, operatorTok, _ := seedProfile(t)
	code, _, errOut := runUsers(t, path, operatorTok, "add", "carol", "admin")
	if code != ExitUsage {
		t.Errorf("exit = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "reader or operator") {
		t.Errorf("stderr = %q, want it to name the accepted roles", errOut)
	}
}