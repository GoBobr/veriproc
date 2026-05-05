// Package auth provides authentication and per-principal quota enforcement
// for the VeriProc REST API (Milestone 6).
//
// The package is intentionally small: it ships with a static bearer-token
// authenticator and an in-memory token-bucket rate limiter sufficient for the
// stub-executor profile and CI. Production deployments are expected to swap
// in a different Authenticator implementation (e.g. backed by api_credentials
// rows in the store).
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"
)

// Sentinel errors used by Authenticator implementations and middleware.
var (
	ErrMissingCredentials = errors.New("auth: missing bearer token")
	ErrInvalidToken       = errors.New("auth: invalid bearer token")
	ErrForbidden          = errors.New("auth: forbidden")
	ErrQuotaExceeded      = errors.New("auth: quota exceeded")
)

// Role is a coarse role label used by handlers to distinguish read-only from
// mutating callers.
type Role string

const (
	// RoleReader may invoke GET endpoints only.
	RoleReader Role = "reader"
	// RoleOperator may invoke all endpoints, including POST /tasks and
	// run cancellation/promotion.
	RoleOperator Role = "operator"
)

// Principal describes the authenticated caller.
type Principal struct {
	Subject string
	Role    Role
	// QuotaPerMinute is the maximum number of mutating requests this principal
	// may issue per rolling 60-second window. Zero disables the limit.
	QuotaPerMinute int
}

// IsMutator reports whether method is one of POST/PUT/PATCH/DELETE.
func IsMutator(method string) bool {
	switch method {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// Authenticator validates a bearer token and returns the associated Principal.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (*Principal, error)
}

// StaticAuthenticator is an in-memory Authenticator backed by a token map.
// Tokens are stored hashed; lookups hash the supplied token before compare.
type StaticAuthenticator struct {
	mu     sync.RWMutex
	byHash map[string]Principal
}

// NewStaticAuthenticator constructs a StaticAuthenticator preloaded with the
// given (token → principal) entries.
func NewStaticAuthenticator(entries map[string]Principal) *StaticAuthenticator {
	a := &StaticAuthenticator{byHash: make(map[string]Principal, len(entries))}
	for tok, p := range entries {
		a.byHash[hashToken(tok)] = p
	}
	return a
}

// Authenticate looks up the supplied bearer token (already stripped of the
// "Bearer " prefix). Returns ErrMissingCredentials for an empty token and
// ErrInvalidToken if the token is unknown.
func (a *StaticAuthenticator) Authenticate(_ context.Context, token string) (*Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrMissingCredentials
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	p, ok := a.byHash[hashToken(token)]
	if !ok {
		return nil, ErrInvalidToken
	}
	return &p, nil
}

// AddToken registers an additional (token → principal) entry.
func (a *StaticAuthenticator) AddToken(token string, p Principal) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byHash[hashToken(token)] = p
}

// hashToken returns the SHA-256 hex digest of a bearer token.
func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// QuotaEnforcer enforces per-principal mutating-request quotas on a sliding
// 60-second window.
type QuotaEnforcer struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[string][]time.Time
}

// NewQuotaEnforcer returns a fresh QuotaEnforcer using the given clock (or
// time.Now if nil).
func NewQuotaEnforcer(now func() time.Time) *QuotaEnforcer {
	if now == nil {
		now = time.Now
	}
	return &QuotaEnforcer{now: now, windows: map[string][]time.Time{}}
}

// Allow records one mutating request for principal p and reports whether it
// is permitted. Calls beyond p.QuotaPerMinute within the trailing 60s window
// return false; the caller should respond with 429 quota_exceeded.
func (q *QuotaEnforcer) Allow(p *Principal) bool {
	if p == nil || p.QuotaPerMinute <= 0 {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	cutoff := now.Add(-time.Minute)
	hist := q.windows[p.Subject]
	pruned := hist[:0]
	for _, t := range hist {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) >= p.QuotaPerMinute {
		q.windows[p.Subject] = pruned
		return false
	}
	pruned = append(pruned, now)
	q.windows[p.Subject] = pruned
	return true
}

// principalCtxKey carries the Principal on the request context.
type principalCtxKey struct{}

// WithPrincipal returns a derived context carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFromContext extracts the Principal stamped by the auth middleware,
// or nil if none.
func PrincipalFromContext(ctx context.Context) *Principal {
	if v, ok := ctx.Value(principalCtxKey{}).(*Principal); ok {
		return v
	}
	return nil
}
