// Package accessprofile implements VeriProc's shared, passwd-like access
// profile: the single file that binds human-readable subjects to reader and
// operator bearer credentials for a group of instances.
//
// The format is line oriented, ':'-separated, with an optional trailing label
// comment:
//
//	alice:reader:1c7e92aa...        # Alice - laptop CLI
//	bob:operator:sha256:9f2c41b...  # Bob - shared ops token
//
// Blank lines and lines whose first non-space character is '#' carry no entry;
// both are preserved when the profile is rewritten. A token field may hold a
// literal token or, when marked with the "<digest>:" prefix, the SHA-256 hex
// digest of a token. Verification always hashes the presented token and
// compares digests, so both forms authenticate identically.
//
// The package deliberately has no dependencies beyond the standard library so
// both the daemon and the console gateway can consume it without dragging in
// an authority of their own.
package accessprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Role is the access scope bound to a subject in an access profile. The
// taxonomy is exactly the VeriProc API roles: reader and operator. No other
// value is accepted by the parser.
type Role string

// The only roles an access profile may declare.
const (
	// RoleReader may read API and console content but must not mutate state.
	RoleReader Role = "reader"
	// RoleOperator may read and mutate state.
	RoleOperator Role = "operator"
)

// digestPrefix marks a token field that already carries a SHA-256 digest of a
// token rather than the token itself.
const digestPrefix = "sha256:"

// Sentinel errors returned by the parser, attributable via errors.Is.
var (
	// ErrFields indicates fewer than the three required fields were present.
	ErrFields = errors.New("accessprofile: entry needs subject, role, and token")
	// ErrTokenColon indicates a literal token containing the field separator.
	ErrTokenColon = errors.New("accessprofile: token must not contain ':'")
	// ErrSubject indicates an empty subject.
	ErrSubject = errors.New("accessprofile: empty subject")
	// ErrRole indicates a role outside the reader/operator taxonomy.
	ErrRole = errors.New("accessprofile: role must be reader or operator")
	// ErrToken indicates an empty token field.
	ErrToken = errors.New("accessprofile: empty token")
	// ErrDigest indicates a digest-marked token field that is not a SHA-256
	// hex digest.
	ErrDigest = errors.New("accessprofile: digest token is not a sha256 hex digest")
	// ErrSubjectExists indicates a revoke or add targeting a subject already
	// present when uniqueness is required.
	ErrSubjectExists = errors.New("accessprofile: subject already present")
	// ErrNoSubject indicates a revoke targeting an absent subject.
	ErrNoSubject = errors.New("accessprofile: subject not present")
	// ErrAmbiguousToken indicates one token bound to two different subjects.
	ErrAmbiguousToken = errors.New("accessprofile: token bound to two subjects")
)

// ParseRole validates a role token, returning ErrRole for anything outside
// the reader/operator taxonomy.
func ParseRole(s string) (Role, error) {
	switch Role(s) {
	case RoleReader:
		return RoleReader, nil
	case RoleOperator:
		return RoleOperator, nil
	}
	return "", fmt.Errorf("%w %q", ErrRole, s)
}

// IsValidRole reports whether r is an accepted access-profile role.
func IsValidRole(r Role) bool {
	_, err := ParseRole(string(r))
	return err == nil
}

// Digest returns the SHA-256 hex digest of token, the form compared against
// token fields during verification. Surrounding whitespace is dropped so that
// a token pasted with a trailing newline still authenticates.
func Digest(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

// Entry is one access-profile line bound to a subject.
type Entry struct {
	// Subject is the human-readable name the role is granted to.
	Subject string
	// Role is the access scope of the entry.
	Role Role
	// Label is the free-form annotation from the trailing comment, e.g. the
	// human or device the credential was issued to. Empty when absent.
	Label string

	// tokenField is the token exactly as written, literal or digest-marked,
	// so rewriting a profile never silently converts one form to the other.
	tokenField string
}

// TokenField returns the token field as it is written in the profile file.
// Management commands must not print it.
func (e Entry) TokenField() string { return e.tokenField }

// NewEntry builds an entry whose token field stores the SHA-256 digest of the
// supplied plaintext token, so a profile minted by the management command
// never carries a usable credential on disk.
func NewEntry(subject string, role Role, label, plaintext string) Entry {
	return Entry{
		Subject:    strings.TrimSpace(subject),
		Role:       role,
		Label:      strings.TrimSpace(label),
		tokenField: digestPrefix + Digest(plaintext),
	}
}

// Format renders an entry as a profile line.
func (e Entry) Format() string {
	line := e.Subject + ":" + string(e.Role) + ":" + e.tokenField
	if e.Label != "" {
		line += "  # " + e.Label
	}
	return line
}

// LineError reports a malformed access-profile line, naming the line so an
// operator can fix a hand-edit without guessing.
type LineError struct {
	// Line is the 1-based line number in the file.
	Line int
	// Text is the offending line.
	Text string
	// Err is the underlying cause.
	Err error
}

func (e *LineError) Error() string {
	return fmt.Sprintf("line %d: %s: %s", e.Line, e.Err, e.Text)
}

func (e *LineError) Unwrap() error { return e.Err }

// Profile is a validated access-profile revision. It carries the original
// file text so a rewrite preserves comments and entry ordering exactly.
type Profile struct {
	raw     []fileLine
	entries []Entry
	// lines[i] is the index into raw backing entries[i].
	lines []int
	// digests maps an entry token digest to its position in entries.
	digests map[string]int
	// subjects maps a subject to the positions of its entries.
	subjects map[string][]int
}

// fileLine is one raw profile line together with whether it carries an entry.
type fileLine struct {
	text  string
	entry int // index into Profile.entries, or -1
}

// Parse validates an access-profile revision. Every entry line must have
// exactly three ':'-separated fields - subject, role, token - except for a
// digest-marked token field, which consumes a fourth. Malformed entries are
// rejected as a whole: a revision either validates or grants nothing.
func Parse(data []byte) (*Profile, error) {
	return parseLines(strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"))
}

// parseLines validates raw profile lines into a Profile.
func parseLines(raw []string) (*Profile, error) {
	p := &Profile{
		raw:      make([]fileLine, len(raw)),
		entries:  make([]Entry, 0, len(raw)),
		digests:  make(map[string]int),
		subjects: make(map[string][]int),
	}
	for i, line := range raw {
		p.raw[i] = fileLine{text: line, entry: -1}
	}

	for i, line := range raw {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue // blank or comment line: carried through, never an entry
		}
		e, err := parseEntry(trimmed, i+1)
		if err != nil {
			return nil, err
		}
		d := e.tokenField
		if isDigestField(d) {
			d = strings.TrimPrefix(d, digestPrefix)
		} else {
			d = Digest(d)
		}
		if prev, ok := p.digests[d]; ok {
			return nil, &LineError{
				Line: i + 1,
				Text: line,
				Err:  fmt.Errorf("%w (subject %q)", ErrAmbiguousToken, p.entries[prev].Subject),
			}
		}
		pos := len(p.entries)
		p.entries = append(p.entries, e)
		p.lines = append(p.lines, i)
		p.raw[i].entry = pos
		p.digests[d] = pos
		p.subjects[e.Subject] = append(p.subjects[e.Subject], pos)
	}
	return p, nil
}

// parseEntry validates a single entry line into an Entry, splitting off any
// trailing '#' label. A label is retained only when the entry itself is well
// formed, so an annotation never survives attached to a rejected line.
func parseEntry(line string, lineNo int) (Entry, error) {
	fail := func(err error) (Entry, error) {
		return Entry{}, &LineError{Line: lineNo, Text: line, Err: err}
	}

	body, label := line, ""
	if idx := strings.Index(line, "#"); idx >= 0 {
		body = strings.TrimSpace(line[:idx])
		label = strings.TrimSpace(line[idx+1:])
	}
	if body == "" {
		return fail(ErrFields)
	}

	fields := strings.SplitN(body, ":", 4)
	switch {
	case len(fields) < 3:
		return fail(ErrFields)
	case strings.TrimSpace(fields[0]) == "":
		return fail(ErrSubject)
	}
	subject := strings.TrimSpace(fields[0])

	role, err := ParseRole(strings.TrimSpace(fields[1]))
	if err != nil {
		return fail(err)
	}

	var tokenField string
	switch {
	case len(fields) == 3:
		tokenField = fields[2]
	default:
		// A fourth field is only legal as the digest marker followed by the
		// digest itself; anything else means a ':' inside the token.
		if !isDigestMarker(fields[2]) {
			return fail(ErrTokenColon)
		}
		tokenField = digestPrefix + fields[3]
		if !isDigestField(tokenField) {
			return fail(ErrDigest)
		}
	}
	tokenField = strings.TrimSpace(tokenField)
	if tokenField == "" {
		return fail(ErrToken)
	}
	if isDigestField(tokenField) && !isDigestFieldValid(tokenField) {
		return fail(ErrDigest)
	}

	return Entry{
		Subject:    subject,
		Role:       role,
		Label:      label,
		tokenField: tokenField,
	}, nil
}

// isDigestMarker reports whether s marks a digest-form token field.
func isDigestMarker(s string) bool { return strings.TrimSpace(s) == "sha256" }

// isDigestField reports whether the token field carries the digest prefix.
func isDigestField(s string) bool { return strings.HasPrefix(s, digestPrefix) }

// isDigestFieldValid reports whether a digest-marked token field holds a
// complete SHA-256 hex digest.
func isDigestFieldValid(s string) bool {
	d := strings.TrimPrefix(s, digestPrefix)
	if len(d) != 2*sha256.Size {
		return false
	}
	_, err := hex.DecodeString(d)
	return err == nil
}

// Bytes returns the profile's file content, preserving comment lines, blank
// lines, and entry ordering exactly as they were read.
func (p *Profile) Bytes() []byte {
	return []byte(strings.Join(p.texts(), "\n"))
}

// texts lists the raw lines of the profile.
func (p *Profile) texts() []string {
	out := make([]string, len(p.raw))
	for i, l := range p.raw {
		out[i] = l.text
	}
	return out
}

// Entries returns the validated entries in file order.
func (p *Profile) Entries() []Entry {
	out := make([]Entry, len(p.entries))
	copy(out, p.entries)
	return out
}

// Len returns the number of entry lines in the profile.
func (p *Profile) Len() int { return len(p.entries) }

// Lookup resolves a presented bearer token to the entry that authorizes it,
// hashing the token exactly once. Reports false when no entry matches.
func (p *Profile) Lookup(token string) (Entry, bool) {
	pos, ok := p.digests[Digest(token)]
	if !ok {
		return Entry{}, false
	}
	return p.entries[pos], true
}

// Subject resolves the first entry bound to subject.
func (p *Profile) Subject(subject string) (Entry, bool) {
	positions := p.subjects[subject]
	if len(positions) == 0 {
		return Entry{}, false
	}
	return p.entries[positions[0]], true
}

// HasSubject reports whether any entry is bound to subject.
func (p *Profile) HasSubject(subject string) bool { return len(p.subjects[subject]) > 0 }

// Append returns a rewritten profile with e added as the final entry, leaving
// every existing line - including comments - untouched. The subject must not
// already be present, so extending an existing subject stays an explicit,
// manual edit rather than a silent role upgrade.
func (p *Profile) Append(e Entry) (*Profile, error) {
	e.Subject = strings.TrimSpace(e.Subject)
	e.Label = strings.TrimSpace(e.Label)
	if e.Subject == "" {
		return nil, ErrSubject
	}
	if !IsValidRole(e.Role) {
		return nil, fmt.Errorf("%w %q", ErrRole, e.Role)
	}
	if e.tokenField == "" {
		return nil, ErrToken
	}
	if p.HasSubject(e.Subject) {
		return nil, fmt.Errorf("%w %q", ErrSubjectExists, e.Subject)
	}

	content := string(p.Bytes())
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += e.Format() + "\n"
	return Parse([]byte(content))
}

// Revoke returns a rewritten profile with every entry bound to subject
// removed, returning ErrNoSubject when the subject is unknown.
func (p *Profile) Revoke(subject string) (*Profile, error) {
	positions := p.subjects[subject]
	if len(positions) == 0 {
		return nil, fmt.Errorf("%w %q", ErrNoSubject, subject)
	}
	drop := make(map[int]bool, len(positions))
	for _, pos := range positions {
		drop[p.lines[pos]] = true
	}
	var lines []string
	for i, l := range p.raw {
		if drop[i] {
			continue
		}
		lines = append(lines, l.text)
	}
	return parseLines(lines)
}