package accessprofile

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTempProfile writes text to a fresh temp file and returns its path.
func newTempProfile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "profiles.conf")
	if text != "" {
		if err := os.WriteFile(path, []byte(text), ProfileFileMode); err != nil {
			t.Fatalf("write temp profile: %v", err)
		}
	}
	return path
}

// newClockedSource returns a Source driven by an adjustable clock together
// with a function advancing that clock.
func newClockedSource(t *testing.T, path string, interval time.Duration, warn WarnFunc) (*Source, func(time.Duration), error) {
	t.Helper()
	now := time.Now()
	clock := func() time.Time { return now }
	src, err := NewSource(path, Options{
		ReloadInterval: interval,
		Now:            clock,
		Warn:           warn,
	})
	advance := func(d time.Duration) { now = now.Add(d) }
	return src, advance, err
}

// Scenario: Grant Takes Effect (access-profiles spec).
func TestSource_GrantTakesEffectWithoutRestart(t *testing.T) {
	path := newTempProfile(t, "alice:reader:token-one\n")
	src, advance, err := newClockedSource(t, path, time.Minute, nil)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	if err := os.WriteFile(path, []byte("alice:reader:token-one\nbob:operator:token-two\n"), ProfileFileMode); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	// Still inside the reload interval: the old revision remains active.
	if _, err := src.Profile(); err != nil {
		t.Fatalf("Profile: %v", err)
	}

	advance(time.Minute + time.Second)
	p, err := src.Profile()
	if err != nil {
		t.Fatalf("Profile after interval: %v", err)
	}
	entry, ok := p.Lookup("token-two")
	if !ok {
		t.Fatalf("granted subject not effective after reload interval")
	}
	if entry.Subject != "bob" {
		t.Errorf("subject = %q, want %q", entry.Subject, "bob")
	}
}

// Scenario: Revocation Takes Effect (access-profiles spec).
func TestSource_RevocationTakesEffectWithoutRestart(t *testing.T) {
	path := newTempProfile(t, "alice:reader:token-one\nbob:operator:token-two\n")
	src, advance, err := newClockedSource(t, path, time.Minute, nil)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if _, ok := mustLookup(t, src, "token-one"); !ok {
		t.Fatalf("alice authenticates before revoke")
	}

	if err := os.WriteFile(path, []byte("bob:operator:token-two\n"), ProfileFileMode); err != nil {
		t.Fatalf("revoke alice: %v", err)
	}
	advance(time.Minute + time.Second)

	p, err := src.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if _, ok := p.Lookup("token-one"); ok {
		t.Errorf("revoked token still authenticates after reload interval")
	}
}

// Scenario: Malformed Entry Revokes Nothing On Reload (access-profiles spec).
func TestSource_InvalidRevisionKeepsPrevious(t *testing.T) {
	path := newTempProfile(t, "alice:reader:token-one\n")
	src, advance, err := newClockedSource(t, path, time.Minute, nil)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}

	if err := os.WriteFile(path, []byte("bob:admin:token-two\ncarol:reader:token-three\n"), ProfileFileMode); err != nil {
		t.Fatalf("write invalid profile: %v", err)
	}
	advance(time.Minute + time.Second)

	p, err := src.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	// The previously validated revision is still authoritative.
	if _, ok := p.Lookup("token-one"); !ok {
		t.Errorf("previously valid subject lost after a failed reload")
	}
	// The invalid revision granted nothing: its lines never became active.
	if _, ok := p.Lookup("token-two"); ok {
		t.Errorf("entry from an invalid revision authenticates")
	}
}

// Scenario: Profile File Permission Warning (access-profiles spec).
func TestSource_PermissiveFileWarned(t *testing.T) {
	var warnings []string

	path := filepath.Join(t.TempDir(), "profiles.conf")
	if err := os.WriteFile(path, []byte("alice:reader:token-one\n"), 0o644); err != nil {
		t.Fatalf("write permissive profile: %v", err)
	}
	_, _, err := newClockedSource(t, path, time.Minute, func(e error) {
		warnings = append(warnings, e.Error())
	})
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatalf("no warning for a readable-by-others profile")
	}
	if !strings.Contains(warnings[0], path) {
		t.Errorf("warning = %q, want it to name the file", warnings[0])
	}
	if !strings.Contains(warnings[0], "644") {
		t.Errorf("warning = %q, want it to state the perm bits", warnings[0])
	}
}

// Scenario: Management Creates Restrictive File (access-profiles spec).
func TestWriteFile_CreatesOwnerOnlyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profiles.conf")
	if err := WriteFile(path, []byte("alice:reader:token-one\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != ProfileFileMode {
		t.Errorf("mode = %o, want %o", got, ProfileFileMode)
	}
	// An existing file is also forced back to owner-only on rewrite.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := WriteFile(path, []byte("bob:reader:token-two\n")); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != ProfileFileMode {
		t.Errorf("mode after rewrite = %o, want %o", got, ProfileFileMode)
	}
}

// Scenario: Failed Update Keeps Access (access-profiles spec).
func TestWriteFile_InvalidContentLeavesPreviousFile(t *testing.T) {
	path := newTempProfile(t, "alice:reader:token-one\n")
	if err := WriteFile(path, []byte("bob:admin:token-two\n")); !errors.Is(err, ErrRole) {
		t.Fatalf("err = %v, want ErrRole", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := string(data); got != "alice:reader:token-one\n" {
		t.Errorf("file = %q, want the previous revision untouched", got)
	}
}

// Scenario: Reader Never Sees Partial File (access-profiles spec): replacement
// is staged out of band and published by rename, so no reader can observe the
// intermediate content.
func TestWriteFile_StageAndPublishAreSeparate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.conf")
	if err := os.WriteFile(path, []byte("alice:reader:token-one\n"), ProfileFileMode); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := WriteFile(path, []byte("bob:reader:token-two\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("leftover staging files: %v", names)
	}
}

// Scenario: Add Entry Mints Token (management-command half of the CLI task).
func TestMintToken_HighEntropyAndHex(t *testing.T) {
	a, err := MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	b, err := MintToken()
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if a == b {
		t.Errorf("two minted tokens are equal")
	}
	if len(a) != 64 {
		t.Errorf("token length = %d, want 64", len(a))
	}
	if _, err := hex.DecodeString(a); err != nil {
		t.Errorf("minted token is not hex: %v", err)
	}
}

// A vanished profile file is reported as a warning, not as a lockout.
func TestSource_MissingFileWarnsAndKeepsRevision(t *testing.T) {
	var warnings []string
	path := newTempProfile(t, "alice:reader:token-one\n")
	src, advance, err := newClockedSource(t, path, time.Minute, func(e error) {
		warnings = append(warnings, e.Error())
	})
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove profile: %v", err)
	}
	advance(time.Minute + time.Second)

	p, err := src.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if _, ok := p.Lookup("token-one"); !ok {
		t.Errorf("revision lost when the file became unreadable")
	}
	if len(warnings) == 0 {
		t.Errorf("no warning for an unreadable profile")
	}
}

func TestNewSource_UnreadableOrInvalidFailsLoudly(t *testing.T) {
	if _, err := NewSource(filepath.Join(t.TempDir(), "nope.conf"), Options{}); err == nil {
		t.Errorf("missing file accepted at construction")
	}
	path := newTempProfile(t, "alice:admin:token-one\n")
	if _, err := NewSource(path, Options{}); !errors.Is(err, ErrRole) {
		t.Errorf("err = %v, want ErrRole", err)
	}
}

// A negative interval freezes the source for processes that prefer restarts.
func TestSource_NegativeIntervalNeverReloads(t *testing.T) {
	path := newTempProfile(t, "alice:reader:token-one\n")
	src, advance, err := newClockedSource(t, path, -1, nil)
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	if err := os.WriteFile(path, []byte("bob:reader:token-two\n"), ProfileFileMode); err != nil {
		t.Fatalf("update profile: %v", err)
	}
	advance(time.Hour * 24)
	p, err := src.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if _, ok := p.Lookup("token-two"); ok {
		t.Errorf("reloading with a negative reload interval")
	}
	if _, ok := p.Lookup("token-one"); !ok {
		t.Errorf("startup revision lost with a negative reload interval")
	}
}

// mustLookup resolves token against the source's current revision.
func mustLookup(t *testing.T, s *Source, token string) (Entry, bool) {
	t.Helper()
	p, err := s.Profile()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	return p.Lookup(token)
}