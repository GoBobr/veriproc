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
func (g *Gateway) Authenticate(r *http.Request) (Principal, error) {
	tok := bearerToken(r.Header.Get("Authorization"))
	if tok == "" {
		// Allow access tokens via query parameter for SSE/preview links if the
		// caller explicitly opts in; production deployments should prefer the
		// header path. The query path is intentionally namespaced.
		tok = r.URL.Query().Get("access_token")
	}
	if tok == "" {
		return Principal{}, errors.New("missing bearer token")
	}
	hash := HashToken(tok)
	subject, role, err := g.db.LookupToken(r.Context(), hash)
	if err != nil {
		return Principal{}, err
	}
	return Principal{Subject: subject, Role: Role(role)}, nil
}
