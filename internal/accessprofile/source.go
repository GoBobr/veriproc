package accessprofile

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// DefaultReloadInterval is how often a Source re-statutes its profile file
// when Options.ReloadInterval is not set.
const DefaultReloadInterval = 5 * time.Second

// WarnFunc receives non-fatal profile warnings such as a permissive file mode
// or a reload that could not be applied. A nil WarnFunc discards warnings.
type WarnFunc func(error)

// Options configures a Source.
type Options struct {
	// ReloadInterval is the bounded interval within which a modified profile
	// file becomes visible to Profile. Zero selects DefaultReloadInterval;
	// a negative value disables re-checking, so the file is only read at
	// startup and an operator restart is required to notice changes.
	ReloadInterval time.Duration
	// Now overrides the time source. Defaults to time.Now.
	Now func() time.Time
	// Warn receives reload and permission warnings. Nil discards them.
	Warn WarnFunc
}

// Source is the live view of one access-profile file held by a long-running
// consumer. It re-confirms the file at most once per ReloadInterval and swaps
// in a new revision when one is available and valid. A revision that fails
// validation never becomes active: the previous one stays in effect, so a bad
// hand-edit cannot revoke everyone until it is fixed.
type Source struct {
	path string
	opts Options

	// mu guards everything below. Profile is on the request path, so file
	// work only happens while the lock is held after an interval elapsed.
	mu        sync.Mutex
	prof      *Profile
	stamp     os.FileInfo
	checkedAt time.Time
	// warned persists a file-permission warning so it is emitted once per
	// profile revision rather than on every check.
	warned bool
}

// NewSource loads path immediately. The returned error covers an unreadable
// file or a revision that does not validate, since starting with credentials
// that can never authenticate is worse than refusing to start.
func NewSource(path string, opts Options) (*Source, error) {
	if opts.ReloadInterval == 0 {
		opts.ReloadInterval = DefaultReloadInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	s := &Source{path: path, opts: opts}
	if err := s.reload(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path returns the profile file this source reads.
func (s *Source) Path() string { return s.path }

// Profile returns the current validated revision, re-checking the file when
// the configured reload interval has elapsed. A reload that cannot be
// satisfied - unreadable, vanished, or malformed - keeps the last validated
// revision and reports the problem through Warn.
func (s *Source) Profile() (*Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.prof == nil {
		return nil, fmt.Errorf("accessprofile %s: no profile loaded", s.path)
	}
	if s.opts.ReloadInterval < 0 {
		return s.prof, nil
	}
	if s.checkedAt.Add(s.opts.ReloadInterval).After(s.opts.Now()) {
		return s.prof, nil
	}
	// reload reports an error only while no revision has ever loaded; once
	// one exists failures degrade to warnings, so a bad file never locks
	// every credential out mid-run.
	_ = s.reload()
	return s.prof, nil
}

// reload re-checks the file and, when a revision changed and validates,
// installs it. Callers must hold the lock.
func (s *Source) reload() error {
	now := s.opts.Now()
	defer func() { s.checkedAt = now }()

	info, err := os.Stat(s.path)
	if err != nil {
		return s.reloadFailure(fmt.Errorf("stat %s: %w", s.path, err))
	}
	if s.stamp != nil && sameFile(s.stamp, info) {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return s.reloadFailure(fmt.Errorf("read %s: %w", s.path, err))
	}
	prof, err := Parse(data)
	if err != nil {
		return s.reloadFailure(fmt.Errorf("%s: keeping the previous revision: %w", s.path, err))
	}
	s.prof = prof
	s.stamp = info
	if info.Mode().Perm()&0o077 != 0 {
		s.warnf("access profile %s is readable by group or others (mode %o); profile files should be owner-only",
			s.path, info.Mode().Perm())
	}
	return nil
}

// reloadFailure records a failed reload. Once a revision is loaded the
// failure becomes a warning; before any revision exists it is returned so
// construction can fail loudly.
func (s *Source) reloadFailure(err error) error {
	if s.prof != nil {
		s.warnf("access profile reload failed: %v", err)
		return nil
	}
	return fmt.Errorf("accessprofile: %w", err)
}

// sameFile reports whether two stat results plausibly refer to the unchanged
// revision of a file.
func sameFile(now, old os.FileInfo) bool {
	return now.Size() == old.Size() && now.ModTime().Equal(old.ModTime())
}

// warnf reports a non-fatal problem to the configured warning sink.
func (s *Source) warnf(format string, args ...any) {
	if s.opts.Warn == nil {
		return
	}
	s.opts.Warn(fmt.Errorf(format, args...))
}