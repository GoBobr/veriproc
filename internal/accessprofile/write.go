package accessprofile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// ProfileFileMode is the mode a profile file is created with: owner-only,
// because every line of the file is either a usable credential or the digest
// of one plus the subject it is bound to.
const ProfileFileMode os.FileMode = 0o600

// WriteFile replaces path with data atomically.
//
// The new content must parse as a valid profile, otherwise nothing is
// written, so a failed update can never leave a file that authenticates
// nobody. The replacement is staged in a temporary file in the target
// directory and renamed into place; rename within a directory is atomic on
// POSIX and NFS, so a concurrent reader always observes a complete revision -
// never a truncated one.
func WriteFile(path string, data []byte) error {
	if _, err := Parse(data); err != nil {
		return fmt.Errorf("refusing to write %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("accessprofile: staging %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	if err := tmp.Chmod(ProfileFileMode); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("accessprofile: chmod %s: %w", tmp.Name(), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("accessprofile: write %s: %w", tmp.Name(), err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("accessprofile: sync %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("accessprofile: close %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("accessprofile: publish %s: %w", path, err)
	}
	return nil
}

// Save atomically publishes p to path using WriteFile semantics.
func Save(path string, p *Profile) error { return WriteFile(path, p.Bytes()) }

// LoadFile reads and validates the profile at path. It is the one-shot
// counterpart to Source, for callers that edit or inspect a file once rather
// than watching it.
func LoadFile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("accessprofile: read %s: %w", path, err)
	}
	return Parse(data)
}

// MintToken returns a fresh bearer token: 32 bytes of cryptographic randomness
// rendered as hex. It is printed exactly once by the management command and
// only its digest is written to the profile.
func MintToken() (string, error) {
	const rawBytes = 32
	b := make([]byte, rawBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("accessprofile: mint token: %w", err)
	}
	return hex.EncodeToString(b), nil
}