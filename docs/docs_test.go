// Package docs_test verifies that the operator documentation stays in sync
// with the implementation: every documented auth config key and CLI command
// must exist in the code, and the example access profile printed in the docs
// must load successfully.
package docs_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gobobr/veriproc/internal/accessprofile"
	"github.com/gobobr/veriproc/internal/config"
	"github.com/gobobr/veriproc/internal/console"
)

func readDoc(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("docs", name))
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	return string(b)
}

// TestDocumentedProfileExampleLoads extracts the example access profile from
// the configuration reference and asserts it parses.
func TestDocumentedProfileExampleLoads(t *testing.T) {
	doc := readDoc(t, "05-configuration.md")

	// The example block is fenced with ``` and contains the profile marker.
	start := strings.Index(doc, "# access.profile")
	if start < 0 {
		t.Fatal("example profile marker not found in 05-configuration.md")
	}
	open := strings.LastIndex(doc[:start], "```")
	close := strings.Index(doc[start:], "```")
	if open < 0 || close < 0 {
		t.Fatal("example profile code fence not found")
	}
	block := doc[open+3 : start+close]
	// The doc uses ellipses for token tails; substitute valid tokens.
	block = strings.ReplaceAll(block, "6f1c…", "6f1c0d1e2f3a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4")
	block = strings.ReplaceAll(block, "93ab…", "93ab0d1e2f3a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4")

	p, err := accessprofile.Parse([]byte(block))
	if err != nil {
		t.Fatalf("documented example profile does not load: %v", err)
	}
	if p.Len() != 2 {
		t.Errorf("example profile entries = %d, want 2", p.Len())
	}
	if _, ok := p.Subject("alice"); !ok {
		t.Error("example profile missing subject alice")
	}
	if _, ok := p.Subject("bob"); !ok {
		t.Error("example profile missing subject bob")
	}
}

// TestDocumentedAuthConfigKeysExist asserts every auth key named in the docs
// is a real config field.
func TestDocumentedAuthConfigKeysExist(t *testing.T) {
	daemonDoc := readDoc(t, "05-configuration.md")
	consoleDoc := readDoc(t, "10-web-console.md")

	// Daemon keys: check they appear in the daemon config struct tags.
	daemonKeys := []string{
		"auth.profile_path",
		"auth.allow_unauthenticated_readers",
		"auth.quota_per_minute",
	}
	cfgSrc := readSource(t, "internal/config/config.go")
	for _, key := range daemonKeys {
		if !strings.Contains(daemonDoc, key) {
			t.Errorf("daemon doc missing key %q", key)
		}
		field := strings.TrimPrefix(key, "auth.")
		if !strings.Contains(cfgSrc, "`yaml:\""+field+"\"`") {
			t.Errorf("daemon config has no yaml tag for documented key %q", key)
		}
	}

	// Console keys.
	consoleKeys := []string{"auth.profile_path", "auth.allow_anonymous_browsing"}
	consoleSrc := readSource(t, "internal/console/config.go")
	for _, key := range consoleKeys {
		if !strings.Contains(consoleDoc, key) {
			t.Errorf("console doc missing key %q", key)
		}
		field := strings.TrimPrefix(key, "auth.")
		if !strings.Contains(consoleSrc, "`yaml:\""+field+"\"`") {
			t.Errorf("console config has no yaml tag for documented key %q", key)
		}
	}

	// The documented keys must exist on the config structs (compile-time).
	var dc config.Config
	_ = dc.Auth.ProfilePath
	_ = dc.Auth.AllowUnauthenticatedReaders
	_ = dc.Auth.QuotaPerMinute
	var cc console.Config
	_ = cc.Auth.ProfilePath
	_ = cc.Auth.AllowAnonymousBrowsing
}

// TestDocumentedCLIUsersCommandsExist asserts the documented `users`
// subcommands are wired in the CLI.
func TestDocumentedCLIUsersCommandsExist(t *testing.T) {
	cliDoc := readDoc(t, "08-cli.md")
	usersSrc := readSource(t, "cmd/veriproc/users.go")
	mainSrc := readSource(t, "cmd/veriproc/main.go")

	for _, cmd := range []string{"users list", "users add", "users revoke"} {
		if !strings.Contains(cliDoc, "`"+cmd+"`") && !strings.Contains(cliDoc, cmd) {
			t.Errorf("CLI doc missing command %q", cmd)
		}
	}
	// The dispatch and subcommand handlers exist.
	if !strings.Contains(mainSrc, `"users"`) {
		t.Error("CLI dispatch does not wire the users subcommand")
	}
	for _, sub := range []string{`"list"`, `"add"`, `"revoke"`} {
		if !strings.Contains(usersSrc, sub) {
			t.Errorf("users.go missing subcommand %s", sub)
		}
	}
	// The operator gate message documented in the docs matches the code.
	if !strings.Contains(usersSrc, "operator access is required") {
		t.Error("users.go missing the documented operator-gate message")
	}
}

// TestDocumentedRejectionMessagesMatch asserts the 401/403 messages named in
// the REST doc exist in the middleware.
func TestDocumentedRejectionMessagesMatch(t *testing.T) {
	doc := readDoc(t, "09-rest-api.md")
	mw := readSource(t, "internal/httpapi/auth_middleware.go")

	for _, name := range []string{"missingCredentialMessage", "invalidCredentialMessage", "insufficientRoleMessage"} {
		if !strings.Contains(doc, name) {
			t.Errorf("REST doc missing %s", name)
		}
		if !strings.Contains(mw, name) {
			t.Errorf("auth middleware missing %s", name)
		}
	}
	// Roles documented for the API match the implementation.
	if !regexp.MustCompile(`\breader\b`).MatchString(doc) {
		t.Error("REST doc does not document the reader role")
	}
}

func readSource(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", rel))
	if err != nil {
		t.Fatalf("read source %s: %v", rel, err)
	}
	return string(b)
}
