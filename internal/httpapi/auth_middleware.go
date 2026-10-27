package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gobobr/veriproc/internal/auth"
	"github.com/gobobr/veriproc/internal/httpapi/apierr"
)

// authMiddleware enforces bearer-token authentication on /api/v1/* paths
// (excluding /api/v1/health, /api/v1/readiness, /api/v1/version which remain
// open for platform probes). Spec §2.14 / M6.
//
// Three modes are expressed by the arguments:
//
//   - authn == nil: authentication disabled, the middleware is a no-op
//     (M0–M5 compatibility and the sandbox open-instance mode).
//   - authn != nil, allowAnonymousReaders == false: every request subject to
//     authentication must carry a valid credential.
//   - authn != nil, allowAnonymousReaders == true: a request with no
//     credential on a read endpoint is admitted as an anonymous reader; a
//     request with no credential on a mutating endpoint is rejected. A
//     credential that is present but unknown is always rejected - it is never
//     downgraded to anonymous, which would mask a mistyped token.
//
// Rejections name the credential or role required and never echo a token.
func authMiddleware(authn auth.Authenticator, allowAnonymousReaders bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authn == nil || !requiresAuth(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			tok := bearerToken(r.Header.Get("Authorization"))

			if tok == "" {
				if allowAnonymousReaders && !auth.IsMutator(r.Method) {
					ctx := auth.WithPrincipal(r.Context(), auth.AnonymousPrincipal())
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				w.Header().Set("WWW-Authenticate", `Bearer realm="veriproc"`)
				apierr.Write(w, r, http.StatusUnauthorized,
					apierr.CodeUnauthenticated, missingCredentialMessage(allowAnonymousReaders, auth.IsMutator(r.Method)))
				return
			}

			p, err := authn.Authenticate(r.Context(), tok)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="veriproc", error="invalid_token"`)
				switch {
				case errors.Is(err, auth.ErrMissingCredentials):
					apierr.Write(w, r, http.StatusUnauthorized,
						apierr.CodeUnauthenticated, missingCredentialMessage(allowAnonymousReaders, auth.IsMutator(r.Method)))
				default:
					apierr.Write(w, r, http.StatusUnauthorized,
						apierr.CodeUnauthenticated, invalidCredentialMessage)
				}
				return
			}
			// Reader role may not invoke mutating endpoints.
			if p.Role != auth.RoleOperator && auth.IsMutator(r.Method) {
				apierr.Write(w, r, http.StatusForbidden,
					apierr.CodeUnauthorized, insufficientRoleMessage)
				return
			}
			ctx := auth.WithPrincipal(r.Context(), p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// The rejection messages are part of the API contract (see the access-profiles
// change): they state which credential or role is required so a client can
// guide the operator, and they never contain a token value.

const (
	// invalidCredentialMessage reports a credential that no access-profile
	// entry matches.
	invalidCredentialMessage = "invalid bearer token: no access-profile entry matches this credential; " +
		"ask an operator to grant you a reader or operator token"
	// insufficientRoleMessage reports a read-only credential on a mutating
	// endpoint.
	insufficientRoleMessage = "operator authorization required: this endpoint mutates state; " +
		"supply an operator credential"
)

// missingCredentialMessage reports a request with no credential at all,
// naming the accepted roles and, when anonymous readers are allowed, the
// narrower scope an anonymous caller has.
func missingCredentialMessage(anonymousAllowed, mutating bool) string {
	const head = "missing bearer token: supply Authorization: Bearer <token> with a "
	if mutating {
		if anonymousAllowed {
			return head + "reader or operator credential; anonymous readers may read but not mutate state"
		}
		return head + "reader or operator credential"
	}
	if anonymousAllowed {
		return head + "reader or operator credential to act as a named user; " +
			"this instance allows anonymous read-only access"
	}
	return head + "reader or operator credential"
}

// quotaMiddleware enforces per-principal mutating-request quotas. When q is
// nil, the middleware is a no-op.
func quotaMiddleware(q *auth.QuotaEnforcer) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if q == nil || !auth.IsMutator(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			p := auth.PrincipalFromContext(r.Context())
			if p == nil {
				next.ServeHTTP(w, r)
				return
			}
			if !q.Allow(p) {
				apierr.WriteWithDetail(w, r, http.StatusTooManyRequests, apierr.Detail{
					Code:      apierr.CodeQuotaExceeded,
					Message:   "per-principal mutating-request quota exceeded",
					Retryable: apierr.BoolPtr(true),
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// requiresAuth reports whether the URL path is subject to bearer-token auth.
func requiresAuth(p string) bool {
	if !strings.HasPrefix(p, "/api/v1/") && p != "/api/v1" {
		return false
	}
	switch p {
	case "/api/v1/health", "/api/v1/readiness", "/api/v1/version":
		return false
	}
	return true
}

// bearerToken extracts the token from an "Authorization: Bearer …" header.
func bearerToken(h string) string {
	const pfx = "Bearer "
	if len(h) < len(pfx) || !strings.EqualFold(h[:len(pfx)], pfx) {
		return ""
	}
	return strings.TrimSpace(h[len(pfx):])
}
