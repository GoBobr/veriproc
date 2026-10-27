package accessprofile

import (
	"errors"
	"strings"
	"testing"
)

func mustParse(t *testing.T, text string) *Profile {
	t.Helper()
	p, err := Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse(%q) failed: %v", text, err)
	}
	return p
}

// Scenario: Profile Entry Grants Role (access-profiles spec).
func TestParse_EntryGrantsRole(t *testing.T) {
	p := mustParse(t, "alice:reader:token-value\n")

	e, ok := p.Subject("alice")
	if !ok {
		t.Fatalf("subject alice not bound after parse")
	}
	if e.Role != RoleReader {
		t.Errorf("role = %q, want %q", e.Role, RoleReader)
	}
	if l := e.Label; l != "" {
		t.Errorf("label = %q, want empty", l)
	}
	if _, ok := p.Lookup("token-value"); !ok {
		t.Errorf("token-value did not authenticate")
	}
}

// Scenario: Label Annotation On Entry (access-profiles spec).
func TestParse_LabelAnnotationOnEntry(t *testing.T) {
	p := mustParse(t, "# fleet profile\nalice:reader:token-value  # Alice - laptop CLI\n")

	e, ok := p.Subject("alice")
	if !ok {
		t.Fatalf("subject alice not bound after parse")
	}
	if e.Label != "Alice - laptop CLI" {
		t.Errorf("label = %q, want %q", e.Label, "Alice - laptop CLI")
	}
	// The leading comment line carries no entry.
	if p.Len() != 1 {
		t.Errorf("entry count = %d, want 1", p.Len())
	}
}

// Scenario: Role Outside Taxonomy Rejected (access-profiles spec).
func TestParse_RoleOutsideTaxonomyRejected(t *testing.T) {
	const text = "alice:reader:token-value\nbob:admin:token-value\n"
	_, err := Parse([]byte(text))
	if err == nil {
		t.Fatalf("Parse succeeded, want an error for role %q", "admin")
	}
	if !errors.Is(err, ErrRole) {
		t.Errorf("err = %v, want ErrRole", err)
	}
	var le *LineError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v, want *LineError", err)
	}
	if le.Line != 2 {
		t.Errorf("Line = %d, want 2", le.Line)
	}
	if !strings.Contains(le.Text, "bob:admin") {
		t.Errorf("Text = %q, want it to name the offending line", le.Text)
	}
}

// A revision either validates or grants nothing: one bad line fails the load.
func TestParse_SingleBadLineRejectsRevision(t *testing.T) {
	cases := []struct {
		name string
		text string
		want error
	}{
		{"missing token", "alice:reader\n", ErrFields},
		{"separator in token", "alice:reader:to:ken\n", ErrTokenColon},
		{"empty subject", ":reader:token-value\n", ErrSubject},
		{"empty token", "alice:reader:\n", ErrToken},
		{"bad digest mark", "alice:reader:sha256:notahexdigest\n", ErrDigest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("ok:reader:ok-value\n" + tc.text))
			if !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

// One token must resolve to exactly one subject.
func TestParse_AmbiguousTokenRejected(t *testing.T) {
	_, err := Parse([]byte("alice:reader:shared-secret\nbob:reader:shared-secret\n"))
	if !errors.Is(err, ErrAmbiguousToken) {
		t.Errorf("err = %v, want ErrAmbiguousToken", err)
	}
}

// Scenario: Digest Entry Authenticates (access-profiles spec).
func TestLookup_DigestEntry(t *testing.T) {
	const token = "s3cr3t-token-value"
	p := mustParse(t, "alice:operator:sha256:" + Digest(token) + "\n")

	entry, ok := p.Lookup(token)
	if !ok {
		t.Fatalf("digest entry did not authenticate")
	}
	if entry.Role != RoleOperator {
		t.Errorf("role = %q, want %q", entry.Role, RoleOperator)
	}
}

// Scenario: Literal Entry Still Authenticates (access-profiles spec).
func TestLookup_LiteralEntry(t *testing.T) {
	const token = "s3cr3t-token-value"
	p := mustParse(t, "alice:operator:"+token+"\n")

	if _, ok := p.Lookup(token); !ok {
		t.Fatalf("literal entry did not authenticate")
	}
}

// Scenario: both forms authenticate the same caller identically (task 4.1).
func TestLookup_LiteralAndDigestFormsAgree(t *testing.T) {
	const token = "s3cr3t-token-value"
	literal := mustParse(t, "alice:reader:"+token+"\n")
	digest := mustParse(t, "alice:reader:sha256:"+Digest(token)+"\n")

	el, okl := literal.Lookup(token)
	ed, okd := digest.Lookup(token)
	if !okl || !okd {
		t.Fatalf("lookup literal=%v digest=%v, want both true", okl, okd)
	}
	if el.Subject != ed.Subject || el.Role != ed.Role {
		t.Errorf("forms disagree: %+v vs %+v", el, ed)
	}
}

func TestLookup_UnknownTokenRejected(t *testing.T) {
	p := mustParse(t, "alice:reader:token-value\n")
	if _, ok := p.Lookup("wrong"); ok {
		t.Errorf("unknown token authenticated")
	}
	if _, ok := p.Lookup(" "); ok {
		t.Errorf("blank token authenticated")
	}
}

// A digest-marked field is stored verbatim: parsing and rewriting must not
// convert it back into a literal.
func TestAppend_PreservesDigestForm(t *testing.T) {
	const token = "another-token"
	p := mustParse(t, "alice:operator:sha256:"+Digest(token)+"\n")

	next, err := p.Append(NewEntry("carol", RoleReader, "Carol - CLI", "new-secret"))
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}
	out := string(next.Bytes())
	if !strings.Contains(out, "alice:operator:sha256:") {
		t.Errorf("dialect changed for existing entry:\n%s", out)
	}
	if !strings.Contains(out, "carol:reader:sha256:"+Digest("new-secret")+"  # Carol - CLI") {
		t.Errorf("new entry not as expected:\n%s", out)
	}
	if _, ok := next.Lookup("new-secret"); !ok {
		t.Errorf("appended entry does not authenticate")
	}
}

// Scenario: Profile Path Configuration - Append refuses an existing subject so
// extending a grant stays an explicit hand edit.
func TestAppend_ExistingSubjectRejected(t *testing.T) {
	p := mustParse(t, "alice:reader:token-value\n")
	if _, err := p.Append(NewEntry("alice", RoleOperator, "", "another")); !errors.Is(err, ErrSubjectExists) {
		t.Errorf("err = %v, want ErrSubjectExists", err)
	}
}

// Scenario: Revoke Entry Removes Access (format-level half; CLI half in
// cmd/veriproc).
func TestRevoke_RemovesSubjectAndAnnotations(t *testing.T) {
	p := mustParse(t, "alice:reader:token-value  # Alice - laptop\nbob:operator:other-secret\n")
	next, err := p.Revoke("alice")
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	if next.HasSubject("alice") {
		t.Errorf("alice still present after revoke")
	}
	if !next.HasSubject("bob") {
		t.Errorf("bob removed by a revoke of alice")
	}
	if _, ok := next.Lookup("token-value"); ok {
		t.Errorf("revoked token still authenticates")
	}
	if strings.Contains(string(next.Bytes()), "Alice - laptop") {
		t.Errorf("revoked entry's annotation survived:\n%s", next.Bytes())
	}
	if _, err := p.Revoke("mallory"); !errors.Is(err, ErrNoSubject) {
		t.Errorf("unknown subject err = %v, want ErrNoSubject", err)
	}
}

// Comments, blank lines, and entry order survive a rewrite.
func TestBytes_PreservesFileShape(t *testing.T) {
	const text = "# header comment\n\nalice:reader:token-value   \nbob:operator:other-secret\n"
	p := mustParse(t, text)
	if got := string(p.Bytes()); got != text {
		t.Errorf("Bytes() = %q, want %q", got, text)
	}
}

func TestParseRole(t *testing.T) {
	if _, err := ParseRole("reader"); err != nil {
		t.Errorf("reader rejected: %v", err)
	}
	if _, err := ParseRole("operator"); err != nil {
		t.Errorf("operator rejected: %v", err)
	}
	if _, err := ParseRole("admin"); !errors.Is(err, ErrRole) {
		t.Errorf("admin err = %v, want ErrRole", err)
	}
	if !IsValidRole(RoleOperator) || IsValidRole("admin") {
		t.Errorf("IsValidRole misclassifies")
	}
}