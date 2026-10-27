package console

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
)

// Role enumerates the two console-effective user classes (Spec §8.7.1).
type Role string

const (
	RoleViewer   Role = "viewer"
	RoleOperator Role = "operator"
)

// IsValidRole reports whether r is one of the recognized role identifiers.
func IsValidRole(r Role) bool {
	return r == RoleViewer || r == RoleOperator
}

// NormalizeRole maps upstream/legacy role names ("reader") onto the console
// role taxonomy and rejects unknown roles.
func NormalizeRole(s string) (Role, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "viewer", "reader":
		return RoleViewer, nil
	case "operator", "admin", "writer":
		return RoleOperator, nil
	default:
		return "", errors.New("console: unrecognized role")
	}
}

// Principal identifies the authenticated console caller.
type Principal struct {
	Subject string
	Role    Role
}

// CanMutate reports whether the principal may invoke mutating endpoints.
func (p Principal) CanMutate() bool { return p.Role == RoleOperator }

// HashToken returns a stable hex-encoded sha256 hash of the supplied bearer
// token. Hashing avoids storing plaintext credentials in SQLite.
func HashToken(tok string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(tok)))
	return hex.EncodeToString(sum[:])
}

// principalCtxKey is the context key used to ferry the authenticated
// principal across handler boundaries.
type principalCtxKey struct{}

// WithPrincipal returns a new context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFromContext returns the principal previously installed with
// WithPrincipal, or the zero value if absent.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(Principal)
	return p, ok
}

// bearerToken extracts the bearer token from an HTTP Authorization header.
func bearerToken(h string) string {
	const pfx = "Bearer "
	if len(h) < len(pfx) || !strings.EqualFold(h[:len(pfx)], pfx) {
		return ""
	}
	return strings.TrimSpace(h[len(pfx):])
}

// Authenticate resolves the bearer token on r into a Principal.
//
// The credential is taken from the Authorization header only: the former
// ?access_token= query path is gone, because query credentials leak into
// access logs, browser history, and Referer headers.
//
// When an access profile is configured it is the sole credential source, so
// the console and the daemons agree on who a token belongs to. Otherwise the
// legacy console-local token table applies.
func (g *Gateway) Authenticate(r *http.Request) (Principal, error) {
	tok := bearerToken(r.Header.Get("Authorization"))
	if tok == "" {
		return Principal{}, errors.New("missing bearer token")
	}
	if g.profile != nil {
		p, err := g.profile.Profile()
		if err != nil {
			return Principal{}, err
		}
		entry, ok := p.Lookup(tok)
		if !ok {
			return Principal{}, ErrCredentialRejected
		}
		role, err := NormalizeRole(string(entry.Role))
		if err != nil {
			return Principal{}, err
		}
		return Principal{Subject: entry.Subject, Role: role}, nil
	}
	hash := HashToken(tok)
	subject, role, err := g.db.LookupToken(r.Context(), hash)
	if err != nil {
		return Principal{}, err
	}
	return Principal{Subject: subject, Role: Role(role)}, nil
}

// AnonymousSubject is the reserved console subject for a request admitted
// without a credential under anonymous browsing. It is not a value any access
// profile entry can declare, so anonymous activity is never attributed to a
// named user.
const AnonymousSubject = "anonymous"

// AnonymousPrincipal returns the read-only principal used for a request
// admitted without a credential under anonymous browsing.
func AnonymousPrincipal() Principal {
	return Principal{Subject: AnonymousSubject, Role: RoleViewer}
}

// ErrCredentialRejected reports that the presented credential matched no
// access-profile entry. It is distinct from a missing credential so the
// gateway can tell the caller which of the two happened.
var ErrCredentialRejected = errors.New("console: invalid bearer token")

// Session describes how a console request was admitted.
type Session struct {
	// Principal is the effective identity of the caller.
	Principal Principal
	// Anonymous is true when the caller was admitted without a valid
	// credential, either because none was presented or because the presented
	// one was rejected.
	Anonymous bool
	// CredentialRejected is true when the caller presented a credential that
	// matched no access-profile entry. It is false when no credential was
	// presented, so a guest by configuration is distinguishable from a guest
	// whose credential was rejected.
	CredentialRejected bool
}

// authenticateStrict resolves the caller without ever downgrading.
//
// A request with no credential is admitted as the anonymous guest when
// anonymous browsing is enabled and rejected otherwise. A request that
// presents a credential matching no access-profile entry is ALWAYS rejected,
// even when anonymous browsing is enabled: an explicit authentication attempt
// that fails must report the failure, because silently serving the request as
// a guest would tell the user their token was accepted when it was not.
//
// This is the strict path used by the identity endpoint, which is how a login
// submission learns whether its credential was accepted.
func (g *Gateway) authenticateStrict(r *http.Request) (Session, error) {
	if bearerToken(r.Header.Get("Authorization")) == "" {
		if g.cfg.Auth.AllowAnonymousBrowsing {
			return Session{Principal: AnonymousPrincipal(), Anonymous: true}, nil
		}
		return Session{}, errors.New("missing bearer token")
	}
	p, err := g.Authenticate(r)
	if err != nil {
		return Session{}, err
	}
	return Session{Principal: p}, nil
}

// authenticateOrAnonymous resolves the caller, admitting an anonymous
// read-only principal when anonymous browsing is enabled.
//
// A credential that is present but matches no access-profile entry is handled
// like no credential at all: on a read endpoint it is downgraded to the
// anonymous read-only principal when anonymous browsing is enabled, and the
// rejection is reported through Session.CredentialRejected so the caller still
// learns that their credential was not accepted. On a mutating endpoint it is
// always rejected.
//
// This lenient path serves the console's content endpoints, where a stale
// credential in a browser should degrade to read-only guest access rather
// than break the dashboard. An explicit login attempt goes through
// authenticateStrict instead, so a wrong credential is always reported.
func (g *Gateway) authenticateOrAnonymous(r *http.Request) (Session, error) {
	if bearerToken(r.Header.Get("Authorization")) == "" {
		if g.cfg.Auth.AllowAnonymousBrowsing {
			return Session{Principal: AnonymousPrincipal(), Anonymous: true}, nil
		}
		return Session{}, errors.New("missing bearer token")
	}
	p, err := g.Authenticate(r)
	if err == nil {
		return Session{Principal: p}, nil
	}
	if errors.Is(err, ErrCredentialRejected) && g.cfg.Auth.AllowAnonymousBrowsing && !isMutatingMethod(r.Method) {
		return Session{Principal: AnonymousPrincipal(), Anonymous: true, CredentialRejected: true}, nil
	}
	return Session{}, err
}

// isMutatingMethod reports whether the method mutates state.
func isMutatingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}
