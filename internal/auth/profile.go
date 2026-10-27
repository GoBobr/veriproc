package auth

import (
	"context"
	"errors"
	"strings"

	"github.com/gobobr/veriproc/internal/accessprofile"
)

// AnonymousSubject is the reserved principal subject used for requests
// admitted without a credential under an anonymous-reader allowance. It is
// deliberately not a value any access-profile entry can declare, so audit
// records and logs can never mistake anonymous activity for a named user.
const AnonymousSubject = "anonymous"

// ErrAnonymousDenied reports that a request carried no credential and the
// caller is not permitted to act anonymously. The middleware maps it to a 401
// that names the credential required.
var ErrAnonymousDenied = errors.New("auth: anonymous access not permitted")

// ProfileAuthenticator validates bearer tokens against a live access-profile
// source, so a grant or a revocation takes effect within the source's reload
// interval without restarting the process.
//
// It satisfies Authenticator and is safe for concurrent use.
type ProfileAuthenticator struct {
	src *accessprofile.Source
	// quota is the per-principal mutating-request quota applied to
	// profile-authenticated principals. Zero disables it.
	quota int
}

// NewProfileAuthenticator wraps src. A quota of zero disables per-principal
// quotas.
func NewProfileAuthenticator(src *accessprofile.Source, quota int) *ProfileAuthenticator {
	return &ProfileAuthenticator{src: src, quota: quota}
}

// Authenticate resolves the supplied bearer token (already stripped of the
// "Bearer " prefix) against the current profile revision. Returns
// ErrMissingCredentials for an empty token and ErrInvalidToken when no entry
// matches.
func (a *ProfileAuthenticator) Authenticate(_ context.Context, token string) (*Principal, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, ErrMissingCredentials
	}
	p, err := a.src.Profile()
	if err != nil {
		return nil, err
	}
	entry, ok := p.Lookup(token)
	if !ok {
		return nil, ErrInvalidToken
	}
	return &Principal{
		Subject:        entry.Subject,
		Role:           Role(entry.Role),
		QuotaPerMinute: a.quota,
	}, nil
}

// AnonymousPrincipal returns the principal used for a request admitted
// without a credential under an anonymous-reader allowance. It never carries
// a quota: anonymous callers are read-only by construction, so there is no
// mutating traffic to bound.
func AnonymousPrincipal() *Principal {
	return &Principal{Subject: AnonymousSubject, Role: RoleReader}
}

// ProfileSource exposes the underlying profile source, for callers that need
// to inspect entries (for example the CLI's operator gate).
func (a *ProfileAuthenticator) ProfileSource() *accessprofile.Source { return a.src }